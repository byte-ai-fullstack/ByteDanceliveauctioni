package projectionrepair

import (
	"context"
	"errors"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

func TestServiceSyntheticDryRunIsStrictlyZeroWrite(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 2)
	path, digest := writeSyntheticBundle(t, document)
	store := freshSyntheticStore()
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}
	applier := &repairApplierStub{}
	service := syntheticService(t, now, store, source, applier)

	report, err := service.Synthetic(context.Background(), SyntheticRequest{BundlePath: path, ExpectedSHA256: digest})
	if err != nil {
		t.Fatalf("Synthetic: %v", err)
	}
	if report.Executed || report.PrefixRecords != 0 || report.SuffixRecords != 2 || report.CompletionOnly || report.BundleSHA256 != digest {
		t.Fatalf("report=%+v", report)
	}
	if store.beginSyntheticCalls != 0 || len(store.finishCalls) != 0 || len(applier.records) != 0 || source.boundCalls != 1 {
		t.Fatalf("begin=%d finish=%d apply=%d bounds=%d", store.beginSyntheticCalls, len(store.finishCalls), len(applier.records), source.boundCalls)
	}
}

func TestServiceSyntheticExecutesSuffixAuditsAndVerifies(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 2)
	path, digest := writeSyntheticBundle(t, document)
	store := freshSyntheticStore()
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}
	applier := updatingSyntheticApplier(t, store)
	service := syntheticService(t, now, store, source, applier)
	metadata := syntheticMetadata(document, digest)

	report, err := service.Synthetic(context.Background(), SyntheticRequest{
		BundlePath: path, ExpectedSHA256: digest, Execute: true, ExecutedBy: "engineer-b",
		Confirm: SyntheticConfirmation(metadata),
	})
	if err != nil {
		t.Fatalf("Synthetic: %v", err)
	}
	if report.AuditID == "" || report.AppliedRecords != 2 || !report.Verified || !report.ResumeSafe || !report.RestartRequired {
		t.Fatalf("report=%+v", report)
	}
	if store.beginSyntheticCalls != 1 || len(store.finishCalls) != 1 || !store.finishCalls[0].succeeded || source.boundCalls != 3 {
		t.Fatalf("begin=%d finish=%+v bounds=%d", store.beginSyntheticCalls, store.finishCalls, source.boundCalls)
	}
}

func TestServiceSyntheticResumesMatchingCommittedPrefix(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 2)
	path, digest := writeSyntheticBundle(t, document)
	prefix := syntheticInboxFixture(t, document.Records[0])
	store := &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 101, UpdatedAtMs: 100},
		inbox:  []InboxEntry{prefix}, inboxEvents: map[string]InboxEntry{prefix.EventID: prefix},
		states:  map[string]LotState{"lot-1": syntheticLotState(t, document.Records[0])},
		history: SyntheticAuditHistory{Started: 1}, interruptedAudits: 1,
	}
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}
	applier := updatingSyntheticApplier(t, store)
	service := syntheticService(t, now, store, source, applier)

	report, err := service.Synthetic(context.Background(), SyntheticRequest{
		BundlePath: path, ExpectedSHA256: digest, Execute: true, ExecutedBy: "engineer-b",
		Confirm: SyntheticConfirmation(syntheticMetadata(document, digest)),
	})
	if err != nil {
		t.Fatalf("Synthetic: %v", err)
	}
	if report.PrefixRecords != 1 || report.SuffixRecords != 1 || report.AppliedRecords != 1 || report.InterruptedAudits != 1 || !report.ResumeSafe {
		t.Fatalf("report=%+v", report)
	}
	if len(applier.records) != 1 || applier.records[0].Offset != 101 {
		t.Fatalf("applied=%+v", applier.records)
	}
}

