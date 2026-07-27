package projectionrepair

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

type syntheticRecordSummary struct {
	Offset        int64
	EventID       string
	LotID         string
	PrevVersion   int64
	Version       int64
	Command       string
	PayloadHash   string
	CanonicalHash string
}

type syntheticPreflight struct {
	metadata SyntheticBundleMetadata
	report   SyntheticReport
	expected map[string]expectedLotState
	states   map[string]LotState
}

func (service *Service) Synthetic(ctx context.Context, request SyntheticRequest) (report SyntheticReport, returnErr error) {
	if service == nil || service.store == nil || service.source == nil || service.applier == nil || service.now == nil {
		return SyntheticReport{}, errors.New("projection repair service is not initialized")
	}
	request.BundlePath = strings.TrimSpace(request.BundlePath)
	request.ExpectedSHA256 = strings.TrimSpace(request.ExpectedSHA256)
	request.ExecutedBy = strings.TrimSpace(request.ExecutedBy)
	if err := validateSyntheticRequest(request); err != nil {
		return SyntheticReport{}, err
	}

	previewBundle, err := OpenSyntheticBundle(request.BundlePath, request.ExpectedSHA256, service.now())
	if err != nil {
		return SyntheticReport{}, err
	}
	preview, err := service.preflightSynthetic(ctx, previewBundle, request)
	closeErr := previewBundle.Close()
	if err != nil {
		return preview.report, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return preview.report, closeErr
	}
	report = preview.report
	if !request.Execute {
		return report, nil
	}
	if err := validateSyntheticExecution(request, preview.metadata); err != nil {
		return report, err
	}

	executionBundle, err := OpenSyntheticBundle(request.BundlePath, request.ExpectedSHA256, service.now())
	if err != nil {
		return report, err
	}
	defer func() {
		if closeErr := executionBundle.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	execution, err := service.preflightSynthetic(ctx, executionBundle, request)
	if err != nil {
		return report, err
	}
	if !sameSyntheticPreflight(preview, execution) {
		return report, fmt.Errorf("%w: database, Kafka earliest, or lot state changed after preview", ErrUnsafeSynthetic)
	}
	report = execution.report
	report.Executed = true
	report.ExecutedBy = request.ExecutedBy

	repairID, err := newRepairID()
	if err != nil {
		return report, err
	}
	report.AuditID = repairID
	interrupted, err := service.store.BeginSyntheticAudit(ctx, repairID, request, execution.metadata, report)
	if err != nil {
		return report, err
	}
	report.InterruptedAudits = interrupted
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
		if finishErr := service.store.FinishSyntheticAudit(completionCtx, repairID, false, failureDetail, nil); finishErr != nil {
			returnErr = errors.Join(returnErr, finishErr)
		}
	}()

	metadata, err := executionBundle.Scan(func(record SyntheticRecord) error {
		if record.SourceOffset < execution.report.DatabaseOffset.NextOffset {
			return nil
		}
		result, applyErr := service.applier.Apply(ctx, record.Decoded(execution.metadata))
		if applyErr != nil {
			return fmt.Errorf("apply synthetic repair offset %d: %w", record.SourceOffset, applyErr)
		}
		if result.AlreadyAdvanced || result.DuplicateEvent || result.NextOffset != record.SourceOffset+1 {
			return fmt.Errorf("%w: concurrent projector movement at offset %d returned next=%d advanced=%t duplicate=%t",
				ErrUnsafeSynthetic, record.SourceOffset, result.NextOffset, result.AlreadyAdvanced, result.DuplicateEvent)
		}
		report.AppliedRecords++
		return nil
	})
	if err != nil {
		return report, err
	}
	if metadata != execution.metadata {
		return report, fmt.Errorf("%w: bundle metadata changed during execution", ErrUnsafeSynthetic)
	}

	currentOffset, err := service.store.ReadPartitionOffset(ctx, execution.metadata.Topic, execution.metadata.Partition)
	if err != nil {
		return report, err
	}
	actualStates, err := service.store.ReadLotStates(ctx, expectedLotIDs(execution.expected))
	if err != nil {
		return report, err
	}
	report.AffectedLots, report.Verified = verifyReplay(execution.metadata.ToOffsetExclusive, currentOffset, execution.expected, actualStates)
	finalBounds, err := service.source.Bounds(ctx, execution.metadata.Partition)
	if err != nil {
		return report, err
	}
	report.DatabaseOffset = currentOffset
	report.KafkaBounds = finalBounds
	if finalBounds.Earliest != execution.metadata.ToOffsetExclusive {
		report.Verified = false
	}
	if !report.Verified {
		return report, ErrVerificationFailed
	}
	report.ResumeSafe = true
	report.RestartRequired = true
	if err := service.store.FinishSyntheticAudit(ctx, repairID, true, report, expectedLotIDs(execution.expected)); err != nil {
		return report, err
	}
	finishedAudit = true
	return report, nil
}

