package vectorindex

import (
	"context"
	"errors"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

func TestProcessorSkipsEmbeddingForUnchangedStableContent(t *testing.T) {
	record, err := DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &fakeEmbedder{}
	hash := record.Document.StableEmbeddingHash(embedder.Provider(), embedder.Model(), embedder.ModelVersion(), embedder.Dimensions())
	index := &fakeVectorIndex{state: searchindex.VectorDocumentState{Found: true, LotVersion: 6, EmbeddingHash: hash, HasEmbedding: true}}
	processor := newTestProcessor(t, index, embedder)
	result, err := processor.Apply(context.Background(), record)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Applied || embedder.calls != 0 || len(index.lastEmbedding) != 0 {
		t.Fatalf("result=%+v embed_calls=%d vector=%v", result, embedder.calls, index.lastEmbedding)
	}
}

func TestProcessorEmbedsChangedVisibleContent(t *testing.T) {
	record, err := DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	index := &fakeVectorIndex{state: searchindex.VectorDocumentState{Found: true, LotVersion: 6, EmbeddingHash: "old", HasEmbedding: true}}
	embedder := &fakeEmbedder{}
	processor := newTestProcessor(t, index, embedder)
	if _, err := processor.Apply(context.Background(), record); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if embedder.calls != 1 || len(index.lastEmbedding) != 3 {
		t.Fatalf("embed_calls=%d vector=%v", embedder.calls, index.lastEmbedding)
	}
}

func TestProcessorReembedsSameLotVersionWhenModelIdentityChanges(t *testing.T) {
	record, err := DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	index := &fakeVectorIndex{state: searchindex.VectorDocumentState{
		Found: true, LotVersion: record.Document.LotVersion, LastEventID: record.Document.LastEventID,
		ContentHash: record.Document.ContentHash, EmbeddingHash: "old-model-hash", HasEmbedding: true,
	}}
	embedder := &fakeEmbedder{}
	processor := newTestProcessor(t, index, embedder)
	if _, err := processor.Apply(context.Background(), record); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if embedder.calls != 1 || len(index.lastEmbedding) != 3 {
		t.Fatalf("embed_calls=%d vector=%v", embedder.calls, index.lastEmbedding)
	}
}

func TestProcessorRepairsMissingEmbeddingAtSameLotVersion(t *testing.T) {
	record, err := DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &fakeEmbedder{}
	hash := record.Document.StableEmbeddingHash(embedder.Provider(), embedder.Model(), embedder.ModelVersion(), embedder.Dimensions())
	index := &fakeVectorIndex{state: searchindex.VectorDocumentState{
		Found: true, LotVersion: record.Document.LotVersion, LastEventID: record.Document.LastEventID,
		ContentHash: record.Document.ContentHash, EmbeddingHash: hash, HasEmbedding: false,
	}}
	processor := newTestProcessor(t, index, embedder)
	if _, err := processor.Apply(context.Background(), record); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if embedder.calls != 1 || len(index.lastEmbedding) != 3 {
		t.Fatalf("embed_calls=%d vector=%v", embedder.calls, index.lastEmbedding)
	}
}

func TestProcessorWrapsEmbeddingFailure(t *testing.T) {
	record, err := DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &fakeEmbedder{err: errors.New("provider unavailable")}
	processor := newTestProcessor(t, &fakeVectorIndex{}, embedder)
	if _, err := processor.Apply(context.Background(), record); !errors.Is(err, ErrEmbeddingFailure) {
		t.Fatalf("error=%v", err)
	}
}

func TestNewProcessorRequiresMatchingIdentity(t *testing.T) {
	index := &fakeVectorIndex{modelVersion: "different"}
	if _, err := NewProcessor(index, &fakeEmbedder{}); err == nil {
		t.Fatal("mismatched embedding identity was accepted")
	}
	if _, err := NewProcessor(nil, &fakeEmbedder{}); err == nil {
		t.Fatal("nil index was accepted")
	}
}

func newTestProcessor(t *testing.T, index *fakeVectorIndex, embedder *fakeEmbedder) *Processor {
	t.Helper()
	processor, err := NewProcessor(index, embedder)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

type fakeVectorIndex struct {
	state         searchindex.VectorDocumentState
	stateErr      error
	applyErr      error
	lastDocument  searchindex.LotDocument
	lastEmbedding []float64
	modelVersion  string
}

func (*fakeVectorIndex) Provider() string { return "dashscope" }
func (*fakeVectorIndex) Model() string    { return "text-embedding-v4" }
func (index *fakeVectorIndex) ModelVersion() string {
	if index.modelVersion != "" {
		return index.modelVersion
	}
	return "text-embedding-v4"
}
func (*fakeVectorIndex) Dimensions() int { return 3 }
func (index *fakeVectorIndex) DocumentState(context.Context, string) (searchindex.VectorDocumentState, error) {
	return index.state, index.stateErr
}
func (index *fakeVectorIndex) ApplyDocument(_ context.Context, document searchindex.LotDocument, embedding []float64) (searchindex.VectorApplyResult, error) {
	index.lastDocument = document
	index.lastEmbedding = append([]float64(nil), embedding...)
	return searchindex.VectorApplyResult{Applied: true}, index.applyErr
}

type fakeEmbedder struct {
	calls int
	err   error
}

func (*fakeEmbedder) Configured() bool     { return true }
func (*fakeEmbedder) Provider() string     { return "dashscope" }
func (*fakeEmbedder) Model() string        { return "text-embedding-v4" }
func (*fakeEmbedder) ModelVersion() string { return "text-embedding-v4" }
func (*fakeEmbedder) Dimensions() int      { return 3 }
func (embedder *fakeEmbedder) Embed(context.Context, []string) ([][]float64, error) {
	embedder.calls++
	if embedder.err != nil {
		return nil, embedder.err
	}
	return [][]float64{{0.1, 0.2, 0.3}}, nil
}
