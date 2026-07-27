package projector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

type projectionApplier interface {
	Apply(ctx context.Context, record DecodedRecord) (ApplyResult, error)
	RecordFinding(ctx context.Context, record DecodedRecord, kind, severity string, freeze bool, cause error) error
}

type projectionConsumerClient interface {
	PollRecords(ctx context.Context, maxRecords int) kgo.Fetches
	AllowRebalance()
	PauseFetchPartitions(topicPartitions map[string][]int32) map[string][]int32
	CommitProjected(record *kgo.Record, nextOffset int64)
}

type ConsumerConfig struct {
	MaxPollRecords int
}

type Consumer struct {
	client      projectionConsumerClient
	store       projectionApplier
	config      ConsumerConfig
	mu          sync.RWMutex
	paused      map[TopicPartition]string
	initialized atomic.Bool
}

func NewConsumer(client projectionConsumerClient, store projectionApplier, config ConsumerConfig) (*Consumer, error) {
	if client == nil || store == nil {
		return nil, errors.New("projector Kafka client and store are required")
	}
	if config.MaxPollRecords <= 0 || config.MaxPollRecords > 10_000 {
		return nil, errors.New("projector max poll records must be within [1,10000]")
	}
	return &Consumer{client: client, store: store, config: config, paused: make(map[TopicPartition]string)}, nil
}

// Run processes each polled partition sequentially while different partitions run concurrently.
// BlockRebalanceOnPoll keeps assignment callbacks out until every record in the bounded poll is committed or paused.
func (consumer *Consumer) Run(ctx context.Context) error {
	if consumer == nil || consumer.client == nil || consumer.store == nil {
		return errors.New("projector consumer is not initialized")
	}
	// NewKafkaConsumer has already established the Kafka client. Mark the
	// running loop initialized before the first poll so an empty topic does not
	// hold /readyz at not_ready forever.
	consumer.initialized.Store(true)
	for {
		fetches := consumer.client.PollRecords(ctx, consumer.config.MaxPollRecords)
		if ctx.Err() != nil || fetches.IsClientClosed() {
			consumer.client.AllowRebalance()
			return nil
		}
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			consumer.client.AllowRebalance()
			return fmt.Errorf("poll projector Kafka records: %s[%d]: %w", fetchErrors[0].Topic, fetchErrors[0].Partition, fetchErrors[0].Err)
		}
		consumer.initialized.Store(true)
		partitions := groupFetchedRecords(fetches)
		failures := consumer.processPoll(ctx, partitions)
		consumer.client.AllowRebalance()
		for _, failure := range failures {
			consumer.client.PauseFetchPartitions(map[string][]int32{failure.Topic: {failure.Partition}})
			consumer.mu.Lock()
			reason := projectionPauseReason(failure.Err)
			consumer.paused[TopicPartition{Topic: failure.Topic, Partition: failure.Partition}] = reason
			consumer.mu.Unlock()
			observability.SetProjectorPaused(failure.Partition, reason, true)
		}
	}
}

func (consumer *Consumer) Ready() bool {
	if consumer == nil {
		return false
	}
	consumer.mu.RLock()
	defer consumer.mu.RUnlock()
	return consumer.initialized.Load() && len(consumer.paused) == 0
}

func (consumer *Consumer) PausedPartitions() map[TopicPartition]string {
	if consumer == nil {
		return nil
	}
	consumer.mu.RLock()
	defer consumer.mu.RUnlock()
	result := make(map[TopicPartition]string, len(consumer.paused))
	for partition, reason := range consumer.paused {
		result[partition] = reason
	}
	return result
}

type fetchedPartition struct {
	Topic         string
	Partition     int32
	HighWatermark int64
	Records       []*kgo.Record
}

type partitionFailure struct {
	Topic     string
	Partition int32
	Err       error
}

func groupFetchedRecords(fetches kgo.Fetches) []fetchedPartition {
	result := make([]fetchedPartition, 0)
	for _, fetch := range fetches {
		for _, topic := range fetch.Topics {
			for _, partition := range topic.Partitions {
				if len(partition.Records) == 0 {
					continue
				}
				result = append(result, fetchedPartition{
					Topic: topic.Topic, Partition: partition.Partition, HighWatermark: partition.HighWatermark, Records: partition.Records,
				})
			}
		}
	}
	return result
}

func (consumer *Consumer) processPoll(ctx context.Context, partitions []fetchedPartition) []partitionFailure {
	failures := make(chan partitionFailure, len(partitions))
	var wait sync.WaitGroup
	for _, partition := range partitions {
		partition := partition
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := consumer.processPartition(ctx, partition.Records); err != nil && !errors.Is(err, context.Canceled) {
				failures <- partitionFailure{Topic: partition.Topic, Partition: partition.Partition, Err: err}
			} else if err == nil {
				lastNextOffset := partition.Records[len(partition.Records)-1].Offset + 1
				observability.SetProjectorLagRecords(partition.Partition, partition.HighWatermark-lastNextOffset)
			}
		}()
	}
	wait.Wait()
	close(failures)
	result := make([]partitionFailure, 0, len(failures))
	for failure := range failures {
		result = append(result, failure)
	}
	return result
}

func (consumer *Consumer) processPartition(ctx context.Context, records []*kgo.Record) error {
	for _, record := range records {
		decoded, err := DecodeRecord(record)
		if err != nil {
			return err
		}
		result, err := consumer.store.Apply(ctx, decoded)
		if err != nil {
			return errors.Join(err, consumer.recordFailure(ctx, decoded, err))
		}
		if result.DuplicateEvent {
			observability.RecordProjectorDuplicate()
		}
		consumer.client.CommitProjected(record, result.NextOffset)
	}
	return nil
}

func (consumer *Consumer) recordFailure(ctx context.Context, record DecodedRecord, cause error) error {
	var kind, severity string
	freeze := false
	switch {
	case errors.Is(cause, ErrEventIdentityConflict):
		kind, severity, freeze = FindingEventIdentityConflict, FindingSeverityP0, true
	case errors.Is(cause, ErrRuntimeProjectionGap), errors.Is(cause, ErrPartitionOffsetGap):
		kind, severity = FindingRuntimeVersionGap, FindingSeverityP1
	case errors.Is(cause, ErrProjectionIdentity), errors.Is(cause, ErrProjectionConfigVersion), errors.Is(cause, ErrProjectionCAS):
		kind, severity, freeze = FindingProjectionConflict, FindingSeverityP0, true
	default:
		return nil
	}
	findingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := consumer.store.RecordFinding(findingCtx, record, kind, severity, freeze, cause); err != nil {
		return fmt.Errorf("record projector finding: %w", err)
	}
	return nil
}

func projectionPauseReason(err error) string {
	switch {
	case errors.Is(err, ErrInvalidRuntimeRecord), errors.Is(err, ErrInvalidApplyRecord), errors.Is(err, ErrInvalidProjection):
		return "invalid_record"
	case errors.Is(err, ErrEventIdentityConflict):
		return "event_identity"
	case errors.Is(err, ErrPartitionOffsetGap):
		return "offset_gap"
	case errors.Is(err, ErrRuntimeProjectionGap):
		return "version_gap"
	case errors.Is(err, ErrProjectionLotFrozen):
		return "lot_frozen"
	case errors.Is(err, ErrProjectionConfigVersion):
		return "config_version"
	case IsTransientDatabaseError(err):
		return "mysql_transient_exhausted"
	default:
		return "projection_error"
	}
}
