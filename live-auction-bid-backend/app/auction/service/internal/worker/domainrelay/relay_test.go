package domainrelay

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestRelayProcessesOriginalACKBeforeMarkingPublished(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{}
	relay := relayFixture(t, store, producer)
	message := relayClaimedMessage(t, 0)

	if err := relay.processOne(context.Background(), message); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if producer.domainCalls != 1 || producer.deadLetterCalls != 0 || store.published != 1 || store.failed != 0 || store.deadLettered != 0 {
		t.Fatalf("producer=%+v store=%+v", producer, store)
	}
}

func TestRelaySchedulesTransientFailureWithoutMarkingPublished(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{domainErr: errors.New("broker unavailable")}
	relay := relayFixture(t, store, producer)
	message := relayClaimedMessage(t, 2)

	if err := relay.processOne(context.Background(), message); !errors.Is(err, ErrRetryScheduled) {
		t.Fatalf("processOne error=%v", err)
	}
	if store.failed != 1 || store.failedAttempts != 3 || store.published != 0 || producer.deadLetterCalls != 0 || store.nextAttempt.UnixMilli() <= relay.now().UnixMilli() {
		t.Fatalf("producer=%+v store=%+v", producer, store)
	}
}

func TestRelayDeadLettersPermanentAndExhaustedMessages(t *testing.T) {
	tests := []struct {
		name       string
		attempts   int
		domainErr  error
		wantDomain int
	}{
		{"poison", 0, fmtInvalidDomain(), 1},
		{"last transient attempt", 7, errors.New("broker unavailable"), 1},
		{"already exhausted", 8, nil, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &relayStoreStub{}
			producer := &relayProducerStub{domainErr: test.domainErr}
			relay := relayFixture(t, store, producer)
			message := relayClaimedMessage(t, test.attempts)
			message.LastError = "previous failure"
			if err := relay.processOne(context.Background(), message); err != nil {
				t.Fatalf("processOne: %v", err)
			}
			if producer.domainCalls != test.wantDomain || producer.deadLetterCalls != 1 || store.deadLettered != 1 || store.published != 0 {
				t.Fatalf("producer=%+v store=%+v", producer, store)
			}
		})
	}
}

func TestRelayRetriesWhenDeadLetterPublishFails(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{domainErr: fmtInvalidDomain(), deadLetterErr: errors.New("DLQ unavailable")}
	relay := relayFixture(t, store, producer)
	message := relayClaimedMessage(t, 1)

	if err := relay.processOne(context.Background(), message); !errors.Is(err, ErrRetryScheduled) {
		t.Fatalf("processOne error=%v", err)
	}
	if producer.deadLetterCalls != 1 || store.failed != 1 || store.failedAttempts != 2 || store.deadLettered != 0 {
		t.Fatalf("producer=%+v store=%+v", producer, store)
	}
}

func TestRelayDoesNotReleaseClaimWhenKafkaACKMarkFails(t *testing.T) {
	store := &relayStoreStub{publishedErr: errors.New("database unavailable")}
	producer := &relayProducerStub{}
	relay := relayFixture(t, store, producer)

	if err := relay.processOne(context.Background(), relayClaimedMessage(t, 0)); err == nil {
		t.Fatal("processOne returned no error")
	}
	if producer.domainCalls != 1 || store.published != 1 || store.failed != 0 {
		t.Fatalf("producer=%+v store=%+v", producer, store)
	}
}

