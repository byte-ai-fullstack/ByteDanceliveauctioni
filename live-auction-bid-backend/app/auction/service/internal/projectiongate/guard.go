package projectiongate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/observability"
)

const maxKafkaTimestampFutureSkew = 5 * time.Minute

type Guard struct {
	source  RuntimeSource
	offsets OffsetSource
	config  Config
	logger  *slog.Logger
	now     func() time.Time

	refreshMu sync.Mutex

	mu                 sync.RWMutex
	snapshot           Snapshot
	consecutiveHealthy int
}

func NewGuard(source RuntimeSource, offsets OffsetSource, config Config, logger *slog.Logger) (*Guard, error) {
	if source == nil || offsets == nil || logger == nil {
		return nil, errors.New("projection gate source, offsets, and logger are required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Guard{
		source:   source,
		offsets:  offsets,
		config:   config,
		logger:   logger.With(slog.String("component", "projection_gate")),
		now:      time.Now,
		snapshot: Snapshot{Reason: ReasonUninitialized},
	}, nil
}

func (guard *Guard) Run(ctx context.Context) {
	if guard == nil {
		return
	}
	ticker := time.NewTicker(guard.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			guard.freeze(ReasonSnapshotStale, ctx.Err())
			return
		case <-ticker.C:
			_ = guard.Refresh(ctx)
		}
	}
}

func (guard *Guard) Refresh(ctx context.Context) error {
	if guard == nil || guard.source == nil || guard.offsets == nil || guard.now == nil {
		return closedError(ReasonUninitialized)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	guard.refreshMu.Lock()
	defer guard.refreshMu.Unlock()

	refreshCtx, cancel := context.WithTimeout(ctx, guard.config.RefreshTimeout)
	defer cancel()
	bounds, err := guard.source.Bounds(refreshCtx)
	if err != nil {
		guard.freeze(ReasonKafkaUnavailable, err)
		return fmt.Errorf("%w: read Kafka bounds: %v", ErrClosed, err)
	}
	offsets, err := guard.offsets.Offsets(refreshCtx)
	if err != nil {
		guard.freeze(ReasonMySQLUnavailable, err)
		return fmt.Errorf("%w: read MySQL projection offsets: %v", ErrClosed, err)
	}
	candidate, err := guard.evaluate(refreshCtx, guard.now().UTC(), bounds, offsets)
	if err != nil {
		guard.freeze(ReasonKafkaUnavailable, err)
		return fmt.Errorf("%w: read oldest Kafka records: %v", ErrClosed, err)
	}
	published := guard.publish(candidate)
	if !published.Ready {
		return closedError(published.Reason)
	}
	return nil
}

func (guard *Guard) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	status := guard.Status()
	if status.Ready {
		return nil
	}
	observability.RecordProjectionGateRejection(status.Reason)
	return closedError(status.Reason)
}

func (guard *Guard) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	status := guard.Status()
	if status.Ready {
		return nil
	}
	return closedError(status.Reason)
}

func (guard *Guard) Status() Snapshot {
	if guard == nil {
		return Snapshot{Reason: ReasonUninitialized}
	}
	guard.mu.Lock()
	previous := cloneSnapshot(guard.snapshot)
	status := cloneSnapshot(previous)
	status = guard.effective(status, guard.now().UTC())
	if status.Reason == ReasonSnapshotStale && previous.Reason != ReasonSnapshotStale {
		guard.consecutiveHealthy = 0
		status.ConsecutiveHealthyPolls = 0
		guard.snapshot = cloneSnapshot(status)
	}
	guard.recordMetrics(status)
	guard.mu.Unlock()
	guard.observeTransition(previous, status, nil)
	return status
}

func (guard *Guard) MetricsSnapshot(context.Context) map[string]any {
	status := guard.Status()
	return map[string]any{
		"projection_gate": status,
	}
}

