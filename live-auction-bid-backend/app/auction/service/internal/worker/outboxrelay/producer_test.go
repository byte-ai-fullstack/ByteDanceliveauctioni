package outboxrelay

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestBuildRuntimeFactRecordUsesStableContractMetadata(t *testing.T) {
	fact := runtimeFactFixture(t)
	ownership := runtimeOwnershipFixture()

	record, err := BuildRuntimeFactRecord(fact, ownership)
	if err != nil {
		t.Fatalf("BuildRuntimeFactRecord: %v", err)
	}
	if record.Topic != eventcontract.RuntimeProjectionTopicV1 {
		t.Fatalf("topic=%q want=%q", record.Topic, eventcontract.RuntimeProjectionTopicV1)
	}
	if string(record.Key) != fact.GetLotId() {
		t.Fatalf("key=%q want=%q", record.Key, fact.GetLotId())
	}
	if !record.Timestamp.Equal(time.UnixMilli(fact.GetOccurredAtUnixMs()).UTC()) {
		t.Fatalf("timestamp=%s want=%s", record.Timestamp, time.UnixMilli(fact.GetOccurredAtUnixMs()).UTC())
	}
	decoded := new(v1.RuntimeFactV1)
	if err := proto.Unmarshal(record.Value, decoded); err != nil {
		t.Fatalf("unmarshal record value: %v", err)
	}
	if !proto.Equal(decoded, fact) {
		t.Fatalf("record value changed RuntimeFact: got=%v want=%v", decoded, fact)
	}

	wantHeaders := []kgo.RecordHeader{
		{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.RuntimeFactContentType)},
		{Key: eventcontract.RuntimeHeaderEventID, Value: []byte(fact.GetEventId())},
		{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte(fact.GetTraceId())},
		{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
		{Key: eventcontract.RuntimeHeaderLotVersion, Value: []byte("7")},
		{Key: eventcontract.RuntimeHeaderOwnerEpoch, Value: []byte("9")},
		{Key: eventcontract.RuntimeHeaderOutboxShard, Value: []byte("3")},
	}
	if !reflect.DeepEqual(record.Headers, wantHeaders) {
		t.Fatalf("headers=%v want=%v", record.Headers, wantHeaders)
	}
}

func TestBuildRuntimeFactRecordRejectsInvalidFactAndOwnership(t *testing.T) {
	t.Run("invalid fact", func(t *testing.T) {
		fact := runtimeFactFixture(t)
		fact.LotVersion++
		if _, err := BuildRuntimeFactRecord(fact, runtimeOwnershipFixture()); !errors.Is(err, eventcontract.ErrInvalidRuntimeOutboxItem) {
			t.Fatalf("error=%v want ErrInvalidRuntimeOutboxItem", err)
		}
	})

	t.Run("invalid ownership", func(t *testing.T) {
		ownership := runtimeOwnershipFixture()
		ownership.OwnerToken = "relay-other:9"
		if _, err := BuildRuntimeFactRecord(runtimeFactFixture(t), ownership); !errors.Is(err, data.ErrRuntimeOutboxInvalidArgument) {
			t.Fatalf("error=%v want ErrRuntimeOutboxInvalidArgument", err)
		}
	})
}

func TestKafkaRuntimeProducerWaitsForProduceResultAndClosesClient(t *testing.T) {
	client := &kafkaClientStub{}
	producer := &KafkaRuntimeProducer{client: client}
	fact := runtimeFactFixture(t)

	if err := producer.ProduceRuntimeFact(context.Background(), fact, runtimeOwnershipFixture()); err != nil {
		t.Fatalf("ProduceRuntimeFact: %v", err)
	}
	if len(client.records) != 1 || string(client.records[0].Key) != fact.GetLotId() {
		t.Fatalf("produced records=%v", client.records)
	}
	producer.Close()
	if !client.closed {
		t.Fatal("Close did not close Kafka client")
	}

	client.produceErr = errors.New("broker unavailable")
	if err := producer.ProduceRuntimeFact(context.Background(), fact, runtimeOwnershipFixture()); !errors.Is(err, client.produceErr) {
		t.Fatalf("error=%v want broker error", err)
	}
}

