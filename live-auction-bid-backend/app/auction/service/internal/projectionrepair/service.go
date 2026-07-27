package projectionrepair

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

type stateStore interface {
	ReadPartitionOffset(context.Context, string, int32) (PartitionOffset, error)
	ReadInboxRange(context.Context, string, int32, int64, int64) ([]InboxEntry, error)
	ReadInboxByEventIDs(context.Context, []string) (map[string]InboxEntry, error)
	ReadLotStates(context.Context, []string) (map[string]LotState, error)
	BeginReplayAudit(context.Context, string, ReplayRequest, int64, any) error
	FinishReplayAudit(context.Context, string, bool, any, []string) error
	ReadSyntheticAuditHistory(context.Context, SyntheticBundleMetadata) (SyntheticAuditHistory, error)
	BeginSyntheticAudit(context.Context, string, SyntheticRequest, SyntheticBundleMetadata, any) (int, error)
	FinishSyntheticAudit(context.Context, string, bool, any, []string) error
}

type runtimeSource interface {
	Bounds(context.Context, int32) (projector.PartitionBounds, error)
	ReadRange(context.Context, int32, int64, int64) ([]*kgo.Record, error)
}

type runtimeApplier interface {
	Apply(context.Context, projector.DecodedRecord) (projector.ApplyResult, error)
}

type Service struct {
	store   stateStore
	source  runtimeSource
	applier runtimeApplier
	now     func() time.Time
}

func NewService(store stateStore, source runtimeSource, applier runtimeApplier) (*Service, error) {
	if store == nil || source == nil || applier == nil {
		return nil, errors.New("projection repair store, Kafka source, and applier are required")
	}
	return &Service{store: store, source: source, applier: applier, now: time.Now}, nil
}

func (service *Service) Diagnose(ctx context.Context, request DiagnoseRequest) (DiagnoseReport, error) {
	if service == nil || service.store == nil || service.source == nil {
		return DiagnoseReport{}, errors.New("projection repair service is not initialized")
	}
	if request.Partition < 0 || request.Before < 0 || request.After < 0 || request.Before+request.After+1 > MaxReplayRecords {
		return DiagnoseReport{}, errors.New("projection repair diagnostic window is invalid")
	}
	bound, err := service.source.Bounds(ctx, request.Partition)
	if err != nil {
		return DiagnoseReport{}, err
	}
	offset, err := service.store.ReadPartitionOffset(ctx, eventcontract.RuntimeProjectionTopicV1, request.Partition)
	if err != nil {
		return DiagnoseReport{}, err
	}
	center := bound.Earliest
	if offset.Found {
		center = offset.NextOffset
	}
	retentionCliff := offset.Found && (offset.NextOffset < bound.Earliest || offset.NextOffset > bound.Latest)
	kafkaCenter := center
	if kafkaCenter < bound.Earliest {
		kafkaCenter = bound.Earliest
	}
	if kafkaCenter > bound.Latest {
		kafkaCenter = bound.Latest
	}
	windowStart := kafkaCenter - request.Before
	if windowStart < bound.Earliest {
		windowStart = bound.Earliest
	}
	windowEnd := kafkaCenter + request.After + 1
	if windowEnd > bound.Latest {
		windowEnd = bound.Latest
	}
	if windowEnd < windowStart {
		windowEnd = windowStart
	}
	records, err := service.source.ReadRange(ctx, request.Partition, windowStart, windowEnd)
	if err != nil {
		return DiagnoseReport{}, err
	}
	inboxStart := center - request.Before
	if inboxStart < 0 {
		inboxStart = 0
	}
	inboxEnd := center + request.After + 1
	if inboxEnd < inboxStart {
		inboxEnd = inboxStart
	}
	inbox, err := service.store.ReadInboxRange(ctx, eventcontract.RuntimeProjectionTopicV1, request.Partition, inboxStart, inboxEnd)
	if err != nil {
		return DiagnoseReport{}, err
	}
	reports, lotIDs := describeRecords(records, inbox)
	states, err := service.store.ReadLotStates(ctx, lotIDs)
	if err != nil {
		return DiagnoseReport{}, err
	}
	return DiagnoseReport{
		Topic:            eventcontract.RuntimeProjectionTopicV1,
		Partition:        request.Partition,
		DatabaseOffset:   offset,
		KafkaBounds:      bound,
		RetentionCliff:   retentionCliff,
		ReplayPossible:   offset.Found && !retentionCliff && offset.NextOffset < bound.Latest,
		WindowStart:      windowStart,
		WindowEnd:        windowEnd,
		Records:          reports,
		Inbox:            inbox,
		ProjectionStates: sortedLotStates(states),
	}, nil
}

