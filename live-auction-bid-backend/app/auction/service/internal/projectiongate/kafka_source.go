package projectiongate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

type runtimeKafkaClient interface {
	Request(context.Context, kmsg.Request) (kmsg.Response, error)
	AddConsumePartitions(map[string]map[int32]kgo.Offset)
	RemoveConsumePartitions(map[string][]int32)
	PollRecords(context.Context, int) kgo.Fetches
	Close()
}

type KafkaSource struct {
	client runtimeKafkaClient
	pollMu sync.Mutex
}

func NewKafkaSource(config kafkaclient.Config) (*KafkaSource, error) {
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	options = append(options,
		kgo.FetchMaxWait(250*time.Millisecond),
		kgo.MaxBufferedRecords(1000),
		kgo.MaxBufferedBytes(16<<20),
	)
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create projection gate Kafka client: %w", err)
	}
	return &KafkaSource{client: client}, nil
}

func (source *KafkaSource) Bounds(ctx context.Context) (map[int32]PartitionBounds, error) {
	if source == nil || source.client == nil {
		return nil, errors.New("projection gate Kafka client is required")
	}
	partitions, err := source.readPartitions(ctx)
	if err != nil {
		return nil, err
	}
	earliest, err := source.readOffsets(ctx, partitions, -2)
	if err != nil {
		return nil, fmt.Errorf("read runtime-topic earliest offsets: %w", err)
	}
	latest, err := source.readOffsets(ctx, partitions, -1)
	if err != nil {
		return nil, fmt.Errorf("read runtime-topic latest offsets: %w", err)
	}
	result := make(map[int32]PartitionBounds, len(partitions))
	for _, partition := range partitions {
		start, startExists := earliest[partition]
		end, endExists := latest[partition]
		if !startExists || !endExists || start < 0 || end < start {
			return nil, fmt.Errorf("invalid runtime-topic bounds for partition %d", partition)
		}
		result[partition] = PartitionBounds{Earliest: start, Latest: end}
	}
	return result, nil
}

func (source *KafkaSource) OldestRecords(
	ctx context.Context,
	starts map[int32]int64,
) (map[int32]OldestRecord, error) {
	if source == nil || source.client == nil {
		return nil, errors.New("projection gate Kafka client is required")
	}
	if len(starts) == 0 {
		return map[int32]OldestRecord{}, nil
	}
	assignments := map[string]map[int32]kgo.Offset{
		eventcontract.RuntimeProjectionTopicV1: {},
	}
	partitions := make([]int32, 0, len(starts))
	for partition, offset := range starts {
		if partition < 0 || offset < 0 {
			return nil, errors.New("projection gate Kafka start position is invalid")
		}
		assignments[eventcontract.RuntimeProjectionTopicV1][partition] = kgo.NewOffset().At(offset).WithEpoch(-1)
		partitions = append(partitions, partition)
	}
	sort.Slice(partitions, func(left, right int) bool { return partitions[left] < partitions[right] })

	source.pollMu.Lock()
	defer source.pollMu.Unlock()
	source.client.AddConsumePartitions(assignments)
	defer source.client.RemoveConsumePartitions(map[string][]int32{
		eventcontract.RuntimeProjectionTopicV1: partitions,
	})

	result := make(map[int32]OldestRecord, len(starts))
	for len(result) < len(starts) {
		fetches := source.client.PollRecords(ctx, len(starts)-len(result))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			return nil, fmt.Errorf(
				"fetch projection gate Kafka record %s[%d]: %w",
				fetchErrors[0].Topic,
				fetchErrors[0].Partition,
				fetchErrors[0].Err,
			)
		}
		for _, fetch := range fetches {
			for _, topic := range fetch.Topics {
				if topic.Topic != eventcontract.RuntimeProjectionTopicV1 {
					return nil, fmt.Errorf("projection gate received unexpected Kafka topic %s", topic.Topic)
				}
				for _, fetchedPartition := range topic.Partitions {
					wanted, assigned := starts[fetchedPartition.Partition]
					if !assigned {
						return nil, fmt.Errorf("projection gate received unassigned Kafka partition %d", fetchedPartition.Partition)
					}
					if _, found := result[fetchedPartition.Partition]; found {
						continue
					}
					for _, record := range fetchedPartition.Records {
						if record == nil {
							return nil, errors.New("projection gate received a nil Kafka record")
						}
						if record.Offset < wanted {
							continue
						}
						result[fetchedPartition.Partition] = OldestRecord{
							Offset:    record.Offset,
							Timestamp: record.Timestamp,
						}
						break
					}
				}
			}
		}
	}
	return result, nil
}

