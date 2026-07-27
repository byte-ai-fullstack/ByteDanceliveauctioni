package domainrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

var (
	ErrInvalidRelayConfig = errors.New("invalid domain relay config")
	ErrRetryScheduled     = errors.New("domain relay retry scheduled")
)

type Config struct {
	InstanceID       string
	ClaimLimit       int
	Concurrency      int
	LeaseTTL         time.Duration
	OperationTimeout time.Duration
	IdleInterval     time.Duration
	RetryBase        time.Duration
	RetryMax         time.Duration
	StatsInterval    time.Duration
	MaxAttempts      int
}

type OrderReadyPublisher interface {
	Publish(ctx context.Context, event *v1.AuctionEvent) error
}

type Option func(*Relay) error

func WithOrderReadyPublisher(publisher OrderReadyPublisher) Option {
	return func(relay *Relay) error {
		if publisher == nil {
			return fmt.Errorf("%w: order READY publisher is required", ErrInvalidRelayConfig)
		}
		relay.orderReadyPublisher = publisher
		return nil
	}
}

// Relay moves committed MySQL domain outbox messages to Kafka with at-least-once delivery.
type Relay struct {
	store               Store
	producer            Producer
	config              Config
	ready               atomic.Bool
	now                 func() time.Time
	wait                func(context.Context, time.Duration) error
	orderReadyPublisher OrderReadyPublisher
}

// New validates dependencies and applies bounded defaults that keep every claimed row actively processing.
func New(store Store, producer Producer, cfg Config, options ...Option) (*Relay, error) {
	if store == nil || producer == nil {
		return nil, fmt.Errorf("%w: store and producer are required", ErrInvalidRelayConfig)
	}
	normalized, err := normalizeRelayConfig(cfg)
	if err != nil {
		return nil, err
	}
	relay := &Relay{store: store, producer: producer, config: normalized, now: time.Now, wait: waitContext}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidRelayConfig)
		}
		if err := option(relay); err != nil {
			return nil, err
		}
	}
	return relay, nil
}

// Ready reports whether the last claim/process cycle reached both dependencies successfully.
func (relay *Relay) Ready() bool {
	return relay != nil && relay.ready.Load()
}

// Run claims short batches forever and lets expired leases recover any interrupted work.
func (relay *Relay) Run(ctx context.Context) error {
	if relay == nil || relay.store == nil || relay.producer == nil || relay.now == nil || relay.wait == nil {
		return errors.New("domain relay is not initialized")
	}
	defer relay.ready.Store(false)
	claimFailures := 0
	lastStats := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		now := relay.now()
		if lastStats.IsZero() || now.Sub(lastStats) >= relay.config.StatsInterval {
			relay.refreshStats(ctx, now)
			lastStats = now
		}
		claimCtx, cancel := context.WithTimeout(ctx, relay.config.OperationTimeout)
		messages, err := relay.store.Claim(claimCtx, relay.config.InstanceID, now, relay.config.ClaimLimit, relay.config.LeaseTTL)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			relay.ready.Store(false)
			claimFailures++
			observability.RecordDomainRelayResult("claim_failed", 0)
			slog.Warn("domain relay claim failed", "attempt", claimFailures, "error", err)
			if err := relay.wait(ctx, fullJitter(relay.config.RetryBase, relay.config.RetryMax, claimFailures)); err != nil {
				return nil
			}
			continue
		}
		claimFailures = 0
		if len(messages) == 0 {
			relay.ready.Store(true)
			if err := relay.wait(ctx, relay.config.IdleInterval); err != nil {
				return nil
			}
			continue
		}
		failures := relay.processBatch(ctx, messages)
		relay.ready.Store(failures == 0)
	}
}

