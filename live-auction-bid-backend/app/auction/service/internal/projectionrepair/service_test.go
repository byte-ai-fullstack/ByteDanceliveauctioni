package projectionrepair

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

func TestServiceDiagnoseCorrelatesKafkaInboxAndDatabaseState(t *testing.T) {
	record := repairRecordFixture(t, 2, 10, "lot-1", 6, 7)
	decoded, err := projector.DecodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	store := &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 10, UpdatedAtMs: 100},
		inbox: []InboxEntry{{
			EventID: decoded.Fact.GetEventId(), Offset: 10, LotID: "lot-1", LotVersion: 7,
			PayloadHash: decoded.PayloadHash, AppliedAtMs: 90,
		}},
		states: map[string]LotState{"lot-1": {
			LotID: "lot-1", ProjectionStateFound: true, LastEventID: "previous", LastLotVersion: 6,
			CanonicalHash: "hash-6", LotVersion: 6,
		}},
	}
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 5, Latest: 20}, records: []*kgo.Record{record}}
	service, err := NewService(store, source, &repairApplierStub{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Diagnose(context.Background(), DiagnoseRequest{Partition: 2, Before: 0, After: 0})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.RetentionCliff || !report.ReplayPossible || report.WindowStart != 10 || report.WindowEnd != 11 {
		t.Fatalf("report=%+v", report)
	}
	if len(report.Records) != 1 || report.Records[0].InboxStatus != "matched" || report.Records[0].LotVersion != 7 {
		t.Fatalf("record reports=%+v", report.Records)
	}
	if len(report.ProjectionStates) != 1 || report.ProjectionStates[0].LastLotVersion != 6 {
		t.Fatalf("states=%+v", report.ProjectionStates)
	}
	if store.inboxRange != [2]int64{10, 11} || source.readRange != [2]int64{10, 11} {
		t.Fatalf("inbox range=%v source range=%v", store.inboxRange, source.readRange)
	}
}

func TestServiceDiagnoseReportsRetentionCliffAndInvalidRecord(t *testing.T) {
	store := &repairStoreStub{offset: PartitionOffset{Found: true, NextOffset: 4, UpdatedAtMs: 10}}
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 10, Latest: 20}}
	service, _ := NewService(store, source, &repairApplierStub{})
	report, err := service.Diagnose(context.Background(), DiagnoseRequest{Partition: 1, Before: 2, After: 2})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if !report.RetentionCliff || report.ReplayPossible || len(report.Records) != 0 {
		t.Fatalf("report=%+v", report)
	}
	if _, err := service.Diagnose(context.Background(), DiagnoseRequest{Partition: -1}); err == nil {
		t.Fatal("invalid diagnostic request was accepted")
	}

	invalid := repairRecordFixture(t, 1, 12, "lot-1", 1, 2)
	invalid.Value = []byte{0xff}
	invalidStore := &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 12, UpdatedAtMs: 10},
		inbox:  []InboxEntry{{Offset: 12, EventID: "unknown"}},
	}
	invalidService, _ := NewService(invalidStore, &repairSourceStub{
		bound: projector.PartitionBounds{Earliest: 10, Latest: 20}, records: []*kgo.Record{invalid},
	}, &repairApplierStub{})
	invalidReport, err := invalidService.Diagnose(context.Background(), DiagnoseRequest{Partition: 1})
	if err != nil || len(invalidReport.Records) != 1 || invalidReport.Records[0].DecodeError == "" || invalidReport.Records[0].InboxStatus != "unverifiable" {
		t.Fatalf("invalid report=%+v error=%v", invalidReport, err)
	}
}

func TestServiceReplayDryRunShowsAffectedVersionsWithoutWrites(t *testing.T) {
	records := []*kgo.Record{
		repairRecordFixture(t, 3, 10, "lot-1", 6, 7),
		repairRecordFixture(t, 3, 11, "lot-1", 7, 8),
	}
	store := &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 10, UpdatedAtMs: 100},
		states: map[string]LotState{"lot-1": {
			LotID: "lot-1", ProjectionStateFound: true, LastLotVersion: 6, LotVersion: 6,
		}},
	}
	source := &repairSourceStub{bound: projector.PartitionBounds{Earliest: 0, Latest: 20}, records: records}
	applier := &repairApplierStub{}
	service, _ := NewService(store, source, applier)
	report, err := service.Replay(context.Background(), ReplayRequest{Partition: 3, ExpectedNextOffset: 10, ThroughOffset: 11})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if report.Executed || report.AppliedRecords != 0 || report.ResumeSafe || len(applier.records) != 0 {
		t.Fatalf("report=%+v applied=%d", report, len(applier.records))
	}
	if len(report.AffectedLots) != 1 || report.AffectedLots[0].InitialVersion != 6 || report.AffectedLots[0].ExpectedVersion != 8 {
		t.Fatalf("affected=%+v", report.AffectedLots)
	}
}