func TestRelayPublishesOrderReadyOnlyAfterKafkaAndMySQLAcknowledgement(t *testing.T) {
	trace := &relayTrace{}
	store := &relayStoreStub{trace: trace}
	producer := &relayProducerStub{trace: trace}
	readyPublisher := &readyPublisherStub{trace: trace}
	relay, err := New(store, producer, Config{InstanceID: "relay-1"}, WithOrderReadyPublisher(readyPublisher))
	if err != nil {
		t.Fatal(err)
	}
	relay.now = func() time.Time { return time.UnixMilli(1_700_000_001_000) }
	message := relayClaimedOrderMessage(t, 0)

	if err := relay.processOne(context.Background(), message); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if producer.domainCalls != 1 || store.published != 1 || len(readyPublisher.events) != 1 {
		t.Fatalf("producer=%+v store=%+v READY=%d", producer, store, len(readyPublisher.events))
	}
	if got := strings.Join(trace.snapshot(), ","); got != "kafka-ack,mysql-mark,nats-ready" {
		t.Fatalf("publication order=%s", got)
	}
	ready := readyPublisher.events[0]
	if ready.GetType() != v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED ||
		ready.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_READY ||
		ready.GetBuyerUserId() != "user-1" || ready.GetRoomId() != "room-1" || ready.GetLotId() != "lot-1" ||
		ready.GetOrderId() != "order-1" || ready.GetLotVersion() != 7 {
		t.Fatalf("READY event=%+v", ready)
	}
}

func TestRelayDoesNotPublishReadyWhenKafkaFails(t *testing.T) {
	readyPublisher := &readyPublisherStub{}
	relay, err := New(&relayStoreStub{}, &relayProducerStub{domainErr: errors.New("kafka unavailable")}, Config{InstanceID: "relay-1"}, WithOrderReadyPublisher(readyPublisher))
	if err != nil {
		t.Fatal(err)
	}
	relay.now = func() time.Time { return time.UnixMilli(1_700_000_001_000) }

	if err := relay.processOne(context.Background(), relayClaimedOrderMessage(t, 0)); !errors.Is(err, ErrRetryScheduled) {
		t.Fatalf("Kafka failure error=%v", err)
	}
	if len(readyPublisher.events) != 0 {
		t.Fatalf("READY published before Kafka ACK: %+v", readyPublisher.events)
	}
}

func TestRelayReadyAccelerationFailureDoesNotRepublishCommittedDomainEvent(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{}
	readyPublisher := &readyPublisherStub{err: errors.New("nats unavailable")}
	relay, err := New(store, producer, Config{InstanceID: "relay-1"}, WithOrderReadyPublisher(readyPublisher))
	if err != nil {
		t.Fatal(err)
	}
	relay.now = func() time.Time { return time.UnixMilli(1_700_000_001_000) }

	if err := relay.processOne(context.Background(), relayClaimedOrderMessage(t, 0)); err != nil {
		t.Fatalf("READY acceleration changed committed outcome: %v", err)
	}
	if producer.domainCalls != 1 || store.published != 1 || len(readyPublisher.events) != 1 {
		t.Fatalf("producer=%+v store=%+v READY=%d", producer, store, len(readyPublisher.events))
	}
}

func TestRelayDoesNotPublishReadyWhenMySQLMarkFails(t *testing.T) {
	store := &relayStoreStub{publishedErr: errors.New("mysql unavailable")}
	readyPublisher := &readyPublisherStub{}
	relay, err := New(store, &relayProducerStub{}, Config{InstanceID: "relay-1"}, WithOrderReadyPublisher(readyPublisher))
	if err != nil {
		t.Fatal(err)
	}
	relay.now = func() time.Time { return time.UnixMilli(1_700_000_001_000) }

	if err := relay.processOne(context.Background(), relayClaimedOrderMessage(t, 0)); err == nil {
		t.Fatal("mark failure returned no error")
	}
	if len(readyPublisher.events) != 0 {
		t.Fatalf("READY published before MySQL mark: %+v", readyPublisher.events)
	}
}

func TestRelayRunRecoversClaimFailureAndStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &relayStoreStub{claimErrors: []error{errors.New("mysql unavailable"), nil}}
	producer := &relayProducerStub{}
	relay := relayFixture(t, store, producer)
	waits := 0
	relay.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			cancel()
		}
		return nil
	}
	if err := relay.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if relay.Ready() || store.claims < 2 || store.statsCalls == 0 {
		t.Fatalf("ready=%t claims=%d stats=%d", relay.Ready(), store.claims, store.statsCalls)
	}
}