func (relay *Relay) processBatch(ctx context.Context, messages []Message) int {
	type route struct {
		topic        string
		partitionKey string
	}
	grouped := make(map[route][]Message, len(messages))
	for _, message := range messages {
		key := route{topic: message.Topic, partitionKey: message.PartitionKey}
		grouped[key] = append(grouped[key], message)
	}
	for key := range grouped {
		sort.Slice(grouped[key], func(left, right int) bool {
			return grouped[key][left].ID < grouped[key][right].ID
		})
	}

	var failures atomic.Int64
	var group sync.WaitGroup
	concurrency := make(chan struct{}, relay.config.Concurrency)
	group.Add(len(grouped))
	for _, routeMessages := range grouped {
		routeMessages := routeMessages
		go func() {
			defer group.Done()
			select {
			case concurrency <- struct{}{}:
				defer func() { <-concurrency }()
			case <-ctx.Done():
				failures.Add(int64(len(routeMessages)))
				return
			}
			for index, message := range routeMessages {
				if err := relay.processOne(ctx, message); err != nil {
					blocked := len(routeMessages) - index - 1
					failures.Add(int64(blocked + 1))
					slog.Warn("domain relay message remains unpublished",
						"outbox_id", message.ID,
						"message_id", safeHeaderValue(message.MessageID, "invalid"),
						"attempts", message.Attempts,
						"same_route_followers_blocked", blocked,
						"error", err,
					)
					return
				}
			}
		}()
	}
	group.Wait()
	return int(failures.Load())
}

func (relay *Relay) processOne(ctx context.Context, message Message) error {
	started := relay.now()
	if message.Attempts >= relay.config.MaxAttempts {
		failure := message.LastError
		if strings.TrimSpace(failure) == "" {
			failure = "maximum publish attempts reached"
		}
		return relay.deadLetter(ctx, message, message.Attempts+1, failure, started)
	}

	produceCtx, cancelProduce := context.WithTimeout(ctx, relay.config.OperationTimeout)
	err := relay.producer.ProduceDomain(produceCtx, message)
	cancelProduce()
	if err == nil {
		markCtx, cancelMark := context.WithTimeout(ctx, relay.config.OperationTimeout)
		err = relay.store.MarkPublished(markCtx, message, relay.now())
		cancelMark()
		if err != nil {
			observability.RecordDomainRelayResult("mark_failed", relay.now().Sub(started))
			return fmt.Errorf("mark Kafka-acknowledged domain message published: %w", err)
		}
		observability.RecordDomainRelayResult("published", relay.now().Sub(started))
		relay.notifyOrderReady(ctx, message)
		return nil
	}

	failure := sanitizeFailure(err.Error())
	attempts := message.Attempts + 1
	if errors.Is(err, ErrInvalidDomainMessage) || attempts >= relay.config.MaxAttempts {
		return relay.deadLetter(ctx, message, attempts, failure, started)
	}
	nextAttempt := relay.now().Add(fullJitter(relay.config.RetryBase, relay.config.RetryMax, attempts))
	markCtx, cancelMark := context.WithTimeout(ctx, relay.config.OperationTimeout)
	markErr := relay.store.MarkFailed(markCtx, message, relay.now(), nextAttempt, attempts, failure)
	cancelMark()
	if markErr != nil {
		observability.RecordDomainRelayResult("retry_mark_failed", relay.now().Sub(started))
		return fmt.Errorf("record domain publish failure: %w", markErr)
	}
	observability.RecordDomainRelayResult("retry_scheduled", relay.now().Sub(started))
	return fmt.Errorf("%w after publish failure: %s", ErrRetryScheduled, failure)
}

func (relay *Relay) notifyOrderReady(ctx context.Context, message Message) {
	if relay.orderReadyPublisher == nil || message.Topic != eventcontract.OrderCreatedTopicV1 {
		return
	}
	event, err := orderReadyEvent(message, relay.now())
	if err != nil {
		slog.Error("domain relay could not derive order READY signal after publish", "outbox_id", message.ID, "error", err)
		return
	}
	publishCtx, cancel := context.WithTimeout(ctx, relay.config.OperationTimeout)
	defer cancel()
	if err := relay.orderReadyPublisher.Publish(publishCtx, event); err != nil {
		slog.Warn("domain relay order READY acceleration signal was not published", "outbox_id", message.ID, "message_id", message.MessageID, "error", err)
	}
}

func orderReadyEvent(message Message, publishedAt time.Time) (*v1.AuctionEvent, error) {
	_, decoded, err := validateDomainMessage(message)
	if err != nil {
		return nil, err
	}
	created, ok := decoded.(*v1.OrderCreatedDomainEventV1)
	if !ok || publishedAt.UnixMilli() <= 0 {
		return nil, fmt.Errorf("%w: order READY source is invalid", ErrInvalidDomainMessage)
	}
	return &v1.AuctionEvent{
		Id:               created.GetMetadata().GetMessageId(),
		Type:             v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED,
		RoomId:           created.GetRoomId(),
		LotId:            created.GetLotId(),
		OccurredAtUnixMs: publishedAt.UnixMilli(),
		BuyerUserId:      created.GetBuyerUserId(),
		OrderId:          created.GetOrderId(),
		OrderVisibility:  v1.OrderVisibility_ORDER_VISIBILITY_READY,
		LotVersion:       created.GetLotVersion(),
	}, nil
}

