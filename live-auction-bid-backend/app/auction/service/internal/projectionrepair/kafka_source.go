package projectionrepair

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

type recordPoller interface {
	PollRecords(ctx context.Context, maxRecords int) kgo.Fetches
}

type kafkaAdmin interface {
	Request(ctx context.Context, request kmsg.Request) (kmsg.Response, error)
	Close()
}

type kafkaReader interface {
	recordPoller
	Ping(context.Context) error
	Close()
}

type kafkaReaderFactory func(...kgo.Opt) (kafkaReader, error)

type KafkaSource struct {
	config    kafkaclient.Config
	admin     kafkaAdmin
	newReader kafkaReaderFactory
}

func NewKafkaSource(ctx context.Context, config kafkaclient.Config) (*KafkaSource, error) {
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	admin, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create projection repair Kafka admin: %w", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		return nil, fmt.Errorf("ping Kafka for projection repair: %w", err)
	}
	return &KafkaSource{
		config: config,
		admin:  admin,
		newReader: func(options ...kgo.Opt) (kafkaReader, error) {
			return kgo.NewClient(options...)
		},
	}, nil
}

func (source *KafkaSource) Bounds(ctx context.Context, partition int32) (projector.PartitionBounds, error) {
	if source == nil || source.admin == nil {
		return projector.PartitionBounds{}, errors.New("projection repair Kafka source is required")
	}
	if partition < 0 {
		return projector.PartitionBounds{}, errors.New("projection repair Kafka partition is invalid")
	}
	key := projector.TopicPartition{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: partition}
	values, err := (projector.KafkaBoundsReader{Requester: source.admin}).ReadBounds(ctx, map[string][]int32{
		key.Topic: {key.Partition},
	})
	if err != nil {
		return projector.PartitionBounds{}, err
	}
	bound, exists := values[key]
	if !exists {
		return projector.PartitionBounds{}, errors.New("projection repair Kafka bounds omitted partition")
	}
	return bound, nil
}

func (source *KafkaSource) ReadRange(ctx context.Context, partition int32, start, end int64) ([]*kgo.Record, error) {
	if source == nil || source.admin == nil {
		return nil, errors.New("projection repair Kafka source is required")
	}
	if partition < 0 || start < 0 || end < start || end-start > MaxReplayRecords+1 {
		return nil, errors.New("projection repair Kafka range is invalid")
	}
	if start == end {
		return []*kgo.Record{}, nil
	}
	options, err := source.config.Options()
	if err != nil {
		return nil, err
	}
	options = append(options,
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{
			eventcontract.RuntimeProjectionTopicV1: {partition: kgo.NewOffset().At(start).WithEpoch(-1)},
		}),
		kgo.FetchMaxWait(500*time.Millisecond),
		kgo.MaxBufferedRecords(int(end-start)+32),
		kgo.MaxBufferedBytes(16<<20),
	)
	if source.newReader == nil {
		return nil, errors.New("projection repair Kafka reader factory is required")
	}
	consumer, err := source.newReader(options...)
	if err != nil {
		return nil, fmt.Errorf("create projection repair Kafka reader: %w", err)
	}
	defer consumer.Close()
	if err := consumer.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping Kafka projection repair reader: %w", err)
	}
	return readRange(ctx, consumer, partition, start, end)
}

func (source *KafkaSource) Close() {
	if source != nil && source.admin != nil {
		source.admin.Close()
	}
}

func readRange(ctx context.Context, poller recordPoller, partition int32, start, end int64) ([]*kgo.Record, error) {
	if poller == nil || partition < 0 || start < 0 || end <= start || end-start > MaxReplayRecords+1 {
		return nil, errors.New("projection repair Kafka range reader is invalid")
	}
	result := make([]*kgo.Record, 0, end-start)
	next := start
	for next < end {
		fetches := poller.PollRecords(ctx, int(minInt64(100, end-next)))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			return nil, fmt.Errorf("fetch projection repair Kafka record %s[%d]: %w", fetchErrors[0].Topic, fetchErrors[0].Partition, fetchErrors[0].Err)
		}
		madeProgress := false
		for _, fetch := range fetches {
			for _, topic := range fetch.Topics {
				if topic.Topic != eventcontract.RuntimeProjectionTopicV1 {
					return nil, fmt.Errorf("projection repair received unexpected topic %s", topic.Topic)
				}
				for _, fetchedPartition := range topic.Partitions {
					if fetchedPartition.Partition != partition {
						return nil, fmt.Errorf("projection repair received unexpected partition %d", fetchedPartition.Partition)
					}
					if fetchedPartition.Err != nil {
						return nil, fmt.Errorf("fetch projection repair partition %d: %w", partition, fetchedPartition.Err)
					}
					for _, record := range fetchedPartition.Records {
						if record == nil {
							return nil, errors.New("projection repair received nil Kafka record")
						}
						if record.Offset < next {
							continue
						}
						if record.Offset >= end {
							continue
						}
						if record.Offset != next {
							return nil, fmt.Errorf("%w: Kafka offset got=%d want=%d", ErrUnsafeReplay, record.Offset, next)
						}
						result = append(result, record)
						next++
						madeProgress = true
					}
				}
			}
		}
		if !madeProgress {
			return nil, fmt.Errorf("%w: Kafka returned no record at offset %d", ErrUnsafeReplay, next)
		}
	}
	return result, nil
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