func (service *Service) Replay(ctx context.Context, request ReplayRequest) (report ReplayReport, returnErr error) {
	if service == nil || service.store == nil || service.source == nil || service.applier == nil {
		return ReplayReport{}, errors.New("projection repair service is not initialized")
	}
	request.Operator = strings.TrimSpace(request.Operator)
	request.Reason = strings.TrimSpace(request.Reason)
	if err := validateReplayRequest(request); err != nil {
		return ReplayReport{}, err
	}
	toOffsetExclusive := request.ThroughOffset + 1
	report = ReplayReport{
		Topic: eventcontract.RuntimeProjectionTopicV1, Partition: request.Partition,
		FromOffset: request.ExpectedNextOffset, ToOffsetExclusive: toOffsetExclusive,
		Executed: request.Execute,
	}
	bound, err := service.source.Bounds(ctx, request.Partition)
	if err != nil {
		return report, err
	}
	offset, err := service.store.ReadPartitionOffset(ctx, eventcontract.RuntimeProjectionTopicV1, request.Partition)
	if err != nil {
		return report, err
	}
	if !offset.Found || offset.NextOffset != request.ExpectedNextOffset {
		return report, fmt.Errorf("%w: DB next_offset found=%t got=%d expected=%d", ErrUnsafeReplay, offset.Found, offset.NextOffset, request.ExpectedNextOffset)
	}
	if request.ExpectedNextOffset < bound.Earliest || toOffsetExclusive > bound.Latest {
		return report, fmt.Errorf("%w: Kafka bounds earliest=%d latest=%d requested=[%d,%d)", ErrUnsafeReplay, bound.Earliest, bound.Latest, request.ExpectedNextOffset, toOffsetExclusive)
	}
	records, err := service.source.ReadRange(ctx, request.Partition, request.ExpectedNextOffset, toOffsetExclusive)
	if err != nil {
		return report, err
	}
	if int64(len(records)) != toOffsetExclusive-request.ExpectedNextOffset {
		return report, fmt.Errorf("%w: Kafka returned %d records for range [%d,%d)", ErrUnsafeReplay, len(records), request.ExpectedNextOffset, toOffsetExclusive)
	}
	for index, record := range records {
		wantOffset := request.ExpectedNextOffset + int64(index)
		if record == nil || record.Offset != wantOffset {
			return report, fmt.Errorf("%w: Kafka record index=%d offset mismatch want=%d", ErrUnsafeReplay, index, wantOffset)
		}
	}
	decoded, recordReports, lotIDs, err := decodeReplayRecords(records)
	if err != nil {
		report.Records = recordReports
		return report, err
	}
	report.Records = recordReports
	states, err := service.store.ReadLotStates(ctx, lotIDs)
	if err != nil {
		return report, err
	}
	expected, affected, err := simulateReplay(decoded, states)
	report.AffectedLots = affected
	if err != nil {
		return report, err
	}
	if !request.Execute {
		return report, nil
	}
	repairID, err := newRepairID()
	if err != nil {
		return report, err
	}
	report.AuditID = repairID
	if err := service.store.BeginReplayAudit(ctx, repairID, request, toOffsetExclusive, report); err != nil {
		return report, err
	}
	finishedAudit := false
	defer func() {
		if finishedAudit {
			return
		}
		failureDetail := map[string]any{"report": report}
		if returnErr != nil {
			failureDetail["error"] = returnErr.Error()
		}
		completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if finishErr := service.store.FinishReplayAudit(completionCtx, repairID, false, failureDetail, nil); finishErr != nil {
			returnErr = errors.Join(returnErr, finishErr)
		}
	}()
	for _, record := range decoded {
		result, err := service.applier.Apply(ctx, record)
		if err != nil {
			return report, fmt.Errorf("apply replay offset %d: %w", record.Offset, err)
		}
		if result.AlreadyAdvanced || result.NextOffset != record.Offset+1 {
			return report, fmt.Errorf("%w: concurrent projector movement at offset %d returned next=%d advanced=%t", ErrUnsafeReplay, record.Offset, result.NextOffset, result.AlreadyAdvanced)
		}
		report.AppliedRecords++
	}
	currentOffset, err := service.store.ReadPartitionOffset(ctx, eventcontract.RuntimeProjectionTopicV1, request.Partition)
	if err != nil {
		return report, err
	}
	actualStates, err := service.store.ReadLotStates(ctx, lotIDs)
	if err != nil {
		return report, err
	}
	report.AffectedLots, report.Verified = verifyReplay(toOffsetExclusive, currentOffset, expected, actualStates)
	if !report.Verified {
		return report, ErrVerificationFailed
	}
	report.ResumeSafe = true
	report.RestartRequired = true
	if err := service.store.FinishReplayAudit(ctx, repairID, true, report, expectedLotIDs(expected)); err != nil {
		return report, err
	}
	finishedAudit = true
	return report, nil
}

