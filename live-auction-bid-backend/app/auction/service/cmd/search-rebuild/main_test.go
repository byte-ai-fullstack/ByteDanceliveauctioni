package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

func TestParseRebuildConfigRequiresExplicitVersionedTarget(t *testing.T) {
	getenv := func(string) string { return "" }
	if _, err := parseRebuildConfig(nil, getenv); err == nil {
		t.Fatal("write rebuild without target was accepted")
	}
	config, err := parseRebuildConfig([]string{
		"--target=auction-lots-v2", "--mapping=deploy/elasticsearch/index-v1.json", "--resume", "--page-size=250",
	}, getenv)
	if err != nil || config.Target != "auction-lots-v2" || !config.Resume || config.PageSize != 250 || !config.SwitchAlias {
		t.Fatalf("config=%+v error=%v", config, err)
	}
	if _, err := parseRebuildConfig([]string{"--target=auction-lots-current", "--mapping=x"}, getenv); err == nil {
		t.Fatal("alias name was accepted as a rebuild target")
	}
}

func TestParseRebuildConfigAllowsReadOnlyDryRun(t *testing.T) {
	config, err := parseRebuildConfig([]string{"--dry-run", "--max-documents=100"}, func(string) string { return "" })
	if err != nil || !config.DryRun || config.MaxDocuments != 100 {
		t.Fatalf("config=%+v error=%v", config, err)
	}
}

func TestParseRebuildConfigAcceptsBoundedPGVectorRebuild(t *testing.T) {
	config, err := parseRebuildConfig([]string{
		"--sink=pgvector", "--target=auction_lot_search_docs_v2", "--max-new-embeddings=250", "--resume",
	}, func(string) string { return "" })
	if err != nil || config.Sink != "pgvector" || config.Target != "auction_lot_search_docs_v2" ||
		config.MaxNewEmbeddings != 250 || !config.Resume || !config.SwitchTable {
		t.Fatalf("config=%+v error=%v", config, err)
	}
	if _, err := parseRebuildConfig([]string{"--sink=pgvector", "--target=auction_lot_search_docs"}, func(string) string { return "" }); err == nil {
		t.Fatal("canonical pgvector table was accepted as a rebuild target")
	}
	if _, err := parseRebuildConfig([]string{
		"--sink=pgvector", "--target=auction_lot_search_docs_v2", "--backup=unsafe;drop",
	}, func(string) string { return "" }); err == nil {
		t.Fatal("unsafe pgvector backup name was accepted")
	}
}

func TestPGVectorRebuilderReusesCompatibleEmbedding(t *testing.T) {
	document := rebuildVectorDocument(t)
	embedder := &fakeRebuildEmbedder{dimensions: 3}
	hash := document.StableEmbeddingHash(embedder.Provider(), embedder.Model(), embedder.ModelVersion(), embedder.Dimensions())
	source := &fakePGVectorRebuildIndex{reusableHash: hash, reusable: []float64{0.1, 0.2, 0.3}}
	target := &fakePGVectorRebuildIndex{}
	rebuilder := &pgvectorRebuilder{source: source, target: target, embedder: embedder, maxNewEmbeddings: 0}
	if err := rebuilder.Apply(context.Background(), document); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if embedder.calls != 0 || rebuilder.reusedEmbeddings != 1 || len(target.lastEmbedding) != 3 {
		t.Fatalf("embed_calls=%d reused=%d embedding=%v", embedder.calls, rebuilder.reusedEmbeddings, target.lastEmbedding)
	}
}

func TestPGVectorRebuilderStopsBeforeExceedingPaidCap(t *testing.T) {
	embedder := &fakeRebuildEmbedder{dimensions: 3, vector: []float64{0.1, 0.2, 0.3}}
	target := &fakePGVectorRebuildIndex{}
	rebuilder := &pgvectorRebuilder{
		source: &fakePGVectorRebuildIndex{}, target: target, embedder: embedder, maxNewEmbeddings: 1,
	}
	first := rebuildVectorDocument(t)
	if err := rebuilder.Apply(context.Background(), first); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	second := rebuildVectorDocument(t)
	second.LotID = "lot-2"
	second.LotVersion++
	second.LastEventID, _ = eventcontract.NewEventID()
	target.state = searchindex.VectorDocumentState{}
	if err := rebuilder.Apply(context.Background(), second); !errors.Is(err, errEmbeddingBudgetExceeded) {
		t.Fatalf("second Apply error=%v", err)
	}
	if embedder.calls != 1 || rebuilder.newEmbeddings != 1 {
		t.Fatalf("embed_calls=%d new_embeddings=%d", embedder.calls, rebuilder.newEmbeddings)
	}
}