func (guard *Guard) evaluate(
	ctx context.Context,
	now time.Time,
	bounds map[int32]PartitionBounds,
	offsets map[int32]ProjectionOffset,
) (Snapshot, error) {
	snapshot := Snapshot{
		Reason:              ReasonHealthy,
		CheckedAtMs:         now.UnixMilli(),
		PartitionCount:      len(bounds),
		RetentionHeadroomMs: guard.config.RuntimeTopicRetention.Milliseconds(),
	}
	if len(bounds) == 0 {
		snapshot.Reason = ReasonPartitionMismatch
		return snapshot, nil
	}
	partitions := make([]int32, 0, len(bounds))
	for partition := range bounds {
		partitions = append(partitions, partition)
	}
	sort.Slice(partitions, func(left, right int) bool { return partitions[left] < partitions[right] })
	for partition := range offsets {
		if _, exists := bounds[partition]; !exists {
			snapshot.Reason = ReasonPartitionMismatch
			return snapshot, nil
		}
	}

	starts := make(map[int32]int64)
	for _, partition := range partitions {
		bound := bounds[partition]
		offset, exists := offsets[partition]
		if !exists || offset.NextOffset < 0 || offset.UpdatedAtMs <= 0 {
			snapshot.Reason = ReasonOffsetMissing
			return snapshot, nil
		}
		if bound.Earliest < 0 || bound.Latest < bound.Earliest {
			snapshot.Reason = ReasonPartitionMismatch
			return snapshot, nil
		}
		if offset.NextOffset < bound.Earliest {
			snapshot.Reason = ReasonRetentionCliff
			return snapshot, nil
		}
		if offset.NextOffset > bound.Latest {
			snapshot.Reason = ReasonOffsetAhead
			return snapshot, nil
		}
		lag := bound.Latest - offset.NextOffset
		if snapshot.TotalLagRecords > math.MaxInt64-lag {
			snapshot.TotalLagRecords = math.MaxInt64
		} else {
			snapshot.TotalLagRecords += lag
		}
		if lag > snapshot.MaxPartitionLagRecords {
			snapshot.MaxPartitionLagRecords = lag
		}
		item := PartitionSnapshot{
			Partition:    partition,
			Earliest:     bound.Earliest,
			DatabaseNext: offset.NextOffset,
			Latest:       bound.Latest,
			LagRecords:   lag,
		}
		snapshot.Partitions = append(snapshot.Partitions, item)
		if lag > 0 {
			starts[partition] = offset.NextOffset
		}
	}
	if len(starts) > 0 {
		records, err := guard.source.OldestRecords(ctx, starts)
		if err != nil {
			return Snapshot{}, err
		}
		for index := range snapshot.Partitions {
			item := &snapshot.Partitions[index]
			if item.LagRecords == 0 {
				continue
			}
			record, exists := records[item.Partition]
			if !exists || record.Offset != item.DatabaseNext {
				snapshot.Reason = ReasonRecordMissing
				return snapshot, nil
			}
			if record.Timestamp.IsZero() || record.Timestamp.After(now.Add(maxKafkaTimestampFutureSkew)) {
				snapshot.Reason = ReasonRecordTimestampInvalid
				return snapshot, nil
			}
			age := now.Sub(record.Timestamp)
			if age < 0 {
				age = 0
			}
			item.OldestTimestamp = record.Timestamp.UnixMilli()
			item.OldestAgeMs = age.Milliseconds()
			if item.OldestAgeMs > snapshot.OldestAgeMs {
				snapshot.OldestAgeMs = item.OldestAgeMs
			}
		}
	}
	oldestAge := time.Duration(snapshot.OldestAgeMs) * time.Millisecond
	if oldestAge >= guard.config.RuntimeTopicRetention {
		snapshot.RetentionHeadroomMs = 0
	} else {
		snapshot.RetentionHeadroomMs = (guard.config.RuntimeTopicRetention - oldestAge).Milliseconds()
	}
	switch {
	case snapshot.MaxPartitionLagRecords > guard.config.MaxLagRecords:
		snapshot.Reason = ReasonLagLimit
	case oldestAge > guard.config.MaxOldestAge:
		snapshot.Reason = ReasonOldestAgeLimit
	case time.Duration(snapshot.RetentionHeadroomMs)*time.Millisecond < guard.config.MinRetentionHeadroom:
		snapshot.Reason = ReasonRetentionHeadroom
	}
	return snapshot, nil
}

