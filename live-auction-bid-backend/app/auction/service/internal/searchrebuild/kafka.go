package searchrebuild

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

var (
	ErrKafkaRetentionCliff = errors.New("search rebuild start offset is outside Kafka retention")
	ErrKafkaCatchupGap     = errors.New("search rebuild Kafka catch-up encountered an offset gap")
)

type PartitionBounds struct {
	Earliest int64
	Latest   int64
}

type kafkaRequester interface {
	Request(ctx context.Context, request kmsg.Request) (kmsg.Response, error)
}

func ReadLotStateBounds(ctx context.Context, requester kafkaRequester) (map[int32]PartitionBounds, error) {
	if requester == nil {
		return nil, errors.New("search rebuild Kafka requester is required")
	}
	partitions, err := readLotStatePartitions(ctx, requester)
	if err != nil {
		return nil, err
	}
	earliest, err := readLotStateOffsets(ctx, requester, partitions, -2)
	if err != nil {
		return nil, fmt.Errorf("read lot-state earliest offsets: %w", err)
	}
	latest, err := readLotStateOffsets(ctx, requester, partitions, -1)
	if err != nil {
		return nil, fmt.Errorf("read lot-state latest offsets: %w", err)
	}
	bounds := make(map[int32]PartitionBounds, len(partitions))
	for _, partition := range partitions {
		start, startOK := earliest[partition]
		end, endOK := latest[partition]
		if !startOK || !endOK || start < 0 || end < start {
			return nil, fmt.Errorf("invalid lot-state Kafka bounds for partition %d", partition)
		}
		bounds[partition] = PartitionBounds{Earliest: start, Latest: end}
	}
	return bounds, nil
}

func LatestOffsets(bounds map[int32]PartitionBounds) map[int32]int64 {
	latest := make(map[int32]int64, len(bounds))
	for partition, bound := range bounds {
		latest[partition] = bound.Latest
	}
	return latest
}

func ValidateCatchupStarts(starts map[int32]int64, bounds map[int32]PartitionBounds) error {
	if len(starts) == 0 || len(starts) != len(bounds) {
		return errors.New("search rebuild Kafka start offsets do not cover every partition")
	}
	for partition, bound := range bounds {
		start, exists := starts[partition]
		if !exists || start < bound.Earliest || start > bound.Latest {
			return fmt.Errorf("%w: partition=%d earliest=%d start=%d latest=%d", ErrKafkaRetentionCliff, partition, bound.Earliest, start, bound.Latest)
		}
	}
	return nil
}

func readLotStatePartitions(ctx context.Context, requester kafkaRequester) ([]int32, error) {
	request := kmsg.NewPtrMetadataRequest()
	request.AllowAutoTopicCreation = false
	topic := kmsg.NewMetadataRequestTopic()
	topic.Topic = kmsg.StringPtr(eventcontract.LotStateTopicV1)
	request.Topics = []kmsg.MetadataRequestTopic{topic}
	response, err := requester.Request(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("read lot-state metadata: %w", err)
	}
	metadata, ok := response.(*kmsg.MetadataResponse)
	if !ok || len(metadata.Topics) != 1 || metadata.Topics[0].Topic == nil || *metadata.Topics[0].Topic != eventcontract.LotStateTopicV1 {
		return nil, errors.New("lot-state Kafka metadata response is invalid")
	}
	if metadata.Topics[0].ErrorCode != 0 {
		return nil, fmt.Errorf("read lot-state metadata: %w", kerr.ErrorForCode(metadata.Topics[0].ErrorCode))
	}
	seen := make(map[int32]struct{}, len(metadata.Topics[0].Partitions))
	partitions := make([]int32, 0, len(metadata.Topics[0].Partitions))
	for _, value := range metadata.Topics[0].Partitions {
		if value.ErrorCode != 0 {
			return nil, fmt.Errorf("read lot-state partition %d metadata: %w", value.Partition, kerr.ErrorForCode(value.ErrorCode))
		}
		if value.Partition < 0 {
			return nil, errors.New("lot-state metadata returned a negative partition")
		}
		if _, duplicate := seen[value.Partition]; duplicate {
			return nil, errors.New("lot-state metadata returned a duplicate partition")
		}
		seen[value.Partition] = struct{}{}
		partitions = append(partitions, value.Partition)
	}
	if len(partitions) == 0 {
		return nil, errors.New("lot-state topic has no partitions")
	}
	sort.Slice(partitions, func(i, j int) bool { return partitions[i] < partitions[j] })
	return partitions, nil
}

func readLotStateOffsets(ctx context.Context, requester kafkaRequester, partitions []int32, timestamp int64) (map[int32]int64, error) {
	request := kmsg.NewPtrListOffsetsRequest()
	request.IsolationLevel = 1
	topic := kmsg.NewListOffsetsRequestTopic()
	topic.Topic = eventcontract.LotStateTopicV1
	for _, partition := range partitions {
		item := kmsg.NewListOffsetsRequestTopicPartition()
		item.Partition = partition
		item.Timestamp = timestamp
		topic.Partitions = append(topic.Partitions, item)
	}
	request.Topics = []kmsg.ListOffsetsRequestTopic{topic}
	response, err := requester.Request(ctx, request)
	if err != nil {
		return nil, err
	}
	offsets, ok := response.(*kmsg.ListOffsetsResponse)
	if !ok || len(offsets.Topics) != 1 || offsets.Topics[0].Topic != eventcontract.LotStateTopicV1 {
		return nil, errors.New("lot-state ListOffsets response is invalid")
	}
	result := make(map[int32]int64, len(partitions))
	for _, partition := range offsets.Topics[0].Partitions {
		if partition.ErrorCode != 0 {
			return nil, fmt.Errorf("lot-state partition %d ListOffsets: %w", partition.Partition, kerr.ErrorForCode(partition.ErrorCode))
		}
		if partition.Offset < 0 {
			return nil, fmt.Errorf("lot-state partition %d returned a negative offset", partition.Partition)
		}
		if _, duplicate := result[partition.Partition]; duplicate {
			return nil, errors.New("lot-state ListOffsets returned a duplicate partition")
		}
		result[partition.Partition] = partition.Offset
	}
	if len(result) != len(partitions) {
		return nil, errors.New("lot-state ListOffsets omitted a partition")
	}
	return result, nil
}