func expectedLotIDs(expected map[string]expectedLotState) []string {
	result := make([]string, 0, len(expected))
	for lotID := range expected {
		result = append(result, lotID)
	}
	sort.Strings(result)
	return result
}

type expectedLotState struct {
	InitialVersion int64
	EventID        string
	Version        int64
	CanonicalHash  string
}

func simulateReplay(records []projector.DecodedRecord, states map[string]LotState) (map[string]expectedLotState, []AffectedLot, error) {
	versions := make(map[string]int64)
	expected := make(map[string]expectedLotState)
	for _, record := range records {
		fact := record.Fact
		current, exists := versions[fact.GetLotId()]
		if !exists {
			state, stateExists := states[fact.GetLotId()]
			if !stateExists {
				return nil, nil, fmt.Errorf("%w: auction lot %s is missing", ErrUnsafeReplay, fact.GetLotId())
			}
			if state.ProjectionStateFound {
				if state.Frozen {
					return nil, nil, fmt.Errorf("%w: lot %s is frozen", ErrUnsafeReplay, fact.GetLotId())
				}
				if state.LastLotVersion != state.LotVersion {
					return nil, nil, fmt.Errorf("%w: lot %s projection=%d auction_lot=%d", ErrUnsafeReplay, fact.GetLotId(), state.LastLotVersion, state.LotVersion)
				}
				current = state.LastLotVersion
			} else {
				current = state.LotVersion
			}
			versions[fact.GetLotId()] = current
		}
		if fact.GetPrevLotVersion() != current {
			return nil, nil, fmt.Errorf("%w: lot %s offset=%d prev=%d expected=%d", ErrUnsafeReplay, fact.GetLotId(), record.Offset, fact.GetPrevLotVersion(), current)
		}
		hash, err := eventcontract.CanonicalStateHash(fact.GetStateAfter())
		if err != nil {
			return nil, nil, fmt.Errorf("derive replay canonical hash: %w", err)
		}
		initial := current
		if previous, seen := expected[fact.GetLotId()]; seen {
			initial = previous.InitialVersion
		}
		expected[fact.GetLotId()] = expectedLotState{
			InitialVersion: initial, EventID: fact.GetEventId(), Version: fact.GetLotVersion(), CanonicalHash: hash,
		}
		versions[fact.GetLotId()] = fact.GetLotVersion()
	}
	return expected, affectedLots(expected, nil), nil
}

func verifyReplay(
	expectedOffset int64,
	actualOffset PartitionOffset,
	expected map[string]expectedLotState,
	actual map[string]LotState,
) ([]AffectedLot, bool) {
	verified := actualOffset.Found && actualOffset.NextOffset == expectedOffset
	result := affectedLots(expected, actual)
	for _, item := range result {
		if !item.Verified {
			verified = false
		}
	}
	return result, verified
}

func affectedLots(expected map[string]expectedLotState, actual map[string]LotState) []AffectedLot {
	lotIDs := make([]string, 0, len(expected))
	for lotID := range expected {
		lotIDs = append(lotIDs, lotID)
	}
	sort.Strings(lotIDs)
	result := make([]AffectedLot, 0, len(lotIDs))
	for _, lotID := range lotIDs {
		want := expected[lotID]
		item := AffectedLot{
			LotID: lotID, InitialVersion: want.InitialVersion, ExpectedEventID: want.EventID,
			ExpectedVersion: want.Version, ExpectedCanonicalHash: want.CanonicalHash,
		}
		if actual != nil {
			if got, exists := actual[lotID]; exists {
				item.ActualEventID = got.LastEventID
				item.ActualVersion = got.LastLotVersion
				item.ActualCanonicalHash = got.CanonicalHash
				item.ActualLotVersion = got.LotVersion
				item.Verified = !got.Frozen && got.LastEventID == want.EventID && got.LastLotVersion == want.Version &&
					got.LotVersion == want.Version && got.CanonicalHash == want.CanonicalHash
			}
		}
		result = append(result, item)
	}
	return result
}

