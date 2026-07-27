package projector

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

var ErrRetentionCliff = errors.New("projector DB offset is outside Kafka retention bounds")

type TopicPartition struct {
	Topic     string
	Partition int32
}

type PartitionBounds struct {
	Earliest int64
	Latest   int64
}

type partitionOffsetInitializer interface {
	EnsurePartitionOffset(ctx context.Context, topic string, partition int32, earliest int64) (int64, error)
}

type partitionBoundsReader interface {
	ReadBounds(ctx context.Context, assignments map[string][]int32) (map[TopicPartition]PartitionBounds, error)
}

// ResolveAssignmentOffsets returns only explicit MySQL-authoritative offsets and rejects silent latest resets.
func ResolveAssignmentOffsets(ctx context.Context, assignments map[string][]int32, store partitionOffsetInitializer, reader partitionBoundsReader) (map[string]map[int32]kgo.Offset, error) {
	if store == nil || reader == nil {
		return nil, errors.New("projector offset store and Kafka bounds reader are required")
	}
	if len(assignments) == 0 {
		return map[string]map[int32]kgo.Offset{}, nil
	}
	for topic, partitions := range assignments {
		if topic != eventcontract.RuntimeProjectionTopicV1 || len(partitions) == 0 {
			return nil, fmt.Errorf("%w: unexpected assignment %s", ErrInvalidApplyRecord, topic)
		}
		for _, partition := range partitions {
			if partition < 0 {
				return nil, fmt.Errorf("%w: negative assigned partition", ErrInvalidApplyRecord)
			}
		}
	}
	bounds, err := reader.ReadBounds(ctx, assignments)
	if err != nil {
		return nil, err
	}
	resolved := make(map[string]map[int32]kgo.Offset, len(assignments))
	for topic, partitions := range assignments {
		resolved[topic] = make(map[int32]kgo.Offset, len(partitions))
		for _, partition := range partitions {
			bound, exists := bounds[TopicPartition{Topic: topic, Partition: partition}]
			if !exists || bound.Earliest < 0 || bound.Latest < bound.Earliest {
				return nil, fmt.Errorf("%w: invalid Kafka bounds for %s[%d]", ErrRetentionCliff, topic, partition)
			}
			nextOffset, err := store.EnsurePartitionOffset(ctx, topic, partition, bound.Earliest)
			if err != nil {
				return nil, err
			}
			if nextOffset < bound.Earliest || nextOffset > bound.Latest {
				return nil, fmt.Errorf("%w: %s[%d] earliest=%d next=%d latest=%d", ErrRetentionCliff, topic, partition, bound.Earliest, nextOffset, bound.Latest)
			}
			resolved[topic][partition] = kgo.NewOffset().At(nextOffset).WithEpoch(-1)
		}
	}
	return resolved, nil
}

type kafkaRequester interface {
	Request(ctx context.Context, request kmsg.Request) (kmsg.Response, error)
}

type KafkaBoundsReader struct {
	Requester kafkaRequester
}

func (reader KafkaBoundsReader) ReadBounds(ctx context.Context, assignments map[string][]int32) (map[TopicPartition]PartitionBounds, error) {
	if reader.Requester == nil {
		return nil, errors.New("kafka bounds requester is required")
	}
	earliest, err := reader.listOffsets(ctx, assignments, -2)
	if err != nil {
		return nil, fmt.Errorf("read Kafka earliest offsets: %w", err)
	}
	latest, err := reader.listOffsets(ctx, assignments, -1)
	if err != nil {
		return nil, fmt.Errorf("read Kafka latest offsets: %w", err)
	}
	result := make(map[TopicPartition]PartitionBounds, len(earliest))
	for key, start := range earliest {
		end, exists := latest[key]
		if !exists {
			return nil, fmt.Errorf("kafka latest offset missing for %s[%d]", key.Topic, key.Partition)
		}
		result[key] = PartitionBounds{Earliest: start, Latest: end}
	}
	return result, nil
}

func (reader KafkaBoundsReader) listOffsets(ctx context.Context, assignments map[string][]int32, timestamp int64) (map[TopicPartition]int64, error) {
	request := kmsg.NewPtrListOffsetsRequest()
	request.IsolationLevel = 1
	for topic, partitions := range assignments {
		requestTopic := kmsg.NewListOffsetsRequestTopic()
		requestTopic.Topic = topic
		for _, partition := range partitions {
			requestPartition := kmsg.NewListOffsetsRequestTopicPartition()
			requestPartition.Partition = partition
			requestPartition.Timestamp = timestamp
			requestTopic.Partitions = append(requestTopic.Partitions, requestPartition)
		}
		request.Topics = append(request.Topics, requestTopic)
	}
	response, err := reader.Requester.Request(ctx, request)
	if err != nil {
		return nil, err
	}
	listResponse, ok := response.(*kmsg.ListOffsetsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected Kafka ListOffsets response %T", response)
	}
	result := make(map[TopicPartition]int64)
	for _, topic := range listResponse.Topics {
		for _, partition := range topic.Partitions {
			if partition.ErrorCode != 0 {
				return nil, fmt.Errorf("kafka ListOffsets %s[%d]: %w", topic.Topic, partition.Partition, kerr.ErrorForCode(partition.ErrorCode))
			}
			if partition.Offset < 0 {
				return nil, fmt.Errorf("kafka ListOffsets %s[%d] returned negative offset", topic.Topic, partition.Partition)
			}
			key := TopicPartition{Topic: topic.Topic, Partition: partition.Partition}
			if _, duplicate := result[key]; duplicate {
				return nil, fmt.Errorf("kafka ListOffsets duplicated %s[%d]", topic.Topic, partition.Partition)
			}
			result[key] = partition.Offset
		}
	}
	return result, nil
}
