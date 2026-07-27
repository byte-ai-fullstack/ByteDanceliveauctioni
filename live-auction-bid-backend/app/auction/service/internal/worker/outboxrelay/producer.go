package outboxrelay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

const runtimeProducerBatchMaxBytes = 512 << 10

var ErrInvalidKafkaProducerConfig = kafkaclient.ErrInvalidConfig

type KafkaProducerConfig = kafkaclient.Config

type kafkaClient interface {
	Ping(ctx context.Context) error
	ProduceSync(ctx context.Context, records ...*kgo.Record) kgo.ProduceResults
	Close()
}

// KafkaRuntimeProducer publishes validated RuntimeFactV1 messages to the fixed Runtime Topic.
type KafkaRuntimeProducer struct {
	client kafkaClient
}

// NewKafkaRuntimeProducer creates an idempotent, all-ISR-acknowledged producer and verifies broker reachability.
func NewKafkaRuntimeProducer(ctx context.Context, cfg KafkaProducerConfig) (*KafkaRuntimeProducer, error) {
	options, err := cfg.Options()
	if err != nil {
		return nil, err
	}
	options = append(options,
		kgo.RequiredAcks(kgo.AllISRAcks()),
		// Relay calls ProduceSync serially for each outbox shard. Keeping this
		// explicit documents the single-flight ordering requirement; franz-go
		// may internally allow up to five requests when idempotence is enabled.
		kgo.MaxProduceRequestsInflightPerBroker(1),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchMaxBytes(runtimeProducerBatchMaxBytes),
		kgo.MaxBufferedRecords(data.RuntimeOutboxShardCount),
		kgo.MaxBufferedBytes(8<<20),
	)
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create Kafka runtime producer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping Kafka runtime producer brokers: %w", err)
	}
	return &KafkaRuntimeProducer{client: client}, nil
}

// ProduceRuntimeFact waits for Kafka's definitive produce result before returning success.
func (p *KafkaRuntimeProducer) ProduceRuntimeFact(ctx context.Context, fact *v1.RuntimeFactV1, ownership data.RuntimeOutboxOwnership) error {
	if p == nil || p.client == nil {
		return errors.New("kafka runtime producer is not initialized")
	}
	record, err := BuildRuntimeFactRecord(fact, ownership)
	if err != nil {
		return err
	}
	_, err = p.client.ProduceSync(ctx, record).First()
	if err != nil {
		return fmt.Errorf("produce runtime fact %s to %s: %w", fact.GetEventId(), eventcontract.RuntimeProjectionTopicV1, err)
	}
	return nil
}

// Close flushes and closes the underlying franz-go client.
func (p *KafkaRuntimeProducer) Close() {
	if p != nil && p.client != nil {
		p.client.Close()
	}
}

// BuildRuntimeFactRecord performs no business recomputation; it validates and deterministically encodes the Lua fact.
func BuildRuntimeFactRecord(fact *v1.RuntimeFactV1, ownership data.RuntimeOutboxOwnership) (*kgo.Record, error) {
	payload, err := eventcontract.MarshalRuntimeFactBinary(fact)
	if err != nil {
		return nil, err
	}
	if err := validateOwnership(ownership); err != nil {
		return nil, err
	}
	return &kgo.Record{
		Topic:     eventcontract.RuntimeProjectionTopicV1,
		Key:       []byte(fact.GetLotId()),
		Value:     payload,
		Timestamp: time.UnixMilli(fact.GetOccurredAtUnixMs()).UTC(),
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.RuntimeFactContentType)},
			{Key: eventcontract.RuntimeHeaderEventID, Value: []byte(fact.GetEventId())},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte(fact.GetTraceId())},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte(strconv.FormatUint(uint64(fact.GetSchemaVersion()), 10))},
			{Key: eventcontract.RuntimeHeaderLotVersion, Value: []byte(strconv.FormatInt(fact.GetLotVersion(), 10))},
			{Key: eventcontract.RuntimeHeaderOwnerEpoch, Value: []byte(strconv.FormatInt(ownership.Epoch, 10))},
			{Key: eventcontract.RuntimeHeaderOutboxShard, Value: []byte(strconv.Itoa(ownership.Shard))},
		},
	}, nil
}

func validateOwnership(ownership data.RuntimeOutboxOwnership) error {
	instanceID := strings.TrimSpace(ownership.InstanceID)
	expectedToken := instanceID + ":" + strconv.FormatInt(ownership.Epoch, 10)
	if ownership.Shard < 0 || ownership.Shard >= data.RuntimeOutboxShardCount || instanceID == "" || strings.ContainsAny(instanceID, ":\r\n") || ownership.Epoch <= 0 || ownership.OwnerToken != expectedToken {
		return fmt.Errorf("%w: invalid runtime outbox ownership", data.ErrRuntimeOutboxInvalidArgument)
	}
	return nil
}