func describeRecords(records []*kgo.Record, inbox []InboxEntry) ([]RecordReport, []string) {
	inboxByOffset := make(map[int64]InboxEntry, len(inbox))
	for _, entry := range inbox {
		inboxByOffset[entry.Offset] = entry
	}
	reports := make([]RecordReport, 0, len(records))
	lotIDs := make([]string, 0, len(records))
	for _, source := range records {
		report, decoded, err := describeRecord(source)
		entry, hasInbox := inboxByOffset[report.Offset]
		switch {
		case !hasInbox:
			report.InboxStatus = "missing"
		case err != nil:
			report.InboxStatus = "unverifiable"
		case entry.EventID == decoded.Fact.GetEventId() && entry.LotID == decoded.Fact.GetLotId() &&
			entry.LotVersion == decoded.Fact.GetLotVersion() && entry.PayloadHash == decoded.PayloadHash:
			report.InboxStatus = "matched"
		default:
			report.InboxStatus = "conflict"
		}
		if err == nil {
			lotIDs = append(lotIDs, decoded.Fact.GetLotId())
		}
		reports = append(reports, report)
	}
	return reports, lotIDs
}

func decodeReplayRecords(records []*kgo.Record) ([]projector.DecodedRecord, []RecordReport, []string, error) {
	decoded := make([]projector.DecodedRecord, 0, len(records))
	reports := make([]RecordReport, 0, len(records))
	lotIDs := make([]string, 0, len(records))
	for _, source := range records {
		report, record, err := describeRecord(source)
		report.InboxStatus = "unchecked"
		reports = append(reports, report)
		if err != nil {
			return nil, reports, lotIDs, fmt.Errorf("decode replay offset %d: %w", report.Offset, err)
		}
		decoded = append(decoded, record)
		lotIDs = append(lotIDs, record.Fact.GetLotId())
	}
	return decoded, reports, lotIDs, nil
}

func describeRecord(source *kgo.Record) (RecordReport, projector.DecodedRecord, error) {
	report := RecordReport{InboxStatus: "unchecked"}
	if source != nil {
		report.Offset = source.Offset
		hash := sha256.Sum256(source.Value)
		report.PayloadHash = hex.EncodeToString(hash[:])
	}
	decoded, err := projector.DecodeRecord(source)
	if err != nil {
		report.DecodeError = err.Error()
		return report, projector.DecodedRecord{}, err
	}
	report.EventID = decoded.Fact.GetEventId()
	report.LotID = decoded.Fact.GetLotId()
	report.PrevLotVersion = decoded.Fact.GetPrevLotVersion()
	report.LotVersion = decoded.Fact.GetLotVersion()
	report.Command = decoded.Fact.GetCommand().String()
	report.PayloadHash = decoded.PayloadHash
	return report, decoded, nil
}

func sortedLotStates(values map[string]LotState) []LotState {
	result := make([]LotState, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].LotID < result[right].LotID })
	return result
}

func validateReplayRequest(request ReplayRequest) error {
	if request.Partition < 0 || request.ExpectedNextOffset < 0 || request.ThroughOffset < request.ExpectedNextOffset ||
		request.ThroughOffset-request.ExpectedNextOffset+1 > MaxReplayRecords {
		return errors.New("projection repair replay range is invalid")
	}
	if !request.Execute {
		return nil
	}
	request.Operator = strings.TrimSpace(request.Operator)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Operator == "" || len(request.Operator) > 128 || strings.ContainsAny(request.Operator, "\r\n\x00") {
		return errors.New("projection repair operator is invalid")
	}
	if request.Reason == "" || len(request.Reason) > 512 || strings.ContainsAny(request.Reason, "\x00") {
		return errors.New("projection repair reason is invalid")
	}
	return nil
}

func newRepairID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate projection repair ID: %w", err)
	}
	return "projection-repair-" + hex.EncodeToString(value[:]), nil
}