func TestRelayProcessBatchCountsOnlyUnpublishedMessages(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{domainErr: errors.New("broker unavailable")}
	relay := relayFixture(t, store, producer)
	messages := []Message{relayClaimedMessage(t, 0), relayClaimedMessage(t, 1)}
	messages[1].PartitionKey = "lot-other"
	if failures := relay.processBatch(context.Background(), messages); failures != 2 {
		t.Fatalf("failures=%d", failures)
	}
	if producer.domainCalls != 2 || store.failed != 2 {
		t.Fatalf("producer=%+v store=%+v", producer, store)
	}
}

func TestRelayProcessBatchPreservesRouteOrderAndStopsFollowersAfterFailure(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{domainFn: func(_ context.Context, message Message) error {
		if message.ID == 2 {
			return errors.New("broker unavailable")
		}
		return nil
	}}
	relay := relayFixture(t, store, producer)

	messages := make([]Message, 0, 3)
	for _, id := range []int64{3, 1, 2} {
		message := relayClaimedMessage(t, 0)
		message.ID = id
		messages = append(messages, message)
	}

	if failures := relay.processBatch(context.Background(), messages); failures != 2 {
		t.Fatalf("failures=%d, want failed row plus blocked follower", failures)
	}
	producer.mu.Lock()
	producedIDs := append([]int64(nil), producer.domainIDs...)
	producer.mu.Unlock()
	if len(producedIDs) != 2 || producedIDs[0] != 1 || producedIDs[1] != 2 {
		t.Fatalf("produced IDs=%v, want [1 2]", producedIDs)
	}
	if store.published != 1 || store.failed != 1 {
		t.Fatalf("store=%+v", store)
	}
}

