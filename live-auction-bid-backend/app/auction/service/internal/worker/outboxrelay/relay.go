package outboxrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

var (
	ErrInvalidRelayConfig  = errors.New("invalid runtime outbox relay config")
	ErrRelayOwnershipLost  = errors.New("runtime outbox relay ownership lost")
	ErrRelayPoisonFact     = errors.New("runtime outbox relay poison fact")
	ErrRelayQueueInvariant = errors.New("runtime outbox relay queue invariant violated")
	ErrRelayProduce        = errors.New("runtime outbox relay Kafka produce failed")
)

type Queue interface {
	Acquire(ctx context.Context, shard int, instanceID string, ttl time.Duration) (data.RuntimeOutboxOwnership, bool, error)
	Renew(ctx context.Context, ownership data.RuntimeOutboxOwnership) (bool, error)
	Release(ctx context.Context, ownership data.RuntimeOutboxOwnership) (bool, error)
	PeekInflight(ctx context.Context, ownership data.RuntimeOutboxOwnership) (string, bool, error)
	Take(ctx context.Context, ownership data.RuntimeOutboxOwnership) (string, bool, error)
	Ack(ctx context.Context, ownership data.RuntimeOutboxOwnership, eventID string) (data.RuntimeOutboxAckResult, error)
}

type QueueMetrics interface {
	Stats(ctx context.Context, shard int) (data.RuntimeOutboxStats, error)
}

type RuntimeFactProducer interface {
	ProduceRuntimeFact(ctx context.Context, fact *v1.RuntimeFactV1, ownership data.RuntimeOutboxOwnership) error
}

type Config struct {
	InstanceID       string
	ShardCount       int
	LeaseTTL         time.Duration
	RenewInterval    time.Duration
	OperationTimeout time.Duration
	ReleaseTimeout   time.Duration
	IdleMin          time.Duration
	IdleMax          time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
}

// Relay serially moves each fenced Redis outbox shard into Kafka.
type Relay struct {
	queue       Queue
	producer    RuntimeFactProducer
	config      Config
	nextMetrics []time.Time
}

// New validates dependencies and applies production-safe bounded defaults.
func New(queue Queue, producer RuntimeFactProducer, cfg Config) (*Relay, error) {
	if queue == nil {
		return nil, fmt.Errorf("%w: queue is required", ErrInvalidRelayConfig)
	}
	if producer == nil {
		return nil, fmt.Errorf("%w: producer is required", ErrInvalidRelayConfig)
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Relay{queue: queue, producer: producer, config: normalized, nextMetrics: make([]time.Time, normalized.ShardCount)}, nil
}

// Run owns one goroutine per fixed outbox shard and blocks until shutdown or a non-recoverable invariant failure.
func (r *Relay) Run(ctx context.Context) error {
	if r == nil || r.queue == nil || r.producer == nil {
		return errors.New("runtime outbox relay is not initialized")
	}
	group, groupCtx := errgroup.WithContext(ctx)
	for shard := 0; shard < r.config.ShardCount; shard++ {
		shard := shard
		group.Go(func() error {
			return r.runShard(groupCtx, shard)
		})
	}
	err := group.Wait()
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

func (r *Relay) runShard(ctx context.Context, shard int) error {
	retryAttempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		acquireCtx, cancel := context.WithTimeout(ctx, r.config.OperationTimeout)
		ownership, acquired, err := r.queue.Acquire(acquireCtx, shard, r.config.InstanceID, r.config.LeaseTTL)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, data.ErrRuntimeOutboxInvalidArgument) {
				return fmt.Errorf("acquire outbox shard %d: %w", shard, err)
			}
			retryAttempt++
			if err := r.wait(ctx, nil, fullJitter(r.config.RetryMin, r.config.RetryMax, retryAttempt)); err != nil {
				return nil
			}
			continue
		}
		retryAttempt = 0
		if !acquired {
			if err := r.wait(ctx, nil, boundedJitter(r.config.IdleMin, r.config.IdleMax)); err != nil {
				return nil
			}
			continue
		}
		if ownership.Shard != shard || ownership.InstanceID != r.config.InstanceID || validateOwnership(ownership) != nil {
			return fmt.Errorf("%w: acquire shard %d returned invalid ownership", ErrRelayQueueInvariant, shard)
		}
		observability.SetRuntimeOutboxOwner(shard, true)
		observability.RecordRuntimeOutboxOwnerChange(shard)

		runErr := r.runOwnedShard(ctx, ownership)
		r.releaseOwnership(ctx, ownership)
		observability.SetRuntimeOutboxOwner(shard, false)
		if runErr == nil {
			return nil
		}
		if errors.Is(runErr, ErrRelayPoisonFact) || errors.Is(runErr, ErrRelayQueueInvariant) {
			return runErr
		}
		if !errors.Is(runErr, ErrRelayOwnershipLost) {
			slog.Warn("runtime outbox relay shard released after transient failure", "shard", shard, "error", runErr)
		}
		if err := r.wait(ctx, nil, boundedJitter(r.config.IdleMin, r.config.IdleMax)); err != nil {
			return nil
		}
	}
}