func TestCaptureSampleIsBoundedAndDeterministic(t *testing.T) {
	var samples []searchindex.LotDocument
	for index, lotID := range []string{"lot-1", "lot-2", "lot-3", "lot-4"} {
		captureSample(&samples, 2, int64(index+1), searchindex.LotDocument{LotID: lotID})
	}
	if len(samples) != 2 || samples[0].LotID != "lot-3" || samples[1].LotID != "lot-4" {
		t.Fatalf("samples=%+v", samples)
	}
}

type fakePGVectorRebuildIndex struct {
	state         searchindex.VectorDocumentState
	reusableHash  string
	reusable      []float64
	lastEmbedding []float64
}

func (*fakePGVectorRebuildIndex) Provider() string     { return "dashscope" }
func (*fakePGVectorRebuildIndex) Model() string        { return "text-embedding-v4" }
func (*fakePGVectorRebuildIndex) ModelVersion() string { return "2026-07" }
func (*fakePGVectorRebuildIndex) Dimensions() int      { return 3 }
func (index *fakePGVectorRebuildIndex) DocumentState(context.Context, string) (searchindex.VectorDocumentState, error) {
	return index.state, nil
}
func (index *fakePGVectorRebuildIndex) HasCompatibleEmbedding(_ context.Context, _ string, hash string) (bool, error) {
	return hash == index.reusableHash && len(index.reusable) > 0, nil
}
func (index *fakePGVectorRebuildIndex) CompatibleEmbedding(_ context.Context, _ string, hash string) ([]float64, bool, error) {
	if hash != index.reusableHash || len(index.reusable) == 0 {
		return nil, false, nil
	}
	return append([]float64(nil), index.reusable...), true, nil
}
func (index *fakePGVectorRebuildIndex) ApplyDocument(_ context.Context, document searchindex.LotDocument, embedding []float64) (searchindex.VectorApplyResult, error) {
	index.lastEmbedding = append([]float64(nil), embedding...)
	index.state = searchindex.VectorDocumentState{
		Found: true, LotVersion: document.LotVersion, LastEventID: document.LastEventID,
		ContentHash: document.ContentHash, EmbeddingHash: document.EmbeddingHash, HasEmbedding: len(embedding) > 0,
	}
	return searchindex.VectorApplyResult{Applied: true}, nil
}

type fakeRebuildEmbedder struct {
	dimensions int
	vector     []float64
	calls      int
}

func (*fakeRebuildEmbedder) Configured() bool         { return true }
func (*fakeRebuildEmbedder) Provider() string         { return "dashscope" }
func (*fakeRebuildEmbedder) Model() string            { return "text-embedding-v4" }
func (*fakeRebuildEmbedder) ModelVersion() string     { return "2026-07" }
func (embedder *fakeRebuildEmbedder) Dimensions() int { return embedder.dimensions }
func (embedder *fakeRebuildEmbedder) Embed(context.Context, []string) ([][]float64, error) {
	embedder.calls++
	vector := embedder.vector
	if len(vector) == 0 {
		vector = []float64{0.1, 0.2, 0.3}
	}
	return [][]float64{append([]float64(nil), vector...)}, nil
}

func rebuildVectorDocument(t *testing.T) searchindex.LotDocument {
	t.Helper()
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	return searchindex.LotDocument{
		LotID: "lot-1", RoomID: "room-1", MainAccountID: "merchant-1", Title: "Jade vase", Description: "Vintage",
		Category: "jewelry", SearchText: "Jade vase\nVintage", Status: v1.LotStatus_LOT_STATUS_LIVE.String(),
		StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, CurrentPrice: &v1.Money{Amount: 12_000, Currency: "CNY"},
		StartsAtUnixMs: 100, EndsAtUnixMs: 200, Href: "/m/room/room-1", PublicVisible: true,
		LotVersion: 7, LastEventID: eventID, ContentHash: strings.Repeat("a", 64),
	}
}