func (service *Service) preflightSynthetic(
	ctx context.Context,
	bundle *VerifiedSyntheticBundle,
	request SyntheticRequest,
) (syntheticPreflight, error) {
	summaries := make([]syntheticRecordSummary, 0)
	metadata, err := bundle.Scan(func(record SyntheticRecord) error {
		canonicalHash, hashErr := eventcontract.CanonicalStateHash(record.Fact.GetStateAfter())
		if hashErr != nil {
			return fmt.Errorf("derive synthetic repair canonical hash: %w", hashErr)
		}
		summaries = append(summaries, syntheticRecordSummary{
			Offset: record.SourceOffset, EventID: record.RepairEventID, LotID: record.Fact.GetLotId(),
			PrevVersion: record.Fact.GetPrevLotVersion(), Version: record.Fact.GetLotVersion(),
			Command: record.Fact.GetCommand().String(), PayloadHash: record.PayloadHash, CanonicalHash: canonicalHash,
		})
		return nil
	})
	preflight := syntheticPreflight{metadata: metadata}
	if err != nil {
		return preflight, err
	}
	report := SyntheticReport{
		Topic: metadata.Topic, Partition: metadata.Partition, FromOffset: metadata.FromOffset,
		ToOffsetExclusive: metadata.ToOffsetExclusive, BundleSHA256: metadata.BundleSHA256,
		PreparedBy: metadata.PreparedBy, ChangeTicket: metadata.ChangeTicket, RepairReason: metadata.RepairReason,
		Executed: request.Execute,
	}
	preflight.report = report

	bounds, err := service.source.Bounds(ctx, metadata.Partition)
	if err != nil {
		return preflight, err
	}
	offset, err := service.store.ReadPartitionOffset(ctx, metadata.Topic, metadata.Partition)
	if err != nil {
		return preflight, err
	}
	report.KafkaBounds = bounds
	report.DatabaseOffset = offset
	preflight.report = report
	if !offset.Found || offset.NextOffset < metadata.FromOffset || offset.NextOffset > bounds.Earliest ||
		metadata.ToOffsetExclusive != bounds.Earliest {
		return preflight, fmt.Errorf("%w: require bundle.from<=DB next<=Kafka earliest=bundle.to; found=%t DB=%d Kafka=%d bundle=[%d,%d)",
			ErrUnsafeSynthetic, offset.Found, offset.NextOffset, bounds.Earliest, metadata.FromOffset, metadata.ToOffsetExclusive)
	}
	report.PrefixRecords = int(offset.NextOffset - metadata.FromOffset)
	report.SuffixRecords = metadata.RecordCount - report.PrefixRecords
	report.CompletionOnly = offset.NextOffset == bounds.Earliest

	history, err := service.store.ReadSyntheticAuditHistory(ctx, metadata)
	if err != nil {
		return preflight, err
	}
	report.PriorAudits = history
	if history.Succeeded != 0 {
		return preflight, fmt.Errorf("%w: an identical synthetic repair already succeeded", ErrUnsafeSynthetic)
	}
	if report.PrefixRecords > 0 && history.Started+history.Failed == 0 {
		return preflight, fmt.Errorf("%w: committed prefix has no matching prior bundle audit", ErrUnsafeSynthetic)
	}

	inboxRange, err := service.store.ReadInboxRange(ctx, metadata.Topic, metadata.Partition, metadata.FromOffset, metadata.ToOffsetExclusive)
	if err != nil {
		return preflight, err
	}
	eventIDs := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		eventIDs = append(eventIDs, summary.EventID)
	}
	inboxEvents, err := service.store.ReadInboxByEventIDs(ctx, eventIDs)
	if err != nil {
		return preflight, err
	}
	inboxByOffset := make(map[int64]InboxEntry, len(inboxRange))
	for _, entry := range inboxRange {
		if entry.Offset < metadata.FromOffset || entry.Offset >= metadata.ToOffsetExclusive {
			return preflight, fmt.Errorf("%w: inbox source position escaped requested range", ErrUnsafeSynthetic)
		}
		if _, duplicate := inboxByOffset[entry.Offset]; duplicate {
			return preflight, fmt.Errorf("%w: duplicate inbox source offset %d", ErrUnsafeSynthetic, entry.Offset)
		}
		inboxByOffset[entry.Offset] = entry
	}
	recordReports := make([]RecordReport, 0, len(summaries))
	for _, summary := range summaries {
		sourceEntry, sourceExists := inboxByOffset[summary.Offset]
		eventEntry, eventExists := inboxEvents[summary.EventID]
		status := "absent"
		if summary.Offset < offset.NextOffset {
			if !sourceExists || !eventExists || !sameSyntheticInbox(summary, sourceEntry) || sourceEntry != eventEntry {
				return preflight, fmt.Errorf("%w: committed prefix inbox conflict at offset %d", ErrUnsafeSynthetic, summary.Offset)
			}
			status = "matched"
		} else if sourceExists || eventExists {
			return preflight, fmt.Errorf("%w: suffix source position or event ID already exists at offset %d", ErrUnsafeSynthetic, summary.Offset)
		}
		recordReports = append(recordReports, RecordReport{
			Offset: summary.Offset, EventID: summary.EventID, LotID: summary.LotID,
			PrevLotVersion: summary.PrevVersion, LotVersion: summary.Version, Command: summary.Command,
			PayloadHash: summary.PayloadHash, InboxStatus: status,
		})
	}
	report.Records = recordReports

	expected, boundary := syntheticExpectedStates(summaries, offset.NextOffset)
	lotIDs := expectedLotIDs(expected)
	states, err := service.store.ReadLotStates(ctx, lotIDs)
	if err != nil {
		return preflight, err
	}
	if err := validateSyntheticLotChain(summaries, offset.NextOffset, states, boundary); err != nil {
		return preflight, err
	}
	report.AffectedLots = affectedLots(expected, nil)
	preflight.metadata = metadata
	preflight.report = report
	preflight.expected = expected
	preflight.states = states
	return preflight, nil
}