type recordPoller interface {
	PollRecords(ctx context.Context, maxRecords int) kgo.Fetches
}

type KafkaCatchup struct {
	client *kgo.Client
	poller recordPoller
	next   map[int32]int64
}

func NewKafkaCatchup(ctx context.Context, config kafkaclient.Config, starts map[int32]int64) (*KafkaCatchup, error) {
	if len(starts) == 0 {
		return nil, errors.New("search rebuild Kafka starts are required")
	}
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	assignments := map[string]map[int32]kgo.Offset{eventcontract.LotStateTopicV1: {}}
	for partition, offset := range starts {
		if partition < 0 || offset < 0 {
			return nil, errors.New("search rebuild Kafka start position is invalid")
		}
		assignments[eventcontract.LotStateTopicV1][partition] = kgo.NewOffset().At(offset).WithEpoch(-1)
	}
	options = append(options,
		kgo.ConsumePartitions(assignments),
		kgo.FetchMaxWait(500*time.Millisecond),
		kgo.MaxBufferedRecords(1000),
		kgo.MaxBufferedBytes(16<<20),
	)
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create search rebuild Kafka catch-up client: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping Kafka for search rebuild: %w", err)
	}
	return newKafkaCatchup(client, starts), nil
}

func newKafkaCatchup(poller recordPoller, starts map[int32]int64) *KafkaCatchup {
	next := make(map[int32]int64, len(starts))
	for partition, offset := range starts {
		next[partition] = offset
	}
	return &KafkaCatchup{poller: poller, next: next}
}

func (catchup *KafkaCatchup) CatchUpTo(
	ctx context.Context,
	targets map[int32]int64,
	apply func(context.Context, searchstate.Record) error,
) (int64, error) {
	if catchup == nil || catchup.poller == nil || len(catchup.next) == 0 || apply == nil {
		return 0, errors.New("search rebuild Kafka catch-up dependencies are required")
	}
	if err := catchup.validateTargets(targets); err != nil {
		return 0, err
	}
	var applied int64
	for !catchup.reached(targets) {
		fetches := catchup.poller.PollRecords(ctx, 100)
		if err := ctx.Err(); err != nil {
			return applied, err
		}
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			return applied, fmt.Errorf("search rebuild Kafka fetch %s[%d]: %w", fetchErrors[0].Topic, fetchErrors[0].Partition, fetchErrors[0].Err)
		}
		for _, fetch := range fetches {
			for _, topic := range fetch.Topics {
				if topic.Topic != eventcontract.LotStateTopicV1 {
					return applied, fmt.Errorf("search rebuild received unexpected topic %s", topic.Topic)
				}
				for _, partition := range topic.Partitions {
					if partition.Err != nil {
						return applied, fmt.Errorf("search rebuild Kafka fetch partition %d: %w", partition.Partition, partition.Err)
					}
					next, exists := catchup.next[partition.Partition]
					if !exists {
						return applied, fmt.Errorf("search rebuild received unassigned partition %d", partition.Partition)
					}
					for _, source := range partition.Records {
						if source == nil {
							return applied, errors.New("search rebuild received a nil Kafka record")
						}
						if source.Offset < next {
							continue
						}
						if source.Offset != next {
							return applied, fmt.Errorf("%w: partition=%d got=%d want=%d", ErrKafkaCatchupGap, partition.Partition, source.Offset, next)
						}
						record, err := searchstate.DecodeRecord(source)
						if err != nil {
							return applied, err
						}
						if err := apply(ctx, record); err != nil {
							return applied, err
						}
						next++
						catchup.next[partition.Partition] = next
						applied++
					}
				}
			}
		}
	}
	return applied, nil
}

func (catchup *KafkaCatchup) Positions() map[int32]int64 {
	positions := make(map[int32]int64, len(catchup.next))
	for partition, offset := range catchup.next {
		positions[partition] = offset
	}
	return positions
}

func (catchup *KafkaCatchup) Close() {
	if catchup != nil && catchup.client != nil {
		catchup.client.Close()
	}
}

func (catchup *KafkaCatchup) validateTargets(targets map[int32]int64) error {
	if len(targets) != len(catchup.next) {
		return errors.New("search rebuild Kafka target offsets do not cover every assigned partition")
	}
	for partition, next := range catchup.next {
		target, exists := targets[partition]
		if !exists || target < 0 || target < next {
			return fmt.Errorf("search rebuild Kafka target for partition %d is behind current position", partition)
		}
	}
	return nil
}

func (catchup *KafkaCatchup) reached(targets map[int32]int64) bool {
	for partition, target := range targets {
		if catchup.next[partition] < target {
			return false
		}
	}
	return true
}