func TestServiceSyntheticCompletionOnlyRequiresFullEvidence(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 2)
	path, digest := writeSyntheticBundle(t, document)
	first := syntheticInboxFixture(t, document.Records[0])
	second := syntheticInboxFixture(t, document.Records[1])
	store := &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 102, UpdatedAtMs: 100},
		inbox:  []InboxEntry{first, second}, inboxEvents: map[string]InboxEntry{first.EventID: first, second.EventID: second},
		states:  map[string]LotState{"lot-1": syntheticLotState(t, document.Records[1])},
		history: SyntheticAuditHistory{Failed: 1},
	}
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}
	applier := &repairApplierStub{}
	service := syntheticService(t, now, store, source, applier)

	report, err := service.Synthetic(context.Background(), SyntheticRequest{
		BundlePath: path, ExpectedSHA256: digest, Execute: true, ExecutedBy: "engineer-b",
		Confirm: SyntheticConfirmation(syntheticMetadata(document, digest)),
	})
	if err != nil {
		t.Fatalf("Synthetic: %v", err)
	}
	if !report.CompletionOnly || report.PrefixRecords != 2 || report.SuffixRecords != 0 || report.AppliedRecords != 0 || !report.ResumeSafe {
		t.Fatalf("report=%+v", report)
	}
	if len(applier.records) != 0 || store.beginSyntheticCalls != 1 || len(store.finishCalls) != 1 || !store.finishCalls[0].succeeded {
		t.Fatalf("apply=%d begin=%d finish=%+v", len(applier.records), store.beginSyntheticCalls, store.finishCalls)
	}
}

func TestServiceSyntheticRejectsUnsafeIdentityPrefixAndMovement(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 2)
	path, digest := writeSyntheticBundle(t, document)
	metadata := syntheticMetadata(document, digest)

	t.Run("same preparer and executor", func(t *testing.T) {
		store := freshSyntheticStore()
		applier := &repairApplierStub{}
		service := syntheticService(t, now, store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}, applier)
		_, err := service.Synthetic(context.Background(), SyntheticRequest{
			BundlePath: path, ExpectedSHA256: digest, Execute: true, ExecutedBy: document.PreparedBy,
			Confirm: SyntheticConfirmation(metadata),
		})
		if err == nil || store.beginSyntheticCalls != 0 || len(applier.records) != 0 {
			t.Fatalf("error=%v begin=%d apply=%d", err, store.beginSyntheticCalls, len(applier.records))
		}
	})

	t.Run("prefix without matching audit", func(t *testing.T) {
		prefix := syntheticInboxFixture(t, document.Records[0])
		store := &repairStoreStub{
			offset: PartitionOffset{Found: true, NextOffset: 101, UpdatedAtMs: 100},
			inbox:  []InboxEntry{prefix}, inboxEvents: map[string]InboxEntry{prefix.EventID: prefix},
			states: map[string]LotState{"lot-1": syntheticLotState(t, document.Records[0])},
		}
		service := syntheticService(t, now, store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}, &repairApplierStub{})
		_, err := service.Synthetic(context.Background(), SyntheticRequest{BundlePath: path, ExpectedSHA256: digest})
		if !errors.Is(err, ErrUnsafeSynthetic) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("suffix event identity conflict", func(t *testing.T) {
		store := freshSyntheticStore()
		conflict := syntheticInboxFixture(t, document.Records[0])
		conflict.Offset = 44
		store.inboxEvents = map[string]InboxEntry{conflict.EventID: conflict}
		service := syntheticService(t, now, store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 102, Latest: 200}}, &repairApplierStub{})
		_, err := service.Synthetic(context.Background(), SyntheticRequest{BundlePath: path, ExpectedSHA256: digest})
		if !errors.Is(err, ErrUnsafeSynthetic) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("Kafka earliest moves after apply", func(t *testing.T) {
		store := freshSyntheticStore()
		source := &repairSourceStub{boundsSequence: []projector.PartitionBounds{
			{Earliest: 102, Latest: 200}, {Earliest: 102, Latest: 200}, {Earliest: 103, Latest: 200},
		}}
		applier := updatingSyntheticApplier(t, store)
		service := syntheticService(t, now, store, source, applier)
		report, err := service.Synthetic(context.Background(), SyntheticRequest{
			BundlePath: path, ExpectedSHA256: digest, Execute: true, ExecutedBy: "engineer-b",
			Confirm: SyntheticConfirmation(metadata),
		})
		if !errors.Is(err, ErrVerificationFailed) || report.ResumeSafe || len(store.finishCalls) != 1 || store.finishCalls[0].succeeded {
			t.Fatalf("report=%+v error=%v finish=%+v", report, err, store.finishCalls)
		}
	})
}

