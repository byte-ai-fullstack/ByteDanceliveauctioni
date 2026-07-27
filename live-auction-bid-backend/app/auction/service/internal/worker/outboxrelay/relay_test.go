package outboxrelay

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestRelayProcessOneDrainsInflightBeforePending(t *testing.T) {
	item := runtimeOutboxItemFixture(t)
	log := newCallLog()
	queue := &queueStub{
		peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
			log.add("peek")
			return item, true, nil
		},
		takeFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
			t.Fatal("Take must not run before an existing inflight item is drained")
			return "", false, nil
		},
		ackFn: func(_ context.Context, _ data.RuntimeOutboxOwnership, _ string) (data.RuntimeOutboxAckResult, error) {
			log.add("ack")
			return data.RuntimeOutboxAckOK, nil
		},
	}
	producer := runtimeProducerStub{produceFn: func(context.Context, *v1.RuntimeFactV1, data.RuntimeOutboxOwnership) error {
		log.add("produce")
		return nil
	}}
	relay := newRelayFixture(t, queue, producer)

	processed, err := relay.processOne(context.Background(), runtimeOwnershipFixture())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !processed {
		t.Fatal("processOne reported no work")
	}
	if got, want := log.snapshot(), []string{"peek", "produce", "ack"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
}

func TestRelayProcessOneTakesOldestPendingWhenInflightIsEmpty(t *testing.T) {
	item := runtimeOutboxItemFixture(t)
	log := newCallLog()
	queue := &queueStub{
		peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
			log.add("peek")
			return "", false, nil
		},
		takeFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
			log.add("take")
			return item, true, nil
		},
		ackFn: func(context.Context, data.RuntimeOutboxOwnership, string) (data.RuntimeOutboxAckResult, error) {
			log.add("ack")
			return data.RuntimeOutboxAckOK, nil
		},
	}
	producer := runtimeProducerStub{produceFn: func(context.Context, *v1.RuntimeFactV1, data.RuntimeOutboxOwnership) error {
		log.add("produce")
		return nil
	}}
	relay := newRelayFixture(t, queue, producer)

	processed, err := relay.processOne(context.Background(), runtimeOwnershipFixture())
	if err != nil || !processed {
		t.Fatalf("processed=%v error=%v", processed, err)
	}
	if got, want := log.snapshot(), []string{"peek", "take", "produce", "ack"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
}

func TestRelayProcessOneReturnsIdleWithoutProducing(t *testing.T) {
	queue := &queueStub{}
	producer := runtimeProducerStub{produceFn: func(context.Context, *v1.RuntimeFactV1, data.RuntimeOutboxOwnership) error {
		t.Fatal("empty queue must not produce")
		return nil
	}}
	relay := newRelayFixture(t, queue, producer)

	processed, err := relay.processOne(context.Background(), runtimeOwnershipFixture())
	if err != nil || processed {
		t.Fatalf("processed=%v error=%v", processed, err)
	}
}

func TestRelayProcessOneLeavesInflightUnackedOnPoisonOrProduceFailure(t *testing.T) {
	tests := []struct {
		name       string
		item       func(*testing.T) string
		produceErr error
		want       error
	}{
		{name: "poison fact", item: func(*testing.T) string { return "not-an-event\n{}" }, want: ErrRelayPoisonFact},
		{name: "produce failure", item: runtimeOutboxItemFixture, produceErr: errors.New("Kafka timeout")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ackCalled := false
			queue := &queueStub{
				peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
					return test.item(t), true, nil
				},
				ackFn: func(context.Context, data.RuntimeOutboxOwnership, string) (data.RuntimeOutboxAckResult, error) {
					ackCalled = true
					return data.RuntimeOutboxAckOK, nil
				},
			}
			producer := runtimeProducerStub{produceFn: func(context.Context, *v1.RuntimeFactV1, data.RuntimeOutboxOwnership) error {
				return test.produceErr
			}}
			relay := newRelayFixture(t, queue, producer)

			processed, err := relay.processOne(context.Background(), runtimeOwnershipFixture())
			if processed || err == nil {
				t.Fatalf("processed=%v error=%v", processed, err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if ackCalled {
				t.Fatal("failed item was ACKed")
			}
		})
	}
}

