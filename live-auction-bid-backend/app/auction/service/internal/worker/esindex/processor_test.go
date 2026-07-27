package esindex

import (
	"context"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

func TestProcessorAppliesValidatedDocument(t *testing.T) {
	index := &fakeDocumentIndex{result: searchindex.ElasticsearchApplyResult{Applied: true}}
	processor, err := NewProcessor(index)
	if err != nil {
		t.Fatal(err)
	}
	record, err := searchstate.DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Apply(context.Background(), record)
	if err != nil || !result.Applied || index.calls != 1 || index.document.LotID != "lot-1" {
		t.Fatalf("result=%+v error=%v calls=%d document=%+v", result, err, index.calls, index.document)
	}
}

func TestProcessorRejectsMissingDependenciesAndRecord(t *testing.T) {
	if _, err := NewProcessor(nil); err == nil {
		t.Fatal("nil index was accepted")
	}
	processor, err := NewProcessor(&fakeDocumentIndex{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Apply(context.Background(), searchstate.Record{}); err == nil {
		t.Fatal("empty record was accepted")
	}
}

type fakeDocumentIndex struct {
	result   searchindex.ElasticsearchApplyResult
	document searchindex.LotDocument
	calls    int
}

func (index *fakeDocumentIndex) ApplyDocument(_ context.Context, document searchindex.LotDocument) (searchindex.ElasticsearchApplyResult, error) {
	index.calls++
	index.document = document
	return index.result, nil
}
