package enrichment

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/mysqlerr"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

type recordApplier interface {
	Apply(ctx context.Context, record Record, attempt int) (ApplyResult, error)
}

type consumerClient interface {
	PollRecords(ctx context.Context, maxRecords int) kgo.Fetches
	AllowRebalance()
	CommitRecord(ctx context.Context, record *kgo.Record) error
	ProduceDeadLetter(ctx context.Context, source *kgo.Record, errorClass string, cause error, failedAt time.Time) error
}

type ConsumerConfig struct {
	MaxPollRecords   int
	RetryAttempts    int
	RetryBase        time.Duration
	RetryMax         time.Duration
	OperationTimeout time.Duration
}

// Consumer applies order-independent domain effects and manually commits only after MySQL or DLQ acknowledgement.
type Consumer struct {
	client consumerClient
	store  recordApplier
	config ConsumerConfig
	ready  atomic.Bool
	now    func() time.Time
	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

func NewConsumer(client consumerClient, store recordApplier, config ConsumerConfig) (*Consumer, error) {
	if client == nil || store == nil {
		return nil, errors.New("order enrichment Kafka client and store are required")
	}
	if config.MaxPollRecords <= 0 || config.MaxPollRecords > 10_000 || config.RetryAttempts <= 0 || config.RetryAttempts > 100 ||
		config.RetryBase <= 0 || config.RetryMax < config.RetryBase || config.OperationTimeout <= 0 {
		return nil, errors.New("order enrichment consumer limits, retries, and timeouts are invalid")
	}
	return &Consumer{
		client: client, store: store, config: config, now: time.Now, wait: waitContext,
		jitter: func(limit time.Duration) time.Duration {
			if limit <= 1 {
				return limit
			}
			return time.Duration(rand.Int63n(int64(limit))) + 1
		},
	}, nil
}

func (consumer *Consumer) Run(ctx context.Context) error {
	if consumer == nil || consumer.client == nil || consumer.store == nil {
		return errors.New("order enrichment consumer is not initialized")
	}
	// NewKafkaClient has already pinged Kafka. Mark the running loop ready before
	// the first poll because an empty topic can legitimately block PollRecords.
	consumer.ready.Store(true)
	defer consumer.ready.Store(false)
	for {
		fetches := consumer.client.PollRecords(ctx, consumer.config.MaxPollRecords)
		if ctx.Err() != nil || fetches.IsClientClosed() {
			consumer.client.AllowRebalance()
			return nil
		}
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			consumer.client.AllowRebalance()
			return fmt.Errorf("poll order enrichment Kafka records: %s[%d]: %w", fetchErrors[0].Topic, fetchErrors[0].Partition, fetchErrors[0].Err)
		}
		for _, fetch := range fetches {
			for _, topic := range fetch.Topics {
				for _, partition := range topic.Partitions {
					for _, record := range partition.Records {
						if err := consumer.processRecord(ctx, record); err != nil {
							consumer.ready.Store(false)
							consumer.client.AllowRebalance()
							return err
						}
						observability.SetOrderEnrichmentLagRecords(partition.Partition, partition.HighWatermark-record.Offset-1)
					}
				}
			}
		}
		consumer.client.AllowRebalance()
	}
}

func (consumer *Consumer) Ready() bool {
	return consumer != nil && consumer.ready.Load()
}