func (r *Relay) runOwnedShard(rootCtx context.Context, ownership data.RuntimeOutboxOwnership) error {
	leaseCtx, cancelLease := context.WithCancelCause(context.WithoutCancel(rootCtx))
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		r.renewOwnership(leaseCtx, cancelLease, ownership)
	}()
	defer func() {
		cancelLease(context.Canceled)
		<-renewDone
	}()

	retryAttempt := 0
	for {
		if rootCtx.Err() != nil {
			return nil
		}
		if cause := context.Cause(leaseCtx); cause != nil {
			return cause
		}

		r.recordQueueMetrics(leaseCtx, ownership.Shard)
		operationCtx, cancelOperation := context.WithTimeout(leaseCtx, r.config.OperationTimeout)
		processed, err := r.processOne(operationCtx, ownership)
		cancelOperation()
		if cause := context.Cause(leaseCtx); cause != nil {
			return cause
		}
		if err != nil {
			if errors.Is(err, ErrRelayOwnershipLost) || errors.Is(err, ErrRelayPoisonFact) || errors.Is(err, ErrRelayQueueInvariant) {
				return err
			}
			retryAttempt++
			if errors.Is(err, ErrRelayProduce) {
				observability.RecordRuntimeOutboxProduceRetry(ownership.Shard)
			}
			if err := r.wait(rootCtx, leaseCtx, fullJitter(r.config.RetryMin, r.config.RetryMax, retryAttempt)); err != nil {
				if cause := context.Cause(leaseCtx); cause != nil {
					return cause
				}
				return nil
			}
			continue
		}
		retryAttempt = 0
		if processed {
			continue
		}
		if err := r.wait(rootCtx, leaseCtx, boundedJitter(r.config.IdleMin, r.config.IdleMax)); err != nil {
			if cause := context.Cause(leaseCtx); cause != nil {
				return cause
			}
			return nil
		}
	}
}

func (r *Relay) recordQueueMetrics(ctx context.Context, shard int) {
	metrics, ok := r.queue.(QueueMetrics)
	if !ok || shard < 0 || shard >= len(r.nextMetrics) || time.Now().Before(r.nextMetrics[shard]) {
		return
	}
	r.nextMetrics[shard] = time.Now().Add(time.Second)
	metricsCtx, cancel := context.WithTimeout(ctx, minDuration(r.config.OperationTimeout, time.Second))
	stats, err := metrics.Stats(metricsCtx, shard)
	cancel()
	if err != nil {
		slog.Debug("runtime outbox relay stats read failed", "shard", shard, "error", err)
		return
	}
	oldestAgeMs := int64(0)
	if strings.TrimSpace(stats.OldestItem) != "" {
		fact, decodeErr := eventcontract.DecodeRuntimeOutboxItem(stats.OldestItem)
		if decodeErr != nil {
			slog.Debug("runtime outbox relay oldest fact decode failed", "shard", shard, "error", decodeErr)
		} else if fact.GetOccurredAtUnixMs() > 0 {
			oldestAgeMs = max(int64(0), time.Now().UnixMilli()-fact.GetOccurredAtUnixMs())
		}
	}
	observability.SetRuntimeOutboxQueueStats(shard, stats.Pending, stats.Inflight, oldestAgeMs)
}

