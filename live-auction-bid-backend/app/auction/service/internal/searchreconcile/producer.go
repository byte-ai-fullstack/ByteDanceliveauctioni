package searchreconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

const maxRepairPayloadBytes = 512 << 10

type repairKafkaClient interface {
	Ping(ctx context.Context) error
	ProduceSync(ctx context.Context, records ...*kgo.Record) kgo.ProduceResults
	Close()
}

type KafkaRepairPublisher struct{ client repairKafkaClient }

func NewKafkaRepairPublisher(ctx context.Context, config kafkaclient.Config) (*KafkaRepairPublisher, error) {
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	options = append(options,
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.MaxBufferedRecords(100),
		kgo.MaxBufferedBytes(2<<20),
	)
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create search repair Kafka producer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping search repair Kafka brokers: %w", err)
	}
	return &KafkaRepairPublisher{client: client}, nil
}

func (publisher *KafkaRepairPublisher) Publish(ctx context.Context, payload []byte) error {
	if publisher == nil || publisher.client == nil {
		return errors.New("search repair Kafka publisher is not initialized")
	}
	record, err := BuildRepairRecord(payload)
	if err != nil {
		return err
	}
	if _, err := publisher.client.ProduceSync(ctx, record).First(); err != nil {
		return fmt.Errorf("produce search repair for lot_id=%s: %w", string(record.Key), err)
	}
	return nil
}

func (publisher *KafkaRepairPublisher) Close() {
	if publisher != nil && publisher.client != nil {
		publisher.client.Close()
	}
}

func BuildRepairRecord(payload []byte) (*kgo.Record, error) {
	if len(payload) == 0 || len(payload) > maxRepairPayloadBytes {
		return nil, errors.New("search repair payload is empty or too large")
	}
	event := new(v1.LotStateDomainEventV1)
	if err := proto.Unmarshal(payload, event); err != nil {
		return nil, fmt.Errorf("decode search repair lot-state event: %w", err)
	}
	if eventcontract.ValidateLotStateDomainEvent(event) != nil || event.GetMetadata() == nil {
		return nil, errors.New("search repair lot-state event is invalid")
	}
	metadata := event.GetMetadata()
	expectedMessageID, err := eventcontract.DomainMessageID(metadata.GetCausationId(), eventcontract.LotStateTopicV1)
	if err != nil || expectedMessageID != metadata.GetMessageId() || metadata.GetSchemaVersion() != 1 || metadata.GetOccurredAtUnixMs() <= 0 ||
		!searchstate.ValidText(metadata.GetTraceId(), 128) {
		return nil, errors.New("search repair message identity is invalid")
	}
	headers := map[string]string{
		eventcontract.RuntimeHeaderContentType:   eventcontract.DomainEventContentType,
		eventcontract.DomainHeaderMessageID:      metadata.GetMessageId(),
		eventcontract.DomainHeaderCausationID:    metadata.GetCausationId(),
		eventcontract.RuntimeHeaderTraceID:       metadata.GetTraceId(),
		eventcontract.RuntimeHeaderSchemaVersion: strconv.FormatUint(uint64(metadata.GetSchemaVersion()), 10),
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	recordHeaders := make([]kgo.RecordHeader, 0, len(keys))
	for _, key := range keys {
		recordHeaders = append(recordHeaders, kgo.RecordHeader{Key: key, Value: []byte(headers[key])})
	}
	return &kgo.Record{
		Topic: eventcontract.LotStateTopicV1, Key: []byte(event.GetLotId()),
		Value: append([]byte(nil), payload...), Headers: recordHeaders,
	}, nil
}