func TestRelayProcessOneClassifiesFencedAndCorruptAckResults(t *testing.T) {
	tests := []struct {
		result data.RuntimeOutboxAckResult
		want   error
	}{
		{result: data.RuntimeOutboxAckNotOwner, want: ErrRelayOwnershipLost},
		{result: data.RuntimeOutboxAckEmpty, want: ErrRelayQueueInvariant},
		{result: data.RuntimeOutboxAckMalformed, want: ErrRelayQueueInvariant},
		{result: data.RuntimeOutboxAckMismatch, want: ErrRelayQueueInvariant},
		{result: data.RuntimeOutboxAckResult("UNKNOWN"), want: ErrRelayQueueInvariant},
	}
	for _, test := range tests {
		t.Run(string(test.result), func(t *testing.T) {
			queue := &queueStub{
				peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
					return runtimeOutboxItemFixture(t), true, nil
				},
				ackFn: func(context.Context, data.RuntimeOutboxOwnership, string) (data.RuntimeOutboxAckResult, error) {
					return test.result, nil
				},
			}
			relay := newRelayFixture(t, queue, runtimeProducerStub{})
			if _, err := relay.processOne(context.Background(), runtimeOwnershipFixture()); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestRelayProcessOneClassifiesQueueErrors(t *testing.T) {
	tests := []struct {
		name  string
		queue *queueStub
		want  error
	}{
		{
			name: "peek fenced",
			queue: &queueStub{peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
				return "", false, data.ErrRuntimeOutboxNotOwner
			}},
			want: ErrRelayOwnershipLost,
		},
		{
			name: "take sees occupied inflight",
			queue: &queueStub{takeFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
				return "", false, data.ErrRuntimeOutboxInflightNotEmpty
			}},
			want: ErrRelayQueueInvariant,
		},
		{
			name: "ack fenced",
			queue: &queueStub{
				peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
					return runtimeOutboxItemFixture(t), true, nil
				},
				ackFn: func(context.Context, data.RuntimeOutboxOwnership, string) (data.RuntimeOutboxAckResult, error) {
					return "", data.ErrRuntimeOutboxNotOwner
				},
			},
			want: ErrRelayOwnershipLost,
		},
		{
			name: "transient Redis error",
			queue: &queueStub{peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
				return "", false, errors.New("Redis timeout")
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relay := newRelayFixture(t, test.queue, runtimeProducerStub{})
			_, err := relay.processOne(context.Background(), runtimeOwnershipFixture())
			if err == nil {
				t.Fatal("processOne returned no error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestRelayRunRenewsBlockedProduceAndFinishesItDuringShutdown(t *testing.T) {
	item := runtimeOutboxItemFixture(t)
	producerStarted := make(chan struct{})
	finishProduce := make(chan struct{})
	renewed := make(chan struct{}, 1)
	acked := make(chan struct{}, 1)
	released := make(chan struct{}, 1)
	var takeCount atomic.Int32
	ownership := data.RuntimeOutboxOwnership{Shard: 0, InstanceID: "relay-1", Epoch: 1, OwnerToken: "relay-1:1", TTL: 90 * time.Millisecond}
	queue := &queueStub{
		acquireFn: func(context.Context, int, string, time.Duration) (data.RuntimeOutboxOwnership, bool, error) {
			return ownership, true, nil
		},
		renewFn: func(context.Context, data.RuntimeOutboxOwnership) (bool, error) {
			select {
			case renewed <- struct{}{}:
			default:
			}
			return true, nil
		},
		releaseFn: func(context.Context, data.RuntimeOutboxOwnership) (bool, error) {
			released <- struct{}{}
			return true, nil
		},
		takeFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
			if takeCount.Add(1) == 1 {
				return item, true, nil
			}
			return "", false, nil
		},
		ackFn: func(context.Context, data.RuntimeOutboxOwnership, string) (data.RuntimeOutboxAckResult, error) {
			acked <- struct{}{}
			return data.RuntimeOutboxAckOK, nil
		},
	}
	producer := runtimeProducerStub{produceFn: func(ctx context.Context, _ *v1.RuntimeFactV1, _ data.RuntimeOutboxOwnership) error {
		close(producerStarted)
		select {
		case <-finishProduce:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	relay := &Relay{queue: queue, producer: producer, config: Config{
		InstanceID:       "relay-1",
		ShardCount:       1,
		LeaseTTL:         ownership.TTL,
		RenewInterval:    10 * time.Millisecond,
		OperationTimeout: 500 * time.Millisecond,
		ReleaseTimeout:   100 * time.Millisecond,
		IdleMin:          time.Millisecond,
		IdleMax:          2 * time.Millisecond,
		RetryMin:         time.Millisecond,
		RetryMax:         2 * time.Millisecond,
	}, nextMetrics: make([]time.Time, 1)}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run(ctx) }()
	waitSignal(t, producerStarted, "producer start")
	waitSignal(t, renewed, "lease renewal while producer is blocked")
	cancel()
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before the in-flight produce completed: %v", err)
	default:
	}
	close(finishProduce)
	waitSignal(t, acked, "ACK after graceful produce completion")
	waitSignal(t, released, "fenced ownership release")
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after graceful shutdown")
	}
}

func TestRelayRunRetriesAcquireAndReacquiresAfterFencing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var acquireCount atomic.Int32
	var releaseCount atomic.Int32
	queue := &queueStub{
		acquireFn: func(context.Context, int, string, time.Duration) (data.RuntimeOutboxOwnership, bool, error) {
			switch attempt := acquireCount.Add(1); attempt {
			case 1:
				return data.RuntimeOutboxOwnership{}, false, errors.New("temporary Redis failure")
			case 2:
				return data.RuntimeOutboxOwnership{}, false, nil
			case 3:
				return data.RuntimeOutboxOwnership{Shard: 0, InstanceID: "relay-1", Epoch: 1, OwnerToken: "relay-1:1", TTL: 90 * time.Millisecond}, true, nil
			default:
				return data.RuntimeOutboxOwnership{Shard: 0, InstanceID: "relay-1", Epoch: 2, OwnerToken: "relay-1:2", TTL: 90 * time.Millisecond}, true, nil
			}
		},
		peekFn: func(_ context.Context, ownership data.RuntimeOutboxOwnership) (string, bool, error) {
			if ownership.Epoch == 1 {
				return "", false, data.ErrRuntimeOutboxNotOwner
			}
			cancel()
			return "", false, nil
		},
		releaseFn: func(context.Context, data.RuntimeOutboxOwnership) (bool, error) {
			releaseCount.Add(1)
			return true, nil
		},
	}
	relay := relayWithFastConfig(t, queue, runtimeProducerStub{})
	if err := relay.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := acquireCount.Load(); got < 4 {
		t.Fatalf("acquire count=%d want at least 4", got)
	}
	if got := releaseCount.Load(); got != 2 {
		t.Fatalf("release count=%d want=2", got)
	}
}

func TestRelayRunStopsOnQueueInvariant(t *testing.T) {
	queue := &queueStub{
		acquireFn: func(context.Context, int, string, time.Duration) (data.RuntimeOutboxOwnership, bool, error) {
			return data.RuntimeOutboxOwnership{Shard: 0, InstanceID: "relay-1", Epoch: 1, OwnerToken: "relay-1:1", TTL: 90 * time.Millisecond}, true, nil
		},
		peekFn: func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error) {
			return "", false, data.ErrRuntimeOutboxInflightNotEmpty
		},
	}
	relay := relayWithFastConfig(t, queue, runtimeProducerStub{})
	if err := relay.Run(context.Background()); !errors.Is(err, ErrRelayQueueInvariant) {
		t.Fatalf("error=%v want ErrRelayQueueInvariant", err)
	}
}

func TestRelayRecordQueueMetricsSamplesValidAndMalformedOldestFacts(t *testing.T) {
	validItem := runtimeOutboxItemFixture(t)
	statsCalls := 0
	queue := &queueMetricsStub{
		queueStub: &queueStub{},
		statsFn: func(ctx context.Context, shard int) (data.RuntimeOutboxStats, error) {
			statsCalls++
			if err := ctx.Err(); err != nil {
				t.Fatalf("Stats context already cancelled: %v", err)
			}
			if shard != 0 {
				t.Fatalf("Stats shard=%d want=0", shard)
			}
			switch statsCalls {
			case 1:
				return data.RuntimeOutboxStats{Pending: 3, Inflight: 1, OldestItem: validItem}, nil
			case 2:
				return data.RuntimeOutboxStats{Pending: 2, OldestItem: "malformed"}, nil
			default:
				return data.RuntimeOutboxStats{}, errors.New("stats unavailable")
			}
		},
	}
	relay := newRelayFixture(t, queue, runtimeProducerStub{})

	relay.recordQueueMetrics(context.Background(), -1)
	if statsCalls != 0 {
		t.Fatalf("invalid shard sampled stats %d times", statsCalls)
	}
	relay.recordQueueMetrics(context.Background(), 0)
	if statsCalls != 1 {
		t.Fatalf("valid sample calls=%d want=1", statsCalls)
	}
	relay.recordQueueMetrics(context.Background(), 0)
	if statsCalls != 1 {
		t.Fatalf("throttled sample calls=%d want=1", statsCalls)
	}

	relay.nextMetrics[0] = time.Time{}
	relay.recordQueueMetrics(context.Background(), 0)
	relay.nextMetrics[0] = time.Time{}
	relay.recordQueueMetrics(context.Background(), 0)
	if statsCalls != 3 {
		t.Fatalf("sample calls=%d want=3", statsCalls)
	}
}

func TestRelayRecordQueueMetricsIgnoresQueuesWithoutStats(t *testing.T) {
	relay := newRelayFixture(t, &queueStub{}, runtimeProducerStub{})
	relay.recordQueueMetrics(context.Background(), 0)
}

func TestRelayWaitAndJitterRespectBoundsAndCancellation(t *testing.T) {
	relay := &Relay{}
	if err := relay.wait(context.Background(), nil, time.Nanosecond); err != nil {
		t.Fatalf("timer wait: %v", err)
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	cancelRoot()
	if err := relay.wait(rootCtx, nil, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("root cancellation error=%v", err)
	}

	leaseCtx, cancelLease := context.WithCancelCause(context.Background())
	cancelLease(ErrRelayOwnershipLost)
	if err := relay.wait(context.Background(), leaseCtx, time.Second); !errors.Is(err, ErrRelayOwnershipLost) {
		t.Fatalf("lease cancellation error=%v", err)
	}

	for index := 0; index < 100; index++ {
		if got := boundedJitter(5*time.Millisecond, 10*time.Millisecond); got < 5*time.Millisecond || got > 10*time.Millisecond {
			t.Fatalf("bounded jitter=%s", got)
		}
		if got := fullJitter(5*time.Millisecond, 20*time.Millisecond, 8); got < 0 || got > 20*time.Millisecond {
			t.Fatalf("full jitter=%s", got)
		}
	}
	if got := boundedJitter(time.Millisecond, time.Millisecond); got != time.Millisecond {
		t.Fatalf("fixed bounded jitter=%s", got)
	}
	if got := fullJitter(time.Nanosecond, time.Nanosecond, 1); got != time.Nanosecond {
		t.Fatalf("fixed full jitter=%s", got)
	}
	if got := minDuration(time.Millisecond, 2*time.Millisecond); got != time.Millisecond {
		t.Fatalf("minDuration=%s", got)
	}
}

func TestNewRelayRejectsUnsafeConfiguration(t *testing.T) {
	queue := &queueStub{}
	producer := runtimeProducerStub{}
	tests := []Config{
		{},
		{InstanceID: "bad:owner"},
		{InstanceID: "relay", ShardCount: data.RuntimeOutboxShardCount - 1},
		{InstanceID: "relay", ShardCount: data.RuntimeOutboxShardCount + 1},
		{InstanceID: "relay", LeaseTTL: 3 * time.Second, RenewInterval: 2 * time.Second},
		{InstanceID: "relay", IdleMin: 2 * time.Second, IdleMax: time.Second},
	}
	for _, cfg := range tests {
		if _, err := New(queue, producer, cfg); !errors.Is(err, ErrInvalidRelayConfig) {
			t.Fatalf("config=%+v error=%v want ErrInvalidRelayConfig", cfg, err)
		}
	}
	if _, err := New(nil, producer, Config{InstanceID: "relay"}); !errors.Is(err, ErrInvalidRelayConfig) {
		t.Fatalf("nil queue error=%v", err)
	}
	if _, err := New(queue, nil, Config{InstanceID: "relay"}); !errors.Is(err, ErrInvalidRelayConfig) {
		t.Fatalf("nil producer error=%v", err)
	}
}

type queueStub struct {
	acquireFn func(context.Context, int, string, time.Duration) (data.RuntimeOutboxOwnership, bool, error)
	renewFn   func(context.Context, data.RuntimeOutboxOwnership) (bool, error)
	releaseFn func(context.Context, data.RuntimeOutboxOwnership) (bool, error)
	peekFn    func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error)
	takeFn    func(context.Context, data.RuntimeOutboxOwnership) (string, bool, error)
	ackFn     func(context.Context, data.RuntimeOutboxOwnership, string) (data.RuntimeOutboxAckResult, error)
}

type queueMetricsStub struct {
	*queueStub
	statsFn func(context.Context, int) (data.RuntimeOutboxStats, error)
}

func (q *queueMetricsStub) Stats(ctx context.Context, shard int) (data.RuntimeOutboxStats, error) {
	if q.statsFn == nil {
		return data.RuntimeOutboxStats{}, nil
	}
	return q.statsFn(ctx, shard)
}

func (q *queueStub) Acquire(ctx context.Context, shard int, instanceID string, ttl time.Duration) (data.RuntimeOutboxOwnership, bool, error) {
	if q.acquireFn == nil {
		return data.RuntimeOutboxOwnership{}, false, nil
	}
	return q.acquireFn(ctx, shard, instanceID, ttl)
}

func (q *queueStub) Renew(ctx context.Context, ownership data.RuntimeOutboxOwnership) (bool, error) {
	if q.renewFn == nil {
		return true, nil
	}
	return q.renewFn(ctx, ownership)
}

func (q *queueStub) Release(ctx context.Context, ownership data.RuntimeOutboxOwnership) (bool, error) {
	if q.releaseFn == nil {
		return true, nil
	}
	return q.releaseFn(ctx, ownership)
}

func (q *queueStub) PeekInflight(ctx context.Context, ownership data.RuntimeOutboxOwnership) (string, bool, error) {
	if q.peekFn == nil {
		return "", false, nil
	}
	return q.peekFn(ctx, ownership)
}

func (q *queueStub) Take(ctx context.Context, ownership data.RuntimeOutboxOwnership) (string, bool, error) {
	if q.takeFn == nil {
		return "", false, nil
	}
	return q.takeFn(ctx, ownership)
}

func (q *queueStub) Ack(ctx context.Context, ownership data.RuntimeOutboxOwnership, eventID string) (data.RuntimeOutboxAckResult, error) {
	if q.ackFn == nil {
		return data.RuntimeOutboxAckOK, nil
	}
	return q.ackFn(ctx, ownership, eventID)
}

type runtimeProducerStub struct {
	produceFn func(context.Context, *v1.RuntimeFactV1, data.RuntimeOutboxOwnership) error
}

func (p runtimeProducerStub) ProduceRuntimeFact(ctx context.Context, fact *v1.RuntimeFactV1, ownership data.RuntimeOutboxOwnership) error {
	if p.produceFn == nil {
		return nil
	}
	return p.produceFn(ctx, fact, ownership)
}

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func newCallLog() *callLog { return &callLog{} }

func (l *callLog) add(call string) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func newRelayFixture(t *testing.T, queue Queue, producer RuntimeFactProducer) *Relay {
	t.Helper()
	relay, err := New(queue, producer, Config{InstanceID: "relay-1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return relay
}

func relayWithFastConfig(t *testing.T, queue Queue, producer RuntimeFactProducer) *Relay {
	t.Helper()
	return &Relay{queue: queue, producer: producer, config: Config{
		InstanceID:       "relay-1",
		ShardCount:       1,
		LeaseTTL:         90 * time.Millisecond,
		RenewInterval:    10 * time.Millisecond,
		OperationTimeout: 50 * time.Millisecond,
		ReleaseTimeout:   50 * time.Millisecond,
		IdleMin:          time.Nanosecond,
		IdleMax:          2 * time.Nanosecond,
		RetryMin:         time.Nanosecond,
		RetryMax:         2 * time.Nanosecond,
	}, nextMetrics: make([]time.Time, 1)}
}

func runtimeOutboxItemFixture(t *testing.T) string {
	t.Helper()
	item, err := eventcontract.EncodeRuntimeOutboxItem(runtimeFactFixture(t))
	if err != nil {
		t.Fatalf("EncodeRuntimeOutboxItem: %v", err)
	}
	return item
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