func (r *Relay) processOne(ctx context.Context, ownership data.RuntimeOutboxOwnership) (bool, error) {
	item, found, err := r.queue.PeekInflight(ctx, ownership)
	if err != nil {
		return false, classifyQueueError("peek inflight", ownership.Shard, err)
	}
	if !found {
		item, found, err = r.queue.Take(ctx, ownership)
		if err != nil {
			return false, classifyQueueError("take pending", ownership.Shard, err)
		}
		if !found {
			return false, nil
		}
	}

	fact, err := eventcontract.DecodeRuntimeOutboxItem(item)
	if err != nil {
		return false, fmt.Errorf("%w: shard %d: %v", ErrRelayPoisonFact, ownership.Shard, err)
	}
	if err := r.producer.ProduceRuntimeFact(ctx, fact, ownership); err != nil {
		return false, fmt.Errorf("%w: event %s from shard %d: %w", ErrRelayProduce, fact.GetEventId(), ownership.Shard, err)
	}
	ackResult, err := r.queue.Ack(ctx, ownership, fact.GetEventId())
	if err != nil {
		observability.RecordRuntimeOutboxAckResult("error")
		return false, classifyQueueError("ack inflight", ownership.Shard, err)
	}
	observability.RecordRuntimeOutboxAckResult(string(ackResult))
	switch ackResult {
	case data.RuntimeOutboxAckOK:
		return true, nil
	case data.RuntimeOutboxAckNotOwner:
		return false, fmt.Errorf("%w: shard %d rejected ACK for event %s", ErrRelayOwnershipLost, ownership.Shard, fact.GetEventId())
	case data.RuntimeOutboxAckEmpty, data.RuntimeOutboxAckMalformed, data.RuntimeOutboxAckMismatch:
		return false, fmt.Errorf("%w: shard %d ACK for event %s returned %s", ErrRelayQueueInvariant, ownership.Shard, fact.GetEventId(), ackResult)
	default:
		return false, fmt.Errorf("%w: shard %d ACK for event %s returned unknown result %q", ErrRelayQueueInvariant, ownership.Shard, fact.GetEventId(), ackResult)
	}
}

func (r *Relay) renewOwnership(ctx context.Context, cancel context.CancelCauseFunc, ownership data.RuntimeOutboxOwnership) {
	ticker := time.NewTicker(r.config.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewTimeout := minDuration(r.config.OperationTimeout, r.config.RenewInterval)
			renewCtx, renewCancel := context.WithTimeout(ctx, renewTimeout)
			ok, err := r.queue.Renew(renewCtx, ownership)
			renewCancel()
			if err != nil {
				cancel(fmt.Errorf("%w: renew shard %d: %v", ErrRelayOwnershipLost, ownership.Shard, err))
				return
			}
			if !ok {
				cancel(fmt.Errorf("%w: renew shard %d was fenced", ErrRelayOwnershipLost, ownership.Shard))
				return
			}
		}
	}
}

func (r *Relay) releaseOwnership(rootCtx context.Context, ownership data.RuntimeOutboxOwnership) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(rootCtx), r.config.ReleaseTimeout)
	defer cancel()
	released, err := r.queue.Release(releaseCtx, ownership)
	if err != nil {
		slog.Warn("runtime outbox relay ownership release failed", "shard", ownership.Shard, "owner_epoch", ownership.Epoch, "error", err)
		return
	}
	if !released {
		slog.Debug("runtime outbox relay ownership already fenced before release", "shard", ownership.Shard, "owner_epoch", ownership.Epoch)
	}
}