func validateSyntheticRequest(request SyntheticRequest) error {
	if request.BundlePath == "" || !validLowerHexDigest(request.ExpectedSHA256) {
		return errors.New("synthetic repair requires a bundle path and exact lowercase SHA-256")
	}
	if !request.Execute && (request.ExecutedBy != "" || request.Confirm != "") {
		return errors.New("synthetic repair execution identity and confirmation require --execute")
	}
	return nil
}

func validateSyntheticExecution(request SyntheticRequest, metadata SyntheticBundleMetadata) error {
	if !validAuditText(request.ExecutedBy, 128, false) {
		return errors.New("synthetic repair executor identity is invalid")
	}
	if request.ExecutedBy == metadata.PreparedBy {
		return errors.New("synthetic repair preparer and executor must be different people")
	}
	expected := SyntheticConfirmation(metadata)
	if request.Confirm != expected {
		return fmt.Errorf("synthetic repair execution requires --confirm=%s", expected)
	}
	return nil
}

func SyntheticConfirmation(metadata SyntheticBundleMetadata) string {
	return fmt.Sprintf("SYNTHETIC_PARTITION_%d_FROM_%d_TO_%d_SHA256_%s",
		metadata.Partition, metadata.FromOffset, metadata.ToOffsetExclusive, metadata.BundleSHA256)
}