func TestKafkaRuntimeProducerRejectsUninitializedClient(t *testing.T) {
	var producer *KafkaRuntimeProducer
	if err := producer.ProduceRuntimeFact(context.Background(), runtimeFactFixture(t), runtimeOwnershipFixture()); err == nil {
		t.Fatal("nil producer returned no error")
	}
	producer.Close()

	producer = &KafkaRuntimeProducer{}
	if err := producer.ProduceRuntimeFact(context.Background(), runtimeFactFixture(t), runtimeOwnershipFixture()); err == nil {
		t.Fatal("producer without client returned no error")
	}
	producer.Close()
}

func TestNewKafkaRuntimeProducerRejectsInvalidConfigBeforeDial(t *testing.T) {
	tests := []KafkaProducerConfig{
		{ClientID: "relay-1"},
		{Brokers: []string{"kafka:9092"}},
		{Brokers: []string{"kafka:9092", "\n"}, ClientID: "relay-1"},
		{Brokers: []string{"kafka:9092"}, ClientID: "relay\n1"},
	}
	for _, cfg := range tests {
		if _, err := NewKafkaRuntimeProducer(context.Background(), cfg); !errors.Is(err, ErrInvalidKafkaProducerConfig) {
			t.Fatalf("config=%+v error=%v want ErrInvalidKafkaProducerConfig", cfg, err)
		}
	}
}

type kafkaClientStub struct {
	records    []*kgo.Record
	produceErr error
	closed     bool
}

func (c *kafkaClientStub) Ping(context.Context) error { return nil }

func (c *kafkaClientStub) ProduceSync(_ context.Context, records ...*kgo.Record) kgo.ProduceResults {
	c.records = append(c.records, records...)
	results := make(kgo.ProduceResults, 0, len(records))
	for _, record := range records {
		results = append(results, kgo.ProduceResult{Record: record, Err: c.produceErr})
	}
	return results
}

func (c *kafkaClientStub) Close() { c.closed = true }

func runtimeFactFixture(t *testing.T) *v1.RuntimeFactV1 {
	t.Helper()
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatalf("NewEventID: %v", err)
	}
	durationMs := int64(60_000)
	antiSnipeWindowMs := int64(10_000)
	antiSnipeExtendMs := int64(30_000)
	return &v1.RuntimeFactV1{
		EventId:          eventID,
		TraceId:          "trace-1",
		LotId:            "lot-1",
		RoomId:           "room-1",
		PrevLotVersion:   6,
		LotVersion:       7,
		OccurredAtUnixMs: 1_700_000_000_000,
		ConfigVersion:    3,
		Command:          v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID,
		StateAfter: &v1.LotRuntimeStateV1{
			LotId:             "lot-1",
			RoomId:            "room-1",
			Status:            v1.LotStatus_LOT_STATUS_LIVE,
			Currency:          "CNY",
			StartPriceFen:     10_000,
			MinIncrementFen:   100,
			CurrentPriceFen:   12_000,
			LeadingUserId:     "user-1",
			LeadingNickname:   "User 1",
			StartedAtUnixMs:   1_699_999_940_000,
			EndsAtUnixMs:      1_700_000_060_000,
			BidCount:          4,
			ParticipantCount:  3,
			DurationMs:        &durationMs,
			AntiSnipeWindowMs: &antiSnipeWindowMs,
			AntiSnipeExtendMs: &antiSnipeExtendMs,
		},
		AcceptedBid: &v1.AcceptedBidV1{
			BidId:            "bid-1",
			UserId:           "user-1",
			Nickname:         "User 1",
			AmountFen:        12_000,
			AcceptedAtUnixMs: 1_700_000_000_000,
		},
		IdempotencyKey: "idem-1",
		SchemaVersion:  eventcontract.RuntimeSchemaVersionV1,
	}
}

func runtimeOwnershipFixture() data.RuntimeOutboxOwnership {
	return data.RuntimeOutboxOwnership{
		Shard:      3,
		InstanceID: "relay-1",
		Epoch:      9,
		OwnerToken: "relay-1:9",
		TTL:        15 * time.Second,
	}
}
