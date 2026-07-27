package projectiongate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeRuntimeSource struct {
	mu        sync.RWMutex
	bounds    map[int32]PartitionBounds
	records   map[int32]OldestRecord
	boundsErr error
	recordErr error
}

func (source *fakeRuntimeSource) Bounds(context.Context) (map[int32]PartitionBounds, error) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	return cloneBounds(source.bounds), source.boundsErr
}

func (source *fakeRuntimeSource) OldestRecords(
	_ context.Context,
	starts map[int32]int64,
) (map[int32]OldestRecord, error) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	if source.recordErr != nil {
		return nil, source.recordErr
	}
	result := make(map[int32]OldestRecord, len(starts))
	for partition := range starts {
		if record, exists := source.records[partition]; exists {
			result[partition] = record
		}
	}
	return result, nil
}

func (source *fakeRuntimeSource) setBounds(bounds map[int32]PartitionBounds) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.bounds = cloneBounds(bounds)
}

type fakeOffsetSource struct {
	mu      sync.RWMutex
	offsets map[int32]ProjectionOffset
	err     error
}

func (source *fakeOffsetSource) Offsets(context.Context) (map[int32]ProjectionOffset, error) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	result := make(map[int32]ProjectionOffset, len(source.offsets))
	for partition, offset := range source.offsets {
		result[partition] = offset
	}
	return result, source.err
}

func testConfig() Config {
	return Config{
		RefreshInterval:       time.Second,
		RefreshTimeout:        500 * time.Millisecond,
		MaxStaleness:          2 * time.Second,
		MaxLagRecords:         10,
		MaxOldestAge:          5 * time.Minute,
		RuntimeTopicRetention: time.Hour,
		MinRetentionHeadroom:  10 * time.Minute,
		HealthyPollsToOpen:    3,
	}
}

func newTestGuard(t *testing.T) (*Guard, *fakeRuntimeSource, *fakeOffsetSource, *time.Time) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	runtimeSource := &fakeRuntimeSource{
		bounds: map[int32]PartitionBounds{
			0: {Earliest: 10, Latest: 12},
			1: {Earliest: 20, Latest: 20},
		},
		records: map[int32]OldestRecord{
			0: {Offset: 10, Timestamp: now.Add(-time.Second)},
		},
	}
	offsetSource := &fakeOffsetSource{offsets: map[int32]ProjectionOffset{
		0: {NextOffset: 10, UpdatedAtMs: now.UnixMilli()},
		1: {NextOffset: 20, UpdatedAtMs: now.UnixMilli()},
	}}
	guard, err := NewGuard(
		runtimeSource,
		offsetSource,
		testConfig(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	guard.now = func() time.Time { return now }
	return guard, runtimeSource, offsetSource, &now
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := testConfig()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "zero interval", mutate: func(config *Config) { config.RefreshInterval = 0 }},
		{name: "timeout reaches interval", mutate: func(config *Config) { config.RefreshTimeout = config.RefreshInterval }},
		{name: "staleness too short", mutate: func(config *Config) { config.MaxStaleness = config.RefreshInterval }},
		{name: "staleness multiplication overflow", mutate: func(config *Config) {
			config.RefreshInterval = time.Duration(1<<63 - 1)
			config.RefreshTimeout = time.Nanosecond
			config.MaxStaleness = time.Duration(1<<63 - 1)
		}},
		{name: "zero lag", mutate: func(config *Config) { config.MaxLagRecords = 0 }},
		{name: "zero age", mutate: func(config *Config) { config.MaxOldestAge = 0 }},
		{name: "zero retention", mutate: func(config *Config) { config.RuntimeTopicRetention = 0 }},
		{name: "headroom reaches retention", mutate: func(config *Config) { config.MinRetentionHeadroom = config.RuntimeTopicRetention }},
		{name: "zero recovery polls", mutate: func(config *Config) { config.HealthyPollsToOpen = 0 }},
		{name: "excess recovery polls", mutate: func(config *Config) { config.HealthyPollsToOpen = 101 }},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid configuration")
			}
		})
	}
}

func TestNewGuardRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := &fakeRuntimeSource{}
	offsets := &fakeOffsetSource{}
	for _, test := range []struct {
		name    string
		source  RuntimeSource
		offsets OffsetSource
		logger  *slog.Logger
	}{
		{name: "runtime source", offsets: offsets, logger: logger},
		{name: "offset source", source: source, logger: logger},
		{name: "logger", source: source, offsets: offsets},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewGuard(test.source, test.offsets, testConfig(), test.logger); err == nil {
				t.Fatal("NewGuard() error = nil, want missing dependency error")
			}
		})
	}
}