func sameSyntheticInbox(summary syntheticRecordSummary, entry InboxEntry) bool {
	return entry.Offset == summary.Offset && entry.EventID == summary.EventID && entry.LotID == summary.LotID &&
		entry.LotVersion == summary.Version && entry.PayloadHash == summary.PayloadHash
}

func syntheticExpectedStates(
	summaries []syntheticRecordSummary,
	nextOffset int64,
) (map[string]expectedLotState, map[string]expectedLotState) {
	expected := make(map[string]expectedLotState)
	boundary := make(map[string]expectedLotState)
	for _, summary := range summaries {
		initial := summary.PrevVersion
		if previous, exists := expected[summary.LotID]; exists {
			initial = previous.InitialVersion
		}
		state := expectedLotState{
			InitialVersion: initial, EventID: summary.EventID, Version: summary.Version, CanonicalHash: summary.CanonicalHash,
		}
		expected[summary.LotID] = state
		if summary.Offset < nextOffset {
			boundary[summary.LotID] = state
		}
	}
	return expected, boundary
}

func validateSyntheticLotChain(
	summaries []syntheticRecordSummary,
	nextOffset int64,
	states map[string]LotState,
	boundary map[string]expectedLotState,
) error {
	versions := make(map[string]int64)
	for _, summary := range summaries {
		state, exists := states[summary.LotID]
		if !exists {
			return fmt.Errorf("%w: auction lot %s is missing", ErrUnsafeSynthetic, summary.LotID)
		}
		if state.Frozen {
			return fmt.Errorf("%w: lot %s is frozen", ErrUnsafeSynthetic, summary.LotID)
		}
		if state.ProjectionStateFound && state.LastLotVersion != state.LotVersion {
			return fmt.Errorf("%w: lot %s projection=%d auction_lot=%d", ErrUnsafeSynthetic, summary.LotID, state.LastLotVersion, state.LotVersion)
		}
	}
	for lotID, want := range boundary {
		state := states[lotID]
		if !state.ProjectionStateFound || state.LastEventID != want.EventID || state.LastLotVersion != want.Version ||
			state.LotVersion != want.Version || state.CanonicalHash != want.CanonicalHash {
			return fmt.Errorf("%w: committed prefix lot state conflict for %s", ErrUnsafeSynthetic, lotID)
		}
		versions[lotID] = want.Version
	}
	for _, summary := range summaries {
		if summary.Offset < nextOffset {
			continue
		}
		current, exists := versions[summary.LotID]
		if !exists {
			state := states[summary.LotID]
			current = state.LotVersion
			if state.ProjectionStateFound {
				current = state.LastLotVersion
			}
		}
		if summary.PrevVersion != current {
			return fmt.Errorf("%w: lot %s offset=%d prev=%d expected=%d",
				ErrUnsafeSynthetic, summary.LotID, summary.Offset, summary.PrevVersion, current)
		}
		versions[summary.LotID] = summary.Version
	}
	return nil
}

func sameSyntheticPreflight(left, right syntheticPreflight) bool {
	return left.metadata == right.metadata &&
		left.report.DatabaseOffset == right.report.DatabaseOffset &&
		left.report.KafkaBounds.Earliest == right.report.KafkaBounds.Earliest &&
		left.report.PrefixRecords == right.report.PrefixRecords &&
		left.report.SuffixRecords == right.report.SuffixRecords &&
		reflect.DeepEqual(left.expected, right.expected) && reflect.DeepEqual(left.states, right.states)
}