func TestSyntheticRequestAndConfirmationValidation(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadata := SyntheticBundleMetadata{
		Partition: 3, FromOffset: 10, ToOffsetExclusive: 12, BundleSHA256: digest, PreparedBy: "engineer-a",
	}
	if err := validateSyntheticRequest(SyntheticRequest{}); err == nil {
		t.Fatal("missing bundle request was accepted")
	}
	if err := validateSyntheticRequest(SyntheticRequest{
		BundlePath: "/evidence/bundle.json", ExpectedSHA256: digest, ExecutedBy: "engineer-b",
	}); err == nil {
		t.Fatal("dry-run execution identity was accepted")
	}
	if err := validateSyntheticExecution(SyntheticRequest{ExecutedBy: "engineer-b", Confirm: "wrong"}, metadata); err == nil {
		t.Fatal("wrong confirmation was accepted")
	}
	if got := SyntheticConfirmation(metadata); got != "SYNTHETIC_PARTITION_3_FROM_10_TO_12_SHA256_"+digest {
		t.Fatalf("confirmation=%s", got)
	}
}

func freshSyntheticStore() *repairStoreStub {
	return &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 100, UpdatedAtMs: 100},
		states: map[string]LotState{"lot-1": {
			LotID: "lot-1", ProjectionStateFound: true, LastEventID: "previous-event",
			LastLotVersion: 6, CanonicalHash: "previous-hash", LotVersion: 6,
		}},
	}
}

func syntheticService(t *testing.T, now time.Time, store *repairStoreStub, source *repairSourceStub, applier *repairApplierStub) *Service {
	t.Helper()
	service, err := NewService(store, source, applier)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service
}

func updatingSyntheticApplier(t *testing.T, store *repairStoreStub) *repairApplierStub {
	t.Helper()
	return &repairApplierStub{apply: func(record projector.DecodedRecord) (projector.ApplyResult, error) {
		hash, err := eventcontract.CanonicalStateHash(record.Fact.GetStateAfter())
		if err != nil {
			return projector.ApplyResult{}, err
		}
		store.offset = PartitionOffset{Found: true, NextOffset: record.Offset + 1, UpdatedAtMs: store.offset.UpdatedAtMs + 1}
		store.states[record.Fact.GetLotId()] = LotState{
			LotID: record.Fact.GetLotId(), ProjectionStateFound: true, LastEventID: record.Fact.GetEventId(),
			LastLotVersion: record.Fact.GetLotVersion(), CanonicalHash: hash, LotVersion: record.Fact.GetLotVersion(),
		}
		return projector.ApplyResult{NextOffset: record.Offset + 1}, nil
	}}
}

func syntheticMetadata(document syntheticBundleDocument, digest string) SyntheticBundleMetadata {
	return SyntheticBundleMetadata{
		SchemaVersion: document.SchemaVersion, Topic: document.Topic, Partition: document.Partition,
		FromOffset: document.FromOffset, ToOffsetExclusive: document.ToOffsetExclusive,
		PreparedBy: document.PreparedBy, ChangeTicket: document.ChangeTicket, RepairReason: document.RepairReason,
		CreatedAtUnixMs: document.CreatedAtUnixMs, RecordCount: len(document.Records), BundleSHA256: digest,
	}
}

func syntheticInboxFixture(t *testing.T, record syntheticRecordDocument) InboxEntry {
	t.Helper()
	fact := decodeSyntheticFact(t, record)
	return InboxEntry{
		EventID: fact.GetEventId(), Offset: record.SourceOffset, LotID: fact.GetLotId(),
		LotVersion: fact.GetLotVersion(), PayloadHash: record.PayloadSHA256, AppliedAtMs: 100,
	}
}

func syntheticLotState(t *testing.T, record syntheticRecordDocument) LotState {
	t.Helper()
	fact := decodeSyntheticFact(t, record)
	hash, err := eventcontract.CanonicalStateHash(fact.GetStateAfter())
	if err != nil {
		t.Fatal(err)
	}
	return LotState{
		LotID: fact.GetLotId(), ProjectionStateFound: true, LastEventID: fact.GetEventId(),
		LastLotVersion: fact.GetLotVersion(), CanonicalHash: hash, LotVersion: fact.GetLotVersion(),
	}
}