func TestGuardRequiresConsecutiveHealthyRefreshes(t *testing.T) {
	guard, _, _, _ := newTestGuard(t)

	if err := guard.Check(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("initial Check() error = %v, want ErrClosed", err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		err := guard.Refresh(context.Background())
		status := guard.Status()
		if attempt < 3 {
			if !errors.Is(err, ErrClosed) || status.Ready || status.Reason != ReasonRecovering {
				t.Fatalf("attempt %d: err=%v status=%+v, want recovering", attempt, err, status)
			}
			continue
		}
		if err != nil || !status.Ready || status.Reason != ReasonHealthy {
			t.Fatalf("attempt %d: err=%v status=%+v, want healthy", attempt, err, status)
		}
	}
	if err := guard.Check(context.Background()); err != nil {
		t.Fatalf("Check() error = %v, want open gate", err)
	}
}

func TestGuardClosesImmediatelyAndRecoversWithHysteresis(t *testing.T) {
	guard, runtimeSource, _, _ := newTestGuard(t)
	openGuard(t, guard)

	runtimeSource.setBounds(map[int32]PartitionBounds{
		0: {Earliest: 10, Latest: 30},
		1: {Earliest: 20, Latest: 20},
	})
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Refresh() error = %v, want closed lag gate", err)
	}
	status := guard.Status()
	if status.Ready || status.Reason != ReasonLagLimit {
		t.Fatalf("status = %+v, want immediate lag_limit close", status)
	}

	runtimeSource.setBounds(map[int32]PartitionBounds{
		0: {Earliest: 10, Latest: 12},
		1: {Earliest: 20, Latest: 20},
	})
	for attempt := 1; attempt <= 3; attempt++ {
		err := guard.Refresh(context.Background())
		if attempt < 3 && (!errors.Is(err, ErrClosed) || guard.Status().Reason != ReasonRecovering) {
			t.Fatalf("recovery attempt %d: err=%v status=%+v", attempt, err, guard.Status())
		}
		if attempt == 3 && err != nil {
			t.Fatalf("recovery attempt %d: err=%v", attempt, err)
		}
	}
}

func TestGuardClassifiesUnsafeProjectionStates(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		bounds     map[int32]PartitionBounds
		offsets    map[int32]ProjectionOffset
		records    map[int32]OldestRecord
		configure  func(*Config)
		wantReason string
	}{
		{name: "empty partitions", bounds: map[int32]PartitionBounds{}, offsets: map[int32]ProjectionOffset{}, wantReason: ReasonPartitionMismatch},
		{name: "extra database partition", bounds: map[int32]PartitionBounds{0: {Earliest: 1, Latest: 1}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 1), 1: validOffset(now, 1)}, wantReason: ReasonPartitionMismatch},
		{name: "missing offset", bounds: map[int32]PartitionBounds{0: {Earliest: 1, Latest: 1}}, offsets: map[int32]ProjectionOffset{}, wantReason: ReasonOffsetMissing},
		{name: "invalid offset row", bounds: map[int32]PartitionBounds{0: {Earliest: 1, Latest: 1}}, offsets: map[int32]ProjectionOffset{0: {NextOffset: -1, UpdatedAtMs: now.UnixMilli()}}, wantReason: ReasonOffsetMissing},
		{name: "retention cliff", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 9}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 4)}, wantReason: ReasonRetentionCliff},
		{name: "offset ahead", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 9}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 10)}, wantReason: ReasonOffsetAhead},
		{name: "invalid Kafka bounds", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 4}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, wantReason: ReasonPartitionMismatch},
		{name: "missing oldest record", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 6}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{}, wantReason: ReasonRecordMissing},
		{name: "wrong oldest offset", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 6}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{0: {Offset: 6, Timestamp: now}}, wantReason: ReasonRecordMissing},
		{name: "zero oldest timestamp", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 6}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{0: {Offset: 5}}, wantReason: ReasonRecordTimestampInvalid},
		{name: "future oldest timestamp", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 6}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{0: {Offset: 5, Timestamp: now.Add(6 * time.Minute)}}, wantReason: ReasonRecordTimestampInvalid},
		{name: "lag limit", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 16}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{0: {Offset: 5, Timestamp: now}}, wantReason: ReasonLagLimit},
		{name: "oldest age limit", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 6}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{0: {Offset: 5, Timestamp: now.Add(-6 * time.Minute)}}, wantReason: ReasonOldestAgeLimit},
		{name: "retention headroom", bounds: map[int32]PartitionBounds{0: {Earliest: 5, Latest: 6}}, offsets: map[int32]ProjectionOffset{0: validOffset(now, 5)}, records: map[int32]OldestRecord{0: {Offset: 5, Timestamp: now.Add(-51 * time.Minute)}}, configure: func(config *Config) { config.MaxOldestAge = 55 * time.Minute }, wantReason: ReasonRetentionHeadroom},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			if test.configure != nil {
				test.configure(&config)
			}
			runtimeSource := &fakeRuntimeSource{bounds: test.bounds, records: test.records}
			offsetSource := &fakeOffsetSource{offsets: test.offsets}
			guard, err := NewGuard(runtimeSource, offsetSource, config, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("NewGuard() error = %v", err)
			}
			guard.now = func() time.Time { return now }
			if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatalf("Refresh() error = %v, want ErrClosed", err)
			}
			if got := guard.Status().Reason; got != test.wantReason {
				t.Fatalf("reason = %q, want %q; status=%+v", got, test.wantReason, guard.Status())
			}
		})
	}
}