func (consumer *Consumer) processRecord(ctx context.Context, source *kgo.Record) error {
	started := consumer.now()
	record, err := DecodeRecord(source)
	if err != nil {
		return consumer.deadLetterAndCommit(ctx, source, "invalid_record", err, started)
	}
	for attempt := 1; attempt <= consumer.config.RetryAttempts; attempt++ {
		operationCtx, cancel := context.WithTimeout(ctx, consumer.config.OperationTimeout)
		result, applyErr := consumer.store.Apply(operationCtx, record, attempt)
		cancel()
		if applyErr == nil {
			resultLabel := "ready"
			if result.Duplicate {
				resultLabel = "duplicate"
			} else if result.Status == "PARTIAL" {
				resultLabel = "partial"
			}
			if err := consumer.commit(ctx, source); err != nil {
				observability.RecordOrderEnrichmentResult("commit_failed", consumer.now().Sub(started))
				return err
			}
			observability.RecordOrderEnrichmentResult(resultLabel, consumer.now().Sub(started))
			return nil
		}
		errorClass, terminal := terminalApplyError(applyErr)
		if terminal {
			return consumer.deadLetterAndCommit(ctx, source, errorClass, applyErr, started)
		}
		retryReason, retryable := retryableApplyError(applyErr)
		if errors.Is(applyErr, context.DeadlineExceeded) && ctx.Err() == nil {
			retryReason, retryable = "operation_timeout", true
		}
		if !retryable {
			observability.RecordOrderEnrichmentResult("apply_failed", consumer.now().Sub(started))
			return fmt.Errorf("apply order enrichment record %s: %w", recordPosition(source), applyErr)
		}
		if attempt == consumer.config.RetryAttempts {
			if errors.Is(applyErr, ErrOrderNotFound) {
				return consumer.deadLetterAndCommit(ctx, source, "order_not_found", applyErr, started)
			}
			observability.RecordOrderEnrichmentResult("retry_exhausted", consumer.now().Sub(started))
			return fmt.Errorf("apply order enrichment record %s after %d attempts: %w", recordPosition(source), attempt, applyErr)
		}
		observability.RecordOrderEnrichmentRetry(retryReason)
		if err := consumer.wait(ctx, consumer.retryDelay(attempt)); err != nil {
			return err
		}
	}
	return errors.New("order enrichment retry loop exited unexpectedly")
}

func (consumer *Consumer) deadLetterAndCommit(ctx context.Context, source *kgo.Record, errorClass string, cause error, started time.Time) error {
	operationCtx, cancel := context.WithTimeout(ctx, consumer.config.OperationTimeout)
	err := consumer.client.ProduceDeadLetter(operationCtx, source, errorClass, cause, consumer.now())
	cancel()
	if err != nil {
		observability.RecordOrderEnrichmentResult("dlq_failed", consumer.now().Sub(started))
		return err
	}
	if err := consumer.commit(ctx, source); err != nil {
		observability.RecordOrderEnrichmentResult("dlq_commit_failed", consumer.now().Sub(started))
		return err
	}
	observability.RecordOrderEnrichmentResult("dead_lettered", consumer.now().Sub(started))
	return nil
}

func (consumer *Consumer) commit(ctx context.Context, source *kgo.Record) error {
	operationCtx, cancel := context.WithTimeout(ctx, consumer.config.OperationTimeout)
	defer cancel()
	return consumer.client.CommitRecord(operationCtx, source)
}

func (consumer *Consumer) retryDelay(failedAttempt int) time.Duration {
	limit := consumer.config.RetryBase
	for step := 1; step < failedAttempt && limit < consumer.config.RetryMax; step++ {
		if limit > consumer.config.RetryMax/2 {
			limit = consumer.config.RetryMax
			break
		}
		limit *= 2
	}
	if limit > consumer.config.RetryMax {
		limit = consumer.config.RetryMax
	}
	return consumer.jitter(limit)
}

func terminalApplyError(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvalidApplyRecord):
		return "invalid_apply_record", true
	case errors.Is(err, ErrMessageIdentityConflict):
		return "message_identity_conflict", true
	case errors.Is(err, ErrOrderEnrichmentConflict):
		return "order_enrichment_conflict", true
	case errors.Is(err, ErrOrderLotMismatch):
		return "order_lot_mismatch", true
	case errors.Is(err, ErrEnrichmentSourceCorrupt):
		return "source_corrupt", true
	default:
		return "", false
	}
}

func retryableApplyError(err error) (string, bool) {
	if errors.Is(err, ErrOrderNotFound) {
		return "order_not_found", true
	}
	if mysqlerr.Transient(err) {
		return "mysql_transient", true
	}
	return "", false
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