func TestServiceReplayExecutesAuditsAndVerifies(t *testing.T) {
	records := []*kgo.Record{
		repairRecordFixture(t, 3, 10, "lot-1", 6, 7),
		repairRecordFixture(t, 3, 11, "lot-1", 7, 8),
	}
	store := &repairStoreStub{
		offset: PartitionOffset{Found: true, NextOffset: 10, UpdatedAtMs: 100},
		states: map[string]LotState{"lot-1": {
			LotID: "lot-1", ProjectionStateFound: true, LastLotVersion: 6, LotVersion: 6,
		}},
	}
	applier := &repairApplierStub{apply: func(record projector.DecodedRecord) (projector.ApplyResult, error) {
		hash, err := eventcontract.CanonicalStateHash(record.Fact.GetStateAfter())
		if err != nil {
			return projector.ApplyResult{}, err
		}
		store.offset.NextOffset = record.Offset + 1
		store.states[record.Fact.GetLotId()] = LotState{
			LotID: record.Fact.GetLotId(), ProjectionStateFound: true, LastEventID: record.Fact.GetEventId(),
			LastLotVersion: record.Fact.GetLotVersion(), CanonicalHash: hash, LotVersion: record.Fact.GetLotVersion(),
		}
		return projector.ApplyResult{NextOffset: record.Offset + 1}, nil
	}}
	service, _ := NewService(store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 0, Latest: 20}, records: records}, applier)
	report, err := service.Replay(context.Background(), ReplayRequest{
		Partition: 3, ExpectedNextOffset: 10, ThroughOffset: 11, Execute: true,
		Operator: "operator-1", Reason: "repair documented gap",
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if report.AuditID == "" || report.AppliedRecords != 2 || !report.Verified || !report.ResumeSafe || !report.RestartRequired {
		t.Fatalf("report=%+v", report)
	}
	if store.beginCalls != 1 || len(store.finishCalls) != 1 || !store.finishCalls[0].succeeded {
		t.Fatalf("audit begin=%d finish=%+v", store.beginCalls, store.finishCalls)
	}
	if len(store.finishCalls[0].lotIDs) != 1 || store.finishCalls[0].lotIDs[0] != "lot-1" {
		t.Fatalf("resolved lots=%v", store.finishCalls[0].lotIDs)
	}
}

func TestServiceReplayRejectsUnsafeMovementGapAndBadVerification(t *testing.T) {
	record := repairRecordFixture(t, 0, 10, "lot-1", 6, 7)
	baseStore := func() *repairStoreStub {
		return &repairStoreStub{
			offset: PartitionOffset{Found: true, NextOffset: 10, UpdatedAtMs: 1},
			states: map[string]LotState{"lot-1": {
				LotID: "lot-1", ProjectionStateFound: true, LastLotVersion: 6, LotVersion: 6,
			}},
		}
	}
	t.Run("expected offset", func(t *testing.T) {
		store := baseStore()
		service, _ := NewService(store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 0, Latest: 20}}, &repairApplierStub{})
		_, err := service.Replay(context.Background(), ReplayRequest{Partition: 0, ExpectedNextOffset: 9, ThroughOffset: 10})
		if !errors.Is(err, ErrUnsafeReplay) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("version gap", func(t *testing.T) {
		store := baseStore()
		bad := repairRecordFixture(t, 0, 10, "lot-1", 7, 8)
		service, _ := NewService(store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 0, Latest: 20}, records: []*kgo.Record{bad}}, &repairApplierStub{})
		_, err := service.Replay(context.Background(), ReplayRequest{Partition: 0, ExpectedNextOffset: 10, ThroughOffset: 10})
		if !errors.Is(err, ErrUnsafeReplay) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("incomplete Kafka range", func(t *testing.T) {
		store := baseStore()
		service, _ := NewService(store, &repairSourceStub{
			bound: projector.PartitionBounds{Earliest: 0, Latest: 20}, records: nil,
		}, &repairApplierStub{})
		_, err := service.Replay(context.Background(), ReplayRequest{Partition: 0, ExpectedNextOffset: 10, ThroughOffset: 10})
		if !errors.Is(err, ErrUnsafeReplay) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("concurrent movement", func(t *testing.T) {
		store := baseStore()
		applier := &repairApplierStub{apply: func(projector.DecodedRecord) (projector.ApplyResult, error) {
			return projector.ApplyResult{NextOffset: 12, AlreadyAdvanced: true}, nil
		}}
		service, _ := NewService(store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 0, Latest: 20}, records: []*kgo.Record{record}}, applier)
		_, err := service.Replay(context.Background(), ReplayRequest{
			Partition: 0, ExpectedNextOffset: 10, ThroughOffset: 10, Execute: true, Operator: "op", Reason: "gap",
		})
		if !errors.Is(err, ErrUnsafeReplay) || len(store.finishCalls) != 1 || store.finishCalls[0].succeeded {
			t.Fatalf("error=%v finish=%+v", err, store.finishCalls)
		}
	})
	t.Run("verification", func(t *testing.T) {
		store := baseStore()
		applier := &repairApplierStub{apply: func(record projector.DecodedRecord) (projector.ApplyResult, error) {
			store.offset.NextOffset = record.Offset + 1
			return projector.ApplyResult{NextOffset: record.Offset + 1}, nil
		}}
		service, _ := NewService(store, &repairSourceStub{bound: projector.PartitionBounds{Earliest: 0, Latest: 20}, records: []*kgo.Record{record}}, applier)
		_, err := service.Replay(context.Background(), ReplayRequest{
			Partition: 0, ExpectedNextOffset: 10, ThroughOffset: 10, Execute: true, Operator: "op", Reason: "gap",
		})
		if !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestReplayRequestValidationAndServiceConstruction(t *testing.T) {
	if _, err := NewService(nil, &repairSourceStub{}, &repairApplierStub{}); err == nil {
		t.Fatal("nil store was accepted")
	}
	for _, request := range []ReplayRequest{
		{Partition: -1},
		{Partition: 0, ExpectedNextOffset: 2, ThroughOffset: 1},
		{Partition: 0, ExpectedNextOffset: 0, ThroughOffset: MaxReplayRecords},
		{Partition: 0, ExpectedNextOffset: 0, ThroughOffset: 0, Execute: true},
	} {
		if err := validateReplayRequest(request); err == nil {
			t.Fatalf("request=%+v was accepted", request)
		}
	}
	if err := validateReplayRequest(ReplayRequest{
		Partition: 0, ExpectedNextOffset: 0, ThroughOffset: 0, Execute: true, Operator: "op", Reason: "reason",
	}); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if first, err := newRepairID(); err != nil || first == "" {
		t.Fatalf("repair id=%q error=%v", first, err)
	}
}

type repairStoreStub struct {
	offset              PartitionOffset
	inbox               []InboxEntry
	inboxEvents         map[string]InboxEntry
	states              map[string]LotState
	history             SyntheticAuditHistory
	inboxRange          [2]int64
	beginCalls          int
	beginSyntheticCalls int
	interruptedAudits   int
	finishCalls         []repairFinishCall
	err                 error
}

type repairFinishCall struct {
	succeeded bool
	lotIDs    []string
}

func (stub *repairStoreStub) ReadPartitionOffset(context.Context, string, int32) (PartitionOffset, error) {
	return stub.offset, stub.err
}

func (stub *repairStoreStub) ReadInboxRange(_ context.Context, _ string, _ int32, start, end int64) ([]InboxEntry, error) {
	stub.inboxRange = [2]int64{start, end}
	return append([]InboxEntry(nil), stub.inbox...), stub.err
}

func (stub *repairStoreStub) ReadInboxByEventIDs(_ context.Context, eventIDs []string) (map[string]InboxEntry, error) {
	if stub.err != nil {
		return nil, stub.err
	}
	result := make(map[string]InboxEntry)
	for _, eventID := range eventIDs {
		if entry, exists := stub.inboxEvents[eventID]; exists {
			result[eventID] = entry
		}
	}
	return result, nil
}

func (stub *repairStoreStub) ReadLotStates(_ context.Context, lotIDs []string) (map[string]LotState, error) {
	if stub.err != nil {
		return nil, stub.err
	}
	result := make(map[string]LotState)
	for _, lotID := range lotIDs {
		if value, exists := stub.states[lotID]; exists {
			result[lotID] = value
		}
	}
	return result, nil
}

func (stub *repairStoreStub) BeginReplayAudit(context.Context, string, ReplayRequest, int64, any) error {
	stub.beginCalls++
	return stub.err
}

func (stub *repairStoreStub) FinishReplayAudit(_ context.Context, _ string, succeeded bool, _ any, lotIDs []string) error {
	stub.finishCalls = append(stub.finishCalls, repairFinishCall{succeeded: succeeded, lotIDs: append([]string(nil), lotIDs...)})
	return stub.err
}

func (stub *repairStoreStub) ReadSyntheticAuditHistory(context.Context, SyntheticBundleMetadata) (SyntheticAuditHistory, error) {
	return stub.history, stub.err
}

func (stub *repairStoreStub) BeginSyntheticAudit(context.Context, string, SyntheticRequest, SyntheticBundleMetadata, any) (int, error) {
	stub.beginSyntheticCalls++
	return stub.interruptedAudits, stub.err
}

func (stub *repairStoreStub) FinishSyntheticAudit(_ context.Context, _ string, succeeded bool, _ any, lotIDs []string) error {
	stub.finishCalls = append(stub.finishCalls, repairFinishCall{succeeded: succeeded, lotIDs: append([]string(nil), lotIDs...)})
	return stub.err
}

type repairSourceStub struct {
	bound          projector.PartitionBounds
	boundsSequence []projector.PartitionBounds
	boundCalls     int
	records        []*kgo.Record
	readRange      [2]int64
	err            error
}

func (stub *repairSourceStub) Bounds(context.Context, int32) (projector.PartitionBounds, error) {
	stub.boundCalls++
	if len(stub.boundsSequence) > 0 {
		value := stub.boundsSequence[0]
		stub.boundsSequence = stub.boundsSequence[1:]
		return value, stub.err
	}
	return stub.bound, stub.err
}

func (stub *repairSourceStub) ReadRange(_ context.Context, _ int32, start, end int64) ([]*kgo.Record, error) {
	stub.readRange = [2]int64{start, end}
	return append([]*kgo.Record(nil), stub.records...), stub.err
}

type repairApplierStub struct {
	records []projector.DecodedRecord
	apply   func(projector.DecodedRecord) (projector.ApplyResult, error)
}

func (stub *repairApplierStub) Apply(_ context.Context, record projector.DecodedRecord) (projector.ApplyResult, error) {
	stub.records = append(stub.records, record)
	if stub.apply != nil {
		return stub.apply(record)
	}
	return projector.ApplyResult{NextOffset: record.Offset + 1}, nil
}

func repairRecordFixture(t *testing.T, partition int32, offset int64, lotID string, previous, version int64) *kgo.Record {
	t.Helper()
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	durationMs := int64(60_000)
	windowMs := int64(10_000)
	extendMs := int64(30_000)
	fact := &v1.RuntimeFactV1{
		EventId: eventID, TraceId: fmt.Sprintf("trace-%d", version), LotId: lotID, RoomId: "room-1",
		PrevLotVersion: previous, LotVersion: version, OccurredAtUnixMs: 1_700_000_000_000 + version,
		ConfigVersion: 3, Command: v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID,
		StateAfter: &v1.LotRuntimeStateV1{
			LotId: lotID, RoomId: "room-1", Status: v1.LotStatus_LOT_STATUS_LIVE, Currency: "CNY",
			StartPriceFen: 10_000, MinIncrementFen: 100, CurrentPriceFen: 10_000 + version*100,
			LeadingUserId: "user-1", LeadingNickname: "User 1", StartedAtUnixMs: 1_699_999_940_000,
			EndsAtUnixMs: 1_700_000_060_000, BidCount: version, ParticipantCount: 1,
			DurationMs: &durationMs, AntiSnipeWindowMs: &windowMs, AntiSnipeExtendMs: &extendMs,
		},
		AcceptedBid: &v1.AcceptedBidV1{
			BidId: fmt.Sprintf("bid-%d", version), UserId: "user-1", Nickname: "User 1",
			AmountFen: 10_000 + version*100, AcceptedAtUnixMs: 1_700_000_000_000 + version,
		},
		IdempotencyKey: fmt.Sprintf("idem-%d", version), SchemaVersion: eventcontract.RuntimeSchemaVersionV1,
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: eventcontract.RuntimeProjectionTopicV1, Partition: partition, Offset: offset,
		Key: []byte(lotID), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.RuntimeFactContentType)},
			{Key: eventcontract.RuntimeHeaderEventID, Value: []byte(eventID)},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte(fact.GetTraceId())},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
			{Key: eventcontract.RuntimeHeaderLotVersion, Value: []byte(strconv.FormatInt(version, 10))},
			{Key: eventcontract.RuntimeHeaderOwnerEpoch, Value: []byte("1")},
			{Key: eventcontract.RuntimeHeaderOutboxShard, Value: []byte("0")},
		},
	}
}
