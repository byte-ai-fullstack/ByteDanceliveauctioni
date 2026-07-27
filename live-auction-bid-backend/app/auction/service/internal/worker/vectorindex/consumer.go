package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

type recordProcessor interface {
	Apply(ctx context.Context, record Record) (searchindex.VectorApplyResult, error)
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

type Consumer struct {
	client consumerClient
	worker recordProcessor
	config ConsumerConfig
	ready  atomic.Bool
	now    func() time.Time
	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

func NewConsumer(client consumerClient, worker recordProcessor, config ConsumerConfig) (*Consumer, error) {
	if client == nil || worker == nil {
		return nil, errors.New("pgvector Kafka client and processor are required")
	}
	if config.MaxPollRecords <= 0 || config.MaxPollRecords > 10_000 || config.RetryAttempts <= 0 || config.RetryAttempts > 100 ||
		config.RetryBase <= 0 || config.RetryMax < config.RetryBase || config.OperationTimeout <= 0 {
		return nil, errors.New("pgvector consumer limits, retries, and timeouts are invalid")
	}
	return &Consumer{
		client: client, worker: worker, config: config, now: time.Now, wait: waitContext,
		jitter: func(limit time.Duration) time.Duration {
			if limit <= 1 {
				return limit
			}
			return time.Duration(rand.Int63n(int64(limit))) + 1
		},
	}, nil
}

func (consumer *Consumer) Run(ctx context.Context) error {
	if consumer == nil || consumer.client == nil || consumer.worker == nil {
		return errors.New("pgvector consumer is not initialized")
	}
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
			return fmt.Errorf("poll pgvector Kafka records: %s[%d]: %w", fetchErrors[0].Topic, fetchErrors[0].Partition, fetchErrors[0].Err)
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
						observability.SetSearchVectorLagRecords(partition.Partition, partition.HighWatermark-record.Offset-1)
					}
				}
			}
		}
		consumer.client.AllowRebalance()
	}
}

func (consumer *Consumer) Ready() bool { return consumer != nil && consumer.ready.Load() }

func (consumer *Consumer) processRecord(ctx context.Context, source *kgo.Record) error {
	started := consumer.now()
	record, err := DecodeRecord(source)
	if err != nil {
		result := consumer.deadLetterAndCommit(ctx, source, "invalid_record", err, started)
		if result == nil {
			observability.RecordSearchVectorResult("dead_lettered", consumer.now().Sub(started))
		}
		return result
	}
	for attempt := 1; attempt <= consumer.config.RetryAttempts; attempt++ {
		operationCtx, cancel := context.WithTimeout(ctx, consumer.config.OperationTimeout)
		result, applyErr := consumer.worker.Apply(operationCtx, record)
		cancel()
		if applyErr == nil {
			if err := consumer.commit(ctx, source); err != nil {
				observability.RecordSearchVectorResult("commit_failed", consumer.now().Sub(started))
				return err
			}
			label := "applied"
			if result.Duplicate {
				label = "duplicate"
			} else if result.Stale {
				label = "stale"
			}
			observability.RecordSearchVectorResult(label, consumer.now().Sub(started))
			return nil
		}
		if errors.Is(applyErr, searchindex.ErrInvalidVectorDocument) || errors.Is(applyErr, searchindex.ErrVectorVersionConflict) || errors.Is(applyErr, ErrInvalidRecord) {
			result := consumer.deadLetterAndCommit(ctx, source, "document_identity_conflict", applyErr, started)
			if result == nil {
				observability.RecordSearchVectorResult("dead_lettered", consumer.now().Sub(started))
			}
			return result
		}
		if attempt == consumer.config.RetryAttempts {
			if errors.Is(applyErr, ErrEmbeddingFailure) {
				result := consumer.deadLetterAndCommit(ctx, source, "embedding_failed", applyErr, started)
				if result == nil {
					observability.RecordSearchVectorResult("embedding_dead_lettered", consumer.now().Sub(started))
				}
				return result
			}
			observability.RecordSearchVectorResult("retry_exhausted", consumer.now().Sub(started))
			return fmt.Errorf("apply pgvector record %s after %d attempts: %w", recordPosition(source), attempt, applyErr)
		}
		reason := "index"
		if errors.Is(applyErr, ErrEmbeddingFailure) {
			reason = "embedding"
		}
		observability.RecordSearchVectorRetry(reason)
		if err := consumer.wait(ctx, consumer.retryDelay(attempt)); err != nil {
			return err
		}
	}
	return errors.New("pgvector retry loop exited unexpectedly")
}

func (consumer *Consumer) deadLetterAndCommit(ctx context.Context, source *kgo.Record, errorClass string, cause error, _ time.Time) error {
	operationCtx, cancel := context.WithTimeout(ctx, consumer.config.OperationTimeout)
	err := consumer.client.ProduceDeadLetter(operationCtx, source, errorClass, cause, consumer.now())
	cancel()
	if err != nil {
		return err
	}
	return consumer.commit(ctx, source)
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
