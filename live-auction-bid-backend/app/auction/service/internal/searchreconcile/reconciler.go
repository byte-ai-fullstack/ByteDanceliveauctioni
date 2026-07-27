package searchreconcile

import (
	"context"
	"errors"
	"fmt"

	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/searchrebuild"
)

const (
	SinkElasticsearch = "elasticsearch"
	SinkPGVector      = "pgvector"

	ResultHealthy    = "healthy"
	ResultMissing    = "missing"
	ResultIncomplete = "incomplete"
	ResultStale      = "stale"
	ResultConflict   = "conflict"
	ResultAhead      = "ahead"
	ResultError      = "error"
)

type Identity struct {
	Found       bool
	Complete    bool
	LotVersion  int64
	LastEventID string
	ContentHash string
}

type IdentityReader interface {
	ReadIdentity(ctx context.Context, lotID string) (Identity, error)
}

type RepairPublisher interface {
	Publish(ctx context.Context, payload []byte) error
}

type FindingStore interface {
	Record(ctx context.Context, finding Finding) error
}

type Finding struct {
	Sink     string
	Result   string
	Expected Identity
	Actual   Identity
	LotID    string
}

type Result struct {
	RepairPublished bool
	SinkResults     map[string]string
}

type Reconciler struct {
	elasticsearch IdentityReader
	pgvector      IdentityReader
	publisher     RepairPublisher
	findings      FindingStore
}

func New(elasticsearch, pgvector IdentityReader, publisher RepairPublisher, findings FindingStore) (*Reconciler, error) {
	if elasticsearch == nil || pgvector == nil || publisher == nil || findings == nil {
		return nil, errors.New("search reconciler requires both readers, repair publisher, and finding store")
	}
	return &Reconciler{elasticsearch: elasticsearch, pgvector: pgvector, publisher: publisher, findings: findings}, nil
}

func (reconciler *Reconciler) Reconcile(ctx context.Context, record searchrebuild.SnapshotRecord) (Result, error) {
	if reconciler == nil || reconciler.elasticsearch == nil || reconciler.pgvector == nil || reconciler.publisher == nil || reconciler.findings == nil ||
		record.Document.LotID == "" || record.Document.LotVersion <= 0 || len(record.Payload) == 0 {
		return Result{}, errors.New("search reconciliation record or dependencies are invalid")
	}
	expected := Identity{
		Found: true, Complete: true, LotVersion: record.Document.LotVersion,
		LastEventID: record.Document.LastEventID, ContentHash: record.Document.ContentHash,
	}
	result := Result{SinkResults: make(map[string]string, 2)}
	repairNeeded := false
	readers := []struct {
		name   string
		reader IdentityReader
	}{
		{name: SinkElasticsearch, reader: reconciler.elasticsearch},
		{name: SinkPGVector, reader: reconciler.pgvector},
	}
	for _, sink := range readers {
		actual, err := sink.reader.ReadIdentity(ctx, record.Document.LotID)
		if err != nil {
			result.SinkResults[sink.name] = ResultError
			observability.RecordSearchReconcile(sink.name, ResultError)
			continue
		}
		requireComplete := sink.name == SinkPGVector && record.Document.PublicVisible && record.Document.SearchText != ""
		classification := classify(expected, actual, requireComplete)
		result.SinkResults[sink.name] = classification
		observability.RecordSearchReconcile(sink.name, classification)
		switch classification {
		case ResultMissing, ResultIncomplete, ResultStale:
			repairNeeded = true
		case ResultConflict, ResultAhead:
			if err := reconciler.findings.Record(ctx, Finding{
				Sink: sink.name, Result: classification, Expected: expected, Actual: actual, LotID: record.Document.LotID,
			}); err != nil {
				return result, fmt.Errorf("record %s search reconciliation finding: %w", sink.name, err)
			}
		}
	}
	if !repairNeeded {
		return result, nil
	}
	if err := reconciler.publisher.Publish(ctx, record.Payload); err != nil {
		observability.RecordSearchRepair("failed")
		return result, fmt.Errorf("publish canonical search repair: %w", err)
	}
	observability.RecordSearchRepair("published")
	result.RepairPublished = true
	return result, nil
}

func classify(expected, actual Identity, requireComplete bool) string {
	if !actual.Found {
		return ResultMissing
	}
	if actual.LotVersion < expected.LotVersion {
		return ResultStale
	}
	if actual.LotVersion > expected.LotVersion {
		return ResultAhead
	}
	if actual.LastEventID != expected.LastEventID || actual.ContentHash != expected.ContentHash {
		return ResultConflict
	}
	if requireComplete && !actual.Complete {
		return ResultIncomplete
	}
	return ResultHealthy
}

type ElasticsearchReader struct {
	index *searchindex.ElasticsearchIndex
}

func NewElasticsearchReader(index *searchindex.ElasticsearchIndex) (*ElasticsearchReader, error) {
	if index == nil {
		return nil, errors.New("search reconciler Elasticsearch index is required")
	}
	return &ElasticsearchReader{index: index}, nil
}

func (reader *ElasticsearchReader) ReadIdentity(ctx context.Context, lotID string) (Identity, error) {
	if reader == nil || reader.index == nil {
		return Identity{}, errors.New("search reconciler Elasticsearch reader is not initialized")
	}
	state, err := reader.index.CurrentDocumentIdentity(ctx, lotID)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Found: state.Found, Complete: state.Found, LotVersion: state.LotVersion, LastEventID: state.LastEventID, ContentHash: state.ContentHash}, nil
}

type PGVectorReader struct{ index *searchindex.PGVectorIndex }

func NewPGVectorReader(index *searchindex.PGVectorIndex) (*PGVectorReader, error) {
	if index == nil {
		return nil, errors.New("search reconciler pgvector index is required")
	}
	return &PGVectorReader{index: index}, nil
}

func (reader *PGVectorReader) ReadIdentity(ctx context.Context, lotID string) (Identity, error) {
	if reader == nil || reader.index == nil {
		return Identity{}, errors.New("search reconciler pgvector reader is not initialized")
	}
	state, err := reader.index.DocumentState(ctx, lotID)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Found: state.Found, Complete: state.HasEmbedding, LotVersion: state.LotVersion, LastEventID: state.LastEventID, ContentHash: state.ContentHash}, nil
}
