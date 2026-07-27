package searchreconcile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/searchrebuild"
)

func TestReconcilerRepublishesCanonicalEventForStaleSink(t *testing.T) {
	record := reconcileRecordFixture()
	expected := identityFromRecord(record)
	publisher := &fakePublisher{}
	findings := &fakeFindingStore{}
	reconciler, err := New(
		&fakeIdentityReader{identity: Identity{}},
		&fakeIdentityReader{identity: expected},
		publisher, findings,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background(), record)
	if err != nil || !result.RepairPublished || publisher.calls != 1 || len(findings.values) != 0 ||
		result.SinkResults[SinkElasticsearch] != ResultMissing || result.SinkResults[SinkPGVector] != ResultHealthy {
		t.Fatalf("result=%+v error=%v publisher=%d findings=%+v", result, err, publisher.calls, findings.values)
	}
	if string(publisher.payload) != string(record.Payload) {
		t.Fatal("repair publisher did not receive the canonical payload")
	}
}

func TestReconcilerRecordsForkWithoutOverwriting(t *testing.T) {
	record := reconcileRecordFixture()
	expected := identityFromRecord(record)
	conflict := expected
	conflict.ContentHash = strings.Repeat("b", 64)
	publisher := &fakePublisher{}
	findings := &fakeFindingStore{}
	reconciler, _ := New(&fakeIdentityReader{identity: conflict}, &fakeIdentityReader{identity: expected}, publisher, findings)
	result, err := reconciler.Reconcile(context.Background(), record)
	if err != nil || result.RepairPublished || publisher.calls != 0 || len(findings.values) != 1 ||
		findings.values[0].Result != ResultConflict || findings.values[0].Sink != SinkElasticsearch {
		t.Fatalf("result=%+v error=%v publisher=%d findings=%+v", result, err, publisher.calls, findings.values)
	}
}

func TestReconcilerKeepsSinksIndependent(t *testing.T) {
	record := reconcileRecordFixture()
	publisher := &fakePublisher{}
	reconciler, _ := New(
		&fakeIdentityReader{err: errors.New("es unavailable")},
		&fakeIdentityReader{identity: Identity{}},
		publisher, &fakeFindingStore{},
	)
	result, err := reconciler.Reconcile(context.Background(), record)
	if err != nil || !result.RepairPublished || publisher.calls != 1 ||
		result.SinkResults[SinkElasticsearch] != ResultError || result.SinkResults[SinkPGVector] != ResultMissing {
		t.Fatalf("result=%+v error=%v publisher=%d", result, err, publisher.calls)
	}
}

func TestReconcilerRepairsMissingVisibleEmbedding(t *testing.T) {
	record := reconcileRecordFixture()
	record.Document.PublicVisible = true
	record.Document.SearchText = "Jade vase"
	expected := identityFromRecord(record)
	incomplete := expected
	incomplete.Complete = false
	publisher := &fakePublisher{}
	reconciler, _ := New(
		&fakeIdentityReader{identity: expected}, &fakeIdentityReader{identity: incomplete},
		publisher, &fakeFindingStore{},
	)
	result, err := reconciler.Reconcile(context.Background(), record)
	if err != nil || !result.RepairPublished || result.SinkResults[SinkPGVector] != ResultIncomplete {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestReconcilerReturnsDurabilityFailures(t *testing.T) {
	record := reconcileRecordFixture()
	expected := identityFromRecord(record)
	ahead := expected
	ahead.LotVersion++
	reconciler, _ := New(
		&fakeIdentityReader{identity: ahead}, &fakeIdentityReader{identity: expected},
		&fakePublisher{}, &fakeFindingStore{err: errors.New("mysql unavailable")},
	)
	if _, err := reconciler.Reconcile(context.Background(), record); err == nil {
		t.Fatal("finding persistence failure was ignored")
	}
	reconciler, _ = New(
		&fakeIdentityReader{}, &fakeIdentityReader{identity: expected},
		&fakePublisher{err: errors.New("kafka unavailable")}, &fakeFindingStore{},
	)
	if _, err := reconciler.Reconcile(context.Background(), record); err == nil {
		t.Fatal("repair publication failure was ignored")
	}
}

type fakeIdentityReader struct {
	identity Identity
	err      error
}

func (reader *fakeIdentityReader) ReadIdentity(context.Context, string) (Identity, error) {
	return reader.identity, reader.err
}

type fakePublisher struct {
	calls   int
	payload []byte
	err     error
}

func (publisher *fakePublisher) Publish(_ context.Context, payload []byte) error {
	publisher.calls++
	publisher.payload = append([]byte(nil), payload...)
	return publisher.err
}

type fakeFindingStore struct {
	values []Finding
	err    error
}

func (store *fakeFindingStore) Record(_ context.Context, finding Finding) error {
	store.values = append(store.values, finding)
	return store.err
}

func reconcileRecordFixture() searchrebuild.SnapshotRecord {
	return searchrebuild.SnapshotRecord{
		Document: searchindex.LotDocument{
			LotID: "lot-1", LotVersion: 7, LastEventID: "01J00000000000000000000000", ContentHash: strings.Repeat("a", 64),
		},
		Payload: []byte("canonical-event"),
	}
}

func identityFromRecord(record searchrebuild.SnapshotRecord) Identity {
	return Identity{
		Found: true, Complete: true, LotVersion: record.Document.LotVersion,
		LastEventID: record.Document.LastEventID, ContentHash: record.Document.ContentHash,
	}
}
