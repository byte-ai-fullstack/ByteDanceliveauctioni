package projector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

type KafkaConsumerConfig struct {
	GroupID           string
	SessionTimeout    time.Duration
	HeartbeatInterval time.Duration
	MaxPollRecords    int
}

type KafkaConsumer struct {
	client *kgo.Client
}

func NewKafkaConsumer(ctx context.Context, kafkaConfig kafkaclient.Config, offsets partitionOffsetInitializer, config KafkaConsumerConfig) (*KafkaConsumer, error) {
	if offsets == nil {
		return nil, errors.New("projector partition offset initializer is required")
	}
	config.GroupID = strings.TrimSpace(config.GroupID)
	if config.GroupID == "" || len(config.GroupID) > 128 || strings.ContainsAny(config.GroupID, "\r\n\x00") {
		return nil, errors.New("projector Kafka group ID is invalid")
	}
	if config.SessionTimeout <= 0 || config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.SessionTimeout {
		return nil, errors.New("projector Kafka session and heartbeat intervals are invalid")
	}
	if config.MaxPollRecords <= 0 || config.MaxPollRecords > 10_000 {
		return nil, errors.New("projector max poll records must be within [1,10000]")
	}
	sharedOptions, err := kafkaConfig.Options()
	if err != nil {
		return nil, err
	}
	var client *kgo.Client
	options := append(sharedOptions,
		kgo.ConsumerGroup(config.GroupID),
		kgo.ConsumeTopics(eventcontract.RuntimeProjectionTopicV1),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
		kgo.SessionTimeout(config.SessionTimeout),
		kgo.HeartbeatInterval(config.HeartbeatInterval),
		kgo.ConsumeResetOffset(kgo.NoResetOffset().AtStart()),
		kgo.AdjustFetchOffsetsFn(func(assignCtx context.Context, current map[string]map[int32]kgo.Offset) (map[string]map[int32]kgo.Offset, error) {
			if client == nil {
				return nil, errors.New("projector Kafka client is not initialized")
			}
			assignments := make(map[string][]int32, len(current))
			for topic, partitions := range current {
				for partition := range partitions {
					assignments[topic] = append(assignments[topic], partition)
				}
			}
			return ResolveAssignmentOffsets(assignCtx, assignments, offsets, KafkaBoundsReader{Requester: client})
		}),
	)
	client, err = kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create projector Kafka consumer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping Kafka for projector: %w", err)
	}
	return &KafkaConsumer{client: client}, nil
}

func (consumer *KafkaConsumer) PollRecords(ctx context.Context, maxRecords int) kgo.Fetches {
	return consumer.client.PollRecords(ctx, maxRecords)
}

func (consumer *KafkaConsumer) AllowRebalance() {
	consumer.client.AllowRebalance()
}

func (consumer *KafkaConsumer) PauseFetchPartitions(topicPartitions map[string][]int32) map[string][]int32 {
	return consumer.client.PauseFetchPartitions(topicPartitions)
}

func (consumer *KafkaConsumer) CommitProjected(record *kgo.Record, nextOffset int64) {
	if record == nil || nextOffset < 0 {
		return
	}
	offsets := map[string]map[int32]kgo.EpochOffset{
		record.Topic: {record.Partition: {Epoch: -1, Offset: nextOffset}},
	}
	consumer.client.CommitOffsets(context.Background(), offsets, nil)
}

func (consumer *KafkaConsumer) Close() {
	if consumer != nil && consumer.client != nil {
		consumer.client.Close()
	}
}