func (guard *Guard) publish(candidate Snapshot) Snapshot {
	now := guard.now().UTC()
	candidate.CheckedAtMs = now.UnixMilli()
	candidate.LastSuccessfulCheckAtMs = now.UnixMilli()
	candidate.Ready = candidate.Reason == ReasonHealthy

	guard.mu.Lock()
	previous := cloneSnapshot(guard.snapshot)
	previous = guard.effective(previous, now)
	if previous.Reason == ReasonSnapshotStale {
		guard.consecutiveHealthy = 0
	}
	if candidate.Ready {
		guard.consecutiveHealthy++
		if previous.Ready {
			guard.consecutiveHealthy = guard.config.HealthyPollsToOpen
		}
		if guard.consecutiveHealthy < guard.config.HealthyPollsToOpen {
			candidate.Ready = false
			candidate.Reason = ReasonRecovering
		}
	} else {
		guard.consecutiveHealthy = 0
	}
	candidate.ConsecutiveHealthyPolls = guard.consecutiveHealthy
	guard.snapshot = cloneSnapshot(candidate)
	guard.mu.Unlock()

	guard.observeTransition(previous, candidate, nil)
	guard.recordMetrics(candidate)
	return candidate
}

func (guard *Guard) freeze(reason string, cause error) {
	now := guard.now().UTC()
	guard.mu.Lock()
	previous := guard.snapshot
	current := cloneSnapshot(previous)
	current.Ready = false
	current.Reason = reason
	current.CheckedAtMs = now.UnixMilli()
	current.ConsecutiveHealthyPolls = 0
	if current.LastSuccessfulCheckAtMs > 0 {
		age := now.Sub(time.UnixMilli(current.LastSuccessfulCheckAtMs))
		if age < 0 {
			age = 0
		}
		current.SnapshotAgeMs = age.Milliseconds()
	}
	guard.consecutiveHealthy = 0
	guard.snapshot = current
	guard.mu.Unlock()

	guard.observeTransition(previous, current, cause)
	guard.recordMetrics(current)
}

func (guard *Guard) effective(status Snapshot, now time.Time) Snapshot {
	if status.LastSuccessfulCheckAtMs <= 0 {
		status.Ready = false
		return status
	}
	age := now.Sub(time.UnixMilli(status.LastSuccessfulCheckAtMs))
	if age < 0 {
		age = 0
	}
	status.SnapshotAgeMs = age.Milliseconds()
	if age > guard.config.MaxStaleness && (status.Ready || status.Reason == ReasonRecovering) {
		status.Ready = false
		status.Reason = ReasonSnapshotStale
	}
	return status
}

func (guard *Guard) observeTransition(previous, current Snapshot, cause error) {
	if previous.Ready == current.Ready && previous.Reason == current.Reason {
		return
	}
	attributes := []any{
		slog.Bool("ready", current.Ready),
		slog.String("reason", current.Reason),
		slog.Int64("total_lag_records", current.TotalLagRecords),
		slog.Int64("oldest_age_ms", current.OldestAgeMs),
	}
	if cause != nil {
		attributes = append(attributes, slog.String("error", boundedError(cause)))
	}
	if current.Ready {
		guard.logger.Info("projection gate opened", attributes...)
		return
	}
	guard.logger.Warn("projection gate closed", attributes...)
}

func (guard *Guard) recordMetrics(status Snapshot) {
	observability.SetProjectionGateState(
		status.Ready,
		status.Reason,
		status.TotalLagRecords,
		status.MaxPartitionLagRecords,
		status.OldestAgeMs,
		status.RetentionHeadroomMs,
		status.SnapshotAgeMs,
	)
}

func cloneSnapshot(value Snapshot) Snapshot {
	value.Partitions = append([]PartitionSnapshot(nil), value.Partitions...)
	return value
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Map(func(char rune) rune {
		if char == '\r' || char == '\n' || char == '\x00' {
			return ' '
		}
		return char
	}, err.Error())
	const limit = 384
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