func (r *Relay) wait(rootCtx, leaseCtx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var leaseDone <-chan struct{}
	if leaseCtx != nil {
		leaseDone = leaseCtx.Done()
	}
	select {
	case <-rootCtx.Done():
		return rootCtx.Err()
	case <-leaseDone:
		return context.Cause(leaseCtx)
	case <-timer.C:
		return nil
	}
}

func classifyQueueError(operation string, shard int, err error) error {
	switch {
	case errors.Is(err, data.ErrRuntimeOutboxNotOwner):
		return fmt.Errorf("%w: %s shard %d: %v", ErrRelayOwnershipLost, operation, shard, err)
	case errors.Is(err, data.ErrRuntimeOutboxInflightNotEmpty):
		return fmt.Errorf("%w: %s shard %d: %v", ErrRelayQueueInvariant, operation, shard, err)
	default:
		return fmt.Errorf("%s shard %d: %w", operation, shard, err)
	}
}

func normalizeConfig(cfg Config) (Config, error) {
	cfg.InstanceID = strings.TrimSpace(cfg.InstanceID)
	if cfg.ShardCount == 0 {
		cfg.ShardCount = data.RuntimeOutboxShardCount
	}
	if cfg.LeaseTTL == 0 {
		cfg.LeaseTTL = 15 * time.Second
	}
	if cfg.RenewInterval == 0 {
		cfg.RenewInterval = 5 * time.Second
	}
	if cfg.OperationTimeout == 0 {
		cfg.OperationTimeout = 10 * time.Second
	}
	if cfg.ReleaseTimeout == 0 {
		cfg.ReleaseTimeout = 2 * time.Second
	}
	if cfg.IdleMin == 0 {
		cfg.IdleMin = 25 * time.Millisecond
	}
	if cfg.IdleMax == 0 {
		cfg.IdleMax = 100 * time.Millisecond
	}
	if cfg.RetryMin == 0 {
		cfg.RetryMin = 50 * time.Millisecond
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 5 * time.Second
	}

	if cfg.InstanceID == "" || strings.ContainsAny(cfg.InstanceID, ":\r\n") {
		return Config{}, fmt.Errorf("%w: instance_id is required and cannot contain colon or line breaks", ErrInvalidRelayConfig)
	}
	if cfg.ShardCount != data.RuntimeOutboxShardCount {
		return Config{}, fmt.Errorf("%w: shard_count must remain fixed at %d", ErrInvalidRelayConfig, data.RuntimeOutboxShardCount)
	}
	if cfg.LeaseTTL <= 0 || cfg.RenewInterval <= 0 || cfg.RenewInterval > cfg.LeaseTTL/3 {
		return Config{}, fmt.Errorf("%w: renew_interval must be positive and no greater than one third of lease_ttl", ErrInvalidRelayConfig)
	}
	if cfg.OperationTimeout <= 0 || cfg.ReleaseTimeout <= 0 {
		return Config{}, fmt.Errorf("%w: operation and release timeouts must be positive", ErrInvalidRelayConfig)
	}
	if cfg.IdleMin <= 0 || cfg.IdleMax < cfg.IdleMin || cfg.RetryMin <= 0 || cfg.RetryMax < cfg.RetryMin {
		return Config{}, fmt.Errorf("%w: idle and retry bounds must be positive ordered ranges", ErrInvalidRelayConfig)
	}
	return cfg, nil
}

func boundedJitter(minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	return minimum + time.Duration(rand.Int63n(int64(maximum-minimum)+1))
}

func fullJitter(minimum, maximum time.Duration, attempt int) time.Duration {
	cap := minimum
	for step := 1; step < attempt && cap < maximum; step++ {
		if cap > maximum/2 {
			cap = maximum
			break
		}
		cap *= 2
	}
	if cap > maximum {
		cap = maximum
	}
	if cap <= 1 {
		return cap
	}
	return time.Duration(rand.Int63n(int64(cap) + 1))
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