func (source *KafkaSource) Close() {
	if source != nil && source.client != nil {
		source.client.Close()
	}
}

func (source *KafkaSource) readPartitions(ctx context.Context) ([]int32, error) {
	request := kmsg.NewPtrMetadataRequest()
	request.AllowAutoTopicCreation = false
	topic := kmsg.NewMetadataRequestTopic()
	topic.Topic = kmsg.StringPtr(eventcontract.RuntimeProjectionTopicV1)
	request.Topics = []kmsg.MetadataRequestTopic{topic}
	response, err := source.client.Request(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("read runtime-topic metadata: %w", err)
	}
	metadata, ok := response.(*kmsg.MetadataResponse)
	if !ok || len(metadata.Topics) != 1 || metadata.Topics[0].Topic == nil ||
		*metadata.Topics[0].Topic != eventcontract.RuntimeProjectionTopicV1 {
		return nil, errors.New("runtime-topic Kafka metadata response is invalid")
	}
	if metadata.Topics[0].ErrorCode != 0 {
		return nil, fmt.Errorf("read runtime-topic metadata: %w", kerr.ErrorForCode(metadata.Topics[0].ErrorCode))
	}
	seen := make(map[int32]struct{}, len(metadata.Topics[0].Partitions))
	partitions := make([]int32, 0, len(metadata.Topics[0].Partitions))
	for _, item := range metadata.Topics[0].Partitions {
		if item.ErrorCode != 0 {
			return nil, fmt.Errorf(
				"read runtime-topic partition %d metadata: %w",
				item.Partition,
				kerr.ErrorForCode(item.ErrorCode),
			)
		}
		if item.Partition < 0 {
			return nil, errors.New("runtime-topic metadata returned a negative partition")
		}
		if _, duplicate := seen[item.Partition]; duplicate {
			return nil, errors.New("runtime-topic metadata returned a duplicate partition")
		}
		seen[item.Partition] = struct{}{}
		partitions = append(partitions, item.Partition)
	}
	if len(partitions) == 0 {
		return nil, errors.New("runtime topic has no partitions")
	}
	sort.Slice(partitions, func(left, right int) bool { return partitions[left] < partitions[right] })
	return partitions, nil
}

func (source *KafkaSource) readOffsets(
	ctx context.Context,
	partitions []int32,
	timestamp int64,
) (map[int32]int64, error) {
	request := kmsg.NewPtrListOffsetsRequest()
	request.IsolationLevel = 1
	topic := kmsg.NewListOffsetsRequestTopic()
	topic.Topic = eventcontract.RuntimeProjectionTopicV1
	for _, partition := range partitions {
		item := kmsg.NewListOffsetsRequestTopicPartition()
		item.Partition = partition
		item.Timestamp = timestamp
		topic.Partitions = append(topic.Partitions, item)
	}
	request.Topics = []kmsg.ListOffsetsRequestTopic{topic}
	response, err := source.client.Request(ctx, request)
	if err != nil {
		return nil, err
	}
	offsets, ok := response.(*kmsg.ListOffsetsResponse)
	if !ok || len(offsets.Topics) != 1 || offsets.Topics[0].Topic != eventcontract.RuntimeProjectionTopicV1 {
		return nil, errors.New("runtime-topic Kafka ListOffsets response is invalid")
	}
	expected := make(map[int32]struct{}, len(partitions))
	for _, partition := range partitions {
		expected[partition] = struct{}{}
	}
	result := make(map[int32]int64, len(partitions))
	for _, partition := range offsets.Topics[0].Partitions {
		if _, exists := expected[partition.Partition]; !exists {
			return nil, errors.New("runtime-topic ListOffsets returned an unexpected partition")
		}
		if partition.ErrorCode != 0 {
			return nil, fmt.Errorf(
				"runtime-topic partition %d ListOffsets: %w",
				partition.Partition,
				kerr.ErrorForCode(partition.ErrorCode),
			)
		}
		if partition.Offset < 0 {
			return nil, fmt.Errorf("runtime-topic partition %d returned a negative offset", partition.Partition)
		}
		if _, duplicate := result[partition.Partition]; duplicate {
			return nil, errors.New("runtime-topic ListOffsets returned a duplicate partition")
		}
		result[partition.Partition] = partition.Offset
	}
	if len(result) != len(partitions) {
		return nil, errors.New("runtime-topic ListOffsets omitted a partition")
	}
	return result, nil
}