func TestRelayConfigurationDefaultsAndValidation(t *testing.T) {
	store := &relayStoreStub{}
	producer := &relayProducerStub{}
	relay, err := New(store, producer, Config{InstanceID: "relay-1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if relay.config.ClaimLimit != 16 || relay.config.Concurrency != 16 || relay.config.MaxAttempts != 8 || relay.config.LeaseTTL != 30*time.Second {
		t.Fatalf("config=%+v", relay.config)
	}
	for _, config := range []Config{
		{},
		{InstanceID: "bad:relay"},
		{InstanceID: "relay", ClaimLimit: 2, Concurrency: 1},
		{InstanceID: "relay", LeaseTTL: time.Second, OperationTimeout: time.Second},
		{InstanceID: "relay", RetryBase: 2 * time.Second, RetryMax: time.Second},
		{InstanceID: "relay", MaxAttempts: 101},
	} {
		if _, err := New(store, producer, config); !errors.Is(err, ErrInvalidRelayConfig) {
			t.Fatalf("config=%+v error=%v", config, err)
		}
	}
	if _, err := New(nil, producer, Config{InstanceID: "relay"}); !errors.Is(err, ErrInvalidRelayConfig) {
		t.Fatalf("nil store error=%v", err)
	}
	if _, err := New(store, producer, Config{InstanceID: "relay"}, WithOrderReadyPublisher(nil)); !errors.Is(err, ErrInvalidRelayConfig) {
		t.Fatalf("nil READY publisher error=%v", err)
	}
	var nilRelay *Relay
	if nilRelay.Ready() {
		t.Fatal("nil relay reported ready")
	}
	if err := nilRelay.Run(context.Background()); err == nil {
		t.Fatal("nil relay Run returned no error")
	}
}

func TestFullJitterAndWaitRespectBoundsAndCancellation(t *testing.T) {
	for attempt := 1; attempt < 20; attempt++ {
		delay := fullJitter(time.Millisecond, 8*time.Millisecond, attempt)
		if delay <= 0 || delay > 8*time.Millisecond {
			t.Fatalf("attempt=%d delay=%s", attempt, delay)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error=%v", err)
	}
}

func relayFixture(t *testing.T, store Store, producer Producer) *Relay {
	t.Helper()
	relay, err := New(store, producer, Config{InstanceID: "relay-1"})
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.UnixMilli(1_700_000_000_000)
	relay.now = func() time.Time { return fixedNow }
	return relay
}

func relayClaimedMessage(t *testing.T, attempts int) Message {
	t.Helper()
	message := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	message.Attempts = attempts
	message.LockedBy = "relay-1"
	message.LockToken = strings.Repeat("a", 32)
	message.LockedUntilMs = time.UnixMilli(1_700_000_000_000).Add(time.Minute).UnixMilli()
	return message
}

func relayClaimedOrderMessage(t *testing.T, attempts int) Message {
	t.Helper()
	message := domainMessageFixture(t, eventcontract.OrderCreatedTopicV1)
	message.Attempts = attempts
	message.LockedBy = "relay-1"
	message.LockToken = strings.Repeat("a", 32)
	message.LockedUntilMs = time.UnixMilli(1_700_000_000_000).Add(time.Minute).UnixMilli()
	return message
}

func fmtInvalidDomain() error {
	return errors.Join(ErrInvalidDomainMessage, errors.New("invalid protobuf"))
}

type relayStoreStub struct {
	mu              sync.Mutex
	claimMessages   []Message
	claimErrors     []error
	claims          int
	published       int
	publishedErr    error
	failed          int
	failedAttempts  int
	nextAttempt     time.Time
	failedErr       error
	deadLettered    int
	deadLetteredErr error
	statsCalls      int
	trace           *relayTrace
}

func (store *relayStoreStub) Claim(context.Context, string, time.Time, int, time.Duration) ([]Message, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claims++
	if len(store.claimErrors) > 0 {
		err := store.claimErrors[0]
		store.claimErrors = store.claimErrors[1:]
		return nil, err
	}
	return append([]Message(nil), store.claimMessages...), nil
}

func (store *relayStoreStub) MarkPublished(context.Context, Message, time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.published++
	store.trace.record("mysql-mark")
	return store.publishedErr
}

func (store *relayStoreStub) MarkFailed(_ context.Context, _ Message, _ time.Time, next time.Time, attempts int, _ string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failed++
	store.failedAttempts = attempts
	store.nextAttempt = next
	return store.failedErr
}

func (store *relayStoreStub) MarkDeadLettered(context.Context, Message, time.Time, int, string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.deadLettered++
	return store.deadLetteredErr
}

func (store *relayStoreStub) Stats(context.Context, time.Time) (Stats, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.statsCalls++
	return Stats{Pending: int64(len(store.claimMessages))}, nil
}

type relayProducerStub struct {
	mu              sync.Mutex
	domainCalls     int
	domainIDs       []int64
	domainErr       error
	domainFn        func(context.Context, Message) error
	deadLetterCalls int
	deadLetterErr   error
	trace           *relayTrace
}

type readyPublisherStub struct {
	events []*v1.AuctionEvent
	err    error
	trace  *relayTrace
}

func (publisher *readyPublisherStub) Publish(_ context.Context, event *v1.AuctionEvent) error {
	publisher.events = append(publisher.events, event)
	publisher.trace.record("nats-ready")
	return publisher.err
}

func (producer *relayProducerStub) ProduceDomain(ctx context.Context, message Message) error {
	producer.mu.Lock()
	producer.domainCalls++
	producer.domainIDs = append(producer.domainIDs, message.ID)
	domainFn := producer.domainFn
	domainErr := producer.domainErr
	producer.mu.Unlock()
	if domainFn != nil {
		return domainFn(ctx, message)
	}
	if domainErr == nil {
		producer.trace.record("kafka-ack")
	}
	return domainErr
}

type relayTrace struct {
	mu    sync.Mutex
	steps []string
}

func (trace *relayTrace) record(step string) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.steps = append(trace.steps, step)
	trace.mu.Unlock()
}

func (trace *relayTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.steps...)
}

func (producer *relayProducerStub) ProduceDeadLetter(context.Context, Message, int, string, time.Time) error {
	producer.mu.Lock()
	defer producer.mu.Unlock()
	producer.deadLetterCalls++
	return producer.deadLetterErr
}