func (relay *Relay) deadLetter(ctx context.Context, message Message, attempts int, failure string, started time.Time) error {
	failedAt := relay.now()
	produceCtx, cancelProduce := context.WithTimeout(ctx, relay.config.OperationTimeout)
	err := relay.producer.ProduceDeadLetter(produceCtx, message, attempts, failure, failedAt)
	cancelProduce()
	if err != nil {
		nextAttempt := relay.now().Add(fullJitter(relay.config.RetryBase, relay.config.RetryMax, attempts))
		markCtx, cancelMark := context.WithTimeout(ctx, relay.config.OperationTimeout)
		markErr := relay.store.MarkFailed(markCtx, message, relay.now(), nextAttempt, attempts, failure)
		cancelMark()
		if markErr != nil {
			observability.RecordDomainRelayResult("dlq_retry_mark_failed", relay.now().Sub(started))
			return fmt.Errorf("produce dead letter: %v; record retry: %w", err, markErr)
		}
		observability.RecordDomainRelayResult("dlq_retry_scheduled", relay.now().Sub(started))
		return fmt.Errorf("%w after dead-letter publish failure: %v", ErrRetryScheduled, err)
	}
	markCtx, cancelMark := context.WithTimeout(ctx, relay.config.OperationTimeout)
	err = relay.store.MarkDeadLettered(markCtx, message, relay.now(), attempts, failure)
	cancelMark()
	if err != nil {
		observability.RecordDomainRelayResult("dlq_mark_failed", relay.now().Sub(started))
		return fmt.Errorf("mark Kafka-acknowledged dead letter: %w", err)
	}
	observability.RecordDomainRelayResult("dead_lettered", relay.now().Sub(started))
	return nil
}

func (relay *Relay) refreshStats(ctx context.Context, now time.Time) {
	statsCtx, cancel := context.WithTimeout(ctx, relay.config.OperationTimeout)
	stats, err := relay.store.Stats(statsCtx, now)
	cancel()
	if err != nil {
		slog.Warn("domain relay backlog metrics refresh failed", "error", err)
		return
	}
	observability.SetDomainOutboxBacklog(stats.Pending, stats.OldestAgeMs)
	observability.SetOrderVisibilityLag(stats.OrderVisibilityLagMs)
}

func normalizeRelayConfig(config Config) (Config, error) {
	config.InstanceID = strings.TrimSpace(config.InstanceID)
	if config.ClaimLimit == 0 {
		config.ClaimLimit = 16
	}
	if config.Concurrency == 0 {
		config.Concurrency = 16
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 30 * time.Second
	}
	if config.OperationTimeout == 0 {
		config.OperationTimeout = 5 * time.Second
	}
	if config.IdleInterval == 0 {
		config.IdleInterval = 250 * time.Millisecond
	}
	if config.RetryBase == 0 {
		config.RetryBase = 100 * time.Millisecond
	}
	if config.RetryMax == 0 {
		config.RetryMax = 10 * time.Second
	}
	if config.StatsInterval == 0 {
		config.StatsInterval = 5 * time.Second
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 8
	}
	if !validBoundedText(config.InstanceID, maxInstanceIDBytes) || strings.Contains(config.InstanceID, ":") ||
		config.ClaimLimit <= 0 || config.ClaimLimit > maxClaimLimit || config.Concurrency <= 0 || config.Concurrency > maxClaimLimit || config.ClaimLimit > config.Concurrency ||
		config.OperationTimeout < time.Millisecond || config.LeaseTTL < 4*config.OperationTimeout || config.IdleInterval < time.Millisecond || config.RetryBase < time.Millisecond || config.RetryMax < config.RetryBase || config.StatsInterval < time.Millisecond ||
		config.MaxAttempts <= 0 || config.MaxAttempts > 100 {
		return Config{}, fmt.Errorf("%w: invalid instance, batch, lease, timeout, retry, or attempt bounds", ErrInvalidRelayConfig)
	}
	return config, nil
}

func fullJitter(base, maximum time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for index := 1; index < attempt && delay < maximum; index++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	if delay <= 1 {
		return delay
	}
	return time.Duration(rand.Int63n(int64(delay))) + 1
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