func TestGuardClassifiesDependencyFailures(t *testing.T) {
	guard, runtimeSource, offsetSource, _ := newTestGuard(t)
	runtimeSource.boundsErr = errors.New("broker unavailable\nsecret")
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) || guard.Status().Reason != ReasonKafkaUnavailable {
		t.Fatalf("Kafka failure: err=%v status=%+v", err, guard.Status())
	}

	runtimeSource.boundsErr = nil
	offsetSource.err = errors.New("database unavailable")
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) || guard.Status().Reason != ReasonMySQLUnavailable {
		t.Fatalf("MySQL failure: err=%v status=%+v", err, guard.Status())
	}

	offsetSource.err = nil
	runtimeSource.recordErr = errors.New("fetch failed")
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) || guard.Status().Reason != ReasonKafkaUnavailable {
		t.Fatalf("record failure: err=%v status=%+v", err, guard.Status())
	}
}

func TestGuardFailsClosedWhenSnapshotExpires(t *testing.T) {
	guard, _, _, now := newTestGuard(t)
	openGuard(t, guard)
	*now = now.Add(testConfig().MaxStaleness + time.Millisecond)

	status := guard.Status()
	if status.Ready || status.Reason != ReasonSnapshotStale {
		t.Fatalf("status = %+v, want stale closed gate", status)
	}
	if status.SnapshotAgeMs <= testConfig().MaxStaleness.Milliseconds() {
		t.Fatalf("snapshot age = %d, want over staleness limit", status.SnapshotAgeMs)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		err := guard.Refresh(context.Background())
		if attempt < 3 && (!errors.Is(err, ErrClosed) || guard.Status().Reason != ReasonRecovering) {
			t.Fatalf("stale recovery attempt %d: err=%v status=%+v", attempt, err, guard.Status())
		}
		if attempt == 3 && err != nil {
			t.Fatalf("stale recovery attempt %d: err=%v", attempt, err)
		}
	}
}

func TestGuardRefreshDetectsStalePreviousSnapshotWithoutStatusRead(t *testing.T) {
	guard, _, _, now := newTestGuard(t)
	openGuard(t, guard)
	*now = now.Add(testConfig().MaxStaleness + time.Millisecond)

	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Refresh() error = %v, want recovering closed gate", err)
	}
	status := guard.Status()
	if status.Ready || status.Reason != ReasonRecovering || status.ConsecutiveHealthyPolls != 1 {
		t.Fatalf("status = %+v, want first recovery sample", status)
	}
}

func TestGuardRespectsCanceledContexts(t *testing.T) {
	guard, _, _, _ := newTestGuard(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error = %v, want context.Canceled", err)
	}
	if err := guard.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check() error = %v, want context.Canceled", err)
	}
	if err := guard.Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ping() error = %v, want context.Canceled", err)
	}
}

func TestGuardDependencyFreezeAdvancesSnapshotAge(t *testing.T) {
	guard, runtimeSource, _, now := newTestGuard(t)
	openGuard(t, guard)
	*now = now.Add(time.Second)
	runtimeSource.boundsErr = errors.New("broker unavailable")
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Refresh() error = %v, want ErrClosed", err)
	}
	status := guard.Status()
	if status.Reason != ReasonKafkaUnavailable || status.SnapshotAgeMs != time.Second.Milliseconds() {
		t.Fatalf("dependency freeze status = %+v", status)
	}
}

func TestGuardConcurrentRefreshAndStatus(t *testing.T) {
	guard, _, _, _ := newTestGuard(t)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 25; attempt++ {
				_ = guard.Refresh(context.Background())
				_ = guard.Status()
				_ = guard.MetricsSnapshot(context.Background())
			}
		}()
	}
	wait.Wait()
	if !guard.Status().Ready {
		t.Fatalf("status = %+v, want healthy after concurrent refreshes", guard.Status())
	}
}

func TestBoundedErrorSanitizesAndLimits(t *testing.T) {
	t.Parallel()
	value := boundedError(errors.New(string(make([]byte, 500)) + "\nsecret"))
	if len([]rune(value)) != 384 {
		t.Fatalf("bounded error rune count = %d, want 384", len([]rune(value)))
	}
	if value == "" {
		t.Fatal("bounded error unexpectedly empty")
	}
}

func openGuard(t *testing.T, guard *Guard) {
	t.Helper()
	for attempt := 0; attempt < guard.config.HealthyPollsToOpen; attempt++ {
		_ = guard.Refresh(context.Background())
	}
	if status := guard.Status(); !status.Ready {
		t.Fatalf("failed to open test guard: %+v", status)
	}
}

func validOffset(now time.Time, next int64) ProjectionOffset {
	return ProjectionOffset{NextOffset: next, UpdatedAtMs: now.UnixMilli()}
}

func cloneBounds(bounds map[int32]PartitionBounds) map[int32]PartitionBounds {
	result := make(map[int32]PartitionBounds, len(bounds))
	for partition, bound := range bounds {
		result[partition] = bound
	}
	return result
}
