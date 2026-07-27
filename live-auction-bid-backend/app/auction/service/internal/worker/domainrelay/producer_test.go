package domainrelay

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

func TestBuildDomainRecordValidatesEveryTopicContract(t *testing.T) {
	for _, topic := range []string{
		eventcontract.BidAcceptedTopicV1,
		eventcontract.LotSettledTopicV1,
		eventcontract.OrderCreatedTopicV1,
		eventcontract.LotStateTopicV1,
		eventcontract.OrderEnrichmentTopicV1,
	} {
		t.Run(topic, func(t *testing.T) {
			message := domainMessageFixture(t, topic)
			record, err := BuildDomainRecord(message)
			if err != nil {
				t.Fatalf("BuildDomainRecord: %v", err)
			}
			if record.Topic != message.Topic || string(record.Key) != message.PartitionKey || !reflect.DeepEqual(record.Value, message.Payload) || !record.Timestamp.Equal(time.UnixMilli(message.CreatedAtMs).UTC()) {
				t.Fatalf("record=%+v message=%+v", record, message)
			}
			if !sortIsStable(record.Headers) {
				t.Fatalf("headers are not sorted: %v", record.Headers)
			}
		})
	}
}

func TestBuildDomainRecordRejectsPoisonRows(t *testing.T) {
	valid := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	tests := []struct {
		name   string
		mutate func(*Message)
	}{
		{"topic", func(message *Message) { message.Topic = eventcontract.DomainDLQTopicV1 }},
		{"message id", func(message *Message) { message.MessageID = "different" }},
		{"key", func(message *Message) { message.PartitionKey = "other-lot" }},
		{"payload", func(message *Message) { message.Payload = []byte("not-protobuf") }},
		{"headers JSON", func(message *Message) { message.HeadersJSON = []byte("[") }},
		{"header identity", func(message *Message) {
			var headers map[string]string
			_ = json.Unmarshal(message.HeadersJSON, &headers)
			headers[eventcontract.DomainHeaderMessageID] = "different"
			message.HeadersJSON, _ = json.Marshal(headers)
		}},
		{"metadata", func(message *Message) {
			event := new(v1.BidAcceptedDomainEventV1)
			_ = proto.Unmarshal(message.Payload, event)
			event.Metadata.TraceId = "other-trace"
			message.Payload, _ = proto.MarshalOptions{Deterministic: true}.Marshal(event)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := valid
			message.Payload = append([]byte(nil), valid.Payload...)
			message.HeadersJSON = append([]byte(nil), valid.HeadersJSON...)
			test.mutate(&message)
			if _, err := BuildDomainRecord(message); !errors.Is(err, ErrInvalidDomainMessage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestBuildDeadLetterRecordPreservesIdentityAndBoundsPayload(t *testing.T) {
	message := domainMessageFixture(t, eventcontract.LotStateTopicV1)
	message.MessageID = "invalid\nmessage"
	message.Topic = "invalid\ntopic"
	message.Payload = make([]byte, maxDeadLetterPayloadSample+100)
	for index := range message.Payload {
		message.Payload[index] = byte(index)
	}
	failedAt := time.UnixMilli(1_700_000_100_000)
	record, err := BuildDeadLetterRecord(message, 5, "invalid\nprotobuf", failedAt)
	if err != nil {
		t.Fatalf("BuildDeadLetterRecord: %v", err)
	}
	if record.Topic != eventcontract.DomainDLQTopicV1 || string(record.Key) != "outbox-"+"1" || !record.Timestamp.Equal(failedAt.UTC()) {
		t.Fatalf("record=%+v", record)
	}
	var envelope deadLetterEnvelope
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.MessageID != message.MessageID || envelope.OriginalTopic != message.Topic || envelope.OriginalPayloadBytes != len(message.Payload) || !envelope.OriginalPayloadTrimmed || len(envelope.OriginalPayloadSample) != maxDeadLetterPayloadSample || strings.ContainsAny(envelope.Failure, "\r\n") {
		t.Fatalf("envelope=%+v", envelope)
	}
	if len(envelope.OriginalPayloadSHA256) != 64 {
		t.Fatalf("payload hash=%q", envelope.OriginalPayloadSHA256)
	}
	if _, err := BuildDeadLetterRecord(Message{}, 0, "", time.Time{}); !errors.Is(err, ErrInvalidDomainMessage) {
		t.Fatalf("invalid dead letter error=%v", err)
	}
}

func TestKafkaProducerWaitsForACKAndCloses(t *testing.T) {
	client := &domainKafkaClientStub{}
	producer := &KafkaProducer{client: client}
	message := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	if err := producer.ProduceDomain(context.Background(), message); err != nil {
		t.Fatalf("ProduceDomain: %v", err)
	}
	if err := producer.ProduceDeadLetter(context.Background(), message, 3, "failed", time.UnixMilli(message.CreatedAtMs+1)); err != nil {
		t.Fatalf("ProduceDeadLetter: %v", err)
	}
	if len(client.records) != 2 || client.records[1].Topic != eventcontract.DomainDLQTopicV1 {
		t.Fatalf("records=%v", client.records)
	}
	producer.Close()
	if !client.closed {
		t.Fatal("Close did not close client")
	}

	client.produceErr = errors.New("broker unavailable")
	if err := producer.ProduceDomain(context.Background(), message); !errors.Is(err, ErrDomainProduce) {
		t.Fatalf("produce error=%v", err)
	}
	if err := producer.ProduceDeadLetter(context.Background(), message, 3, "failed", time.Now()); !errors.Is(err, ErrDomainProduce) {
		t.Fatalf("DLQ produce error=%v", err)
	}
}

func TestKafkaProducerRejectsInvalidConfigAndUninitializedUse(t *testing.T) {
	if _, err := NewKafkaProducer(context.Background(), kafkaclient.Config{ClientID: "relay"}); !errors.Is(err, kafkaclient.ErrInvalidConfig) {
		t.Fatalf("config error=%v", err)
	}
	message := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	var producer *KafkaProducer
	if err := producer.ProduceDomain(context.Background(), message); err == nil {
		t.Fatal("nil producer returned no error")
	}
	if err := producer.ProduceDeadLetter(context.Background(), message, 1, "failure", time.Now()); err == nil {
		t.Fatal("nil producer returned no DLQ error")
	}
	producer.Close()
}

func domainMessageFixture(t *testing.T, topic string) Message {
	t.Helper()
	causationID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := eventcontract.DomainMessageID(causationID, topic)
	if err != nil {
		t.Fatal(err)
	}
	createdAtMs := int64(1_700_000_000_000)
	metadata := &v1.DomainEventMetadataV1{
		MessageId: messageID, CausationId: causationID, TraceId: "trace-1", SchemaVersion: 1, OccurredAtUnixMs: createdAtMs,
	}
	var event proto.Message
	partitionKey := "lot-1"
	switch topic {
	case eventcontract.BidAcceptedTopicV1:
		event = &v1.BidAcceptedDomainEventV1{Metadata: metadata, LotId: "lot-1", RoomId: "room-1", BidId: "bid-1", UserId: "user-1", AmountFen: 12_000, Currency: "CNY", LotVersion: 7}
	case eventcontract.LotSettledTopicV1:
		event = &v1.LotSettledDomainEventV1{Metadata: metadata, LotId: "lot-1", RoomId: "room-1", WinnerUserId: "user-1", FinalPriceFen: 12_000, Currency: "CNY", OrderId: "order-1", LotVersion: 7}
	case eventcontract.OrderCreatedTopicV1:
		partitionKey = "order-1"
		event = &v1.OrderCreatedDomainEventV1{Metadata: metadata, OrderId: "order-1", LotId: "lot-1", RoomId: "room-1", BuyerUserId: "user-1", TotalAmountFen: 12_000, Currency: "CNY", LotVersion: 7}
	case eventcontract.LotStateTopicV1:
		state := &v1.LotStateDomainEventV1{
			Metadata: metadata, LotId: "lot-1", RoomId: "room-1", MainAccountId: "account-1", LotVersion: 7,
			Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "Jade vase", Description: "Vintage carved vase",
			Category: "jewelry", Tags: []string{"jade"}, CurrentPriceFen: 12_000, StartPriceFen: 10_000,
			Currency: "CNY", StartsAtUnixMs: createdAtMs - 1_000, EndsAtUnixMs: createdAtMs + 60_000,
		}
		state.ContentHash, err = eventcontract.LotStateContentHash(state)
		if err != nil {
			t.Fatal(err)
		}
		event = state
	case eventcontract.OrderEnrichmentTopicV1:
		partitionKey = "order-1"
		event = &v1.OrderEnrichmentRequestedDomainEventV1{Metadata: metadata, OrderId: "order-1", LotId: "lot-1"}
	default:
		t.Fatalf("unsupported fixture topic %q", topic)
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := json.Marshal(map[string]string{
		eventcontract.RuntimeHeaderContentType:   eventcontract.DomainEventContentType,
		eventcontract.DomainHeaderMessageID:      messageID,
		eventcontract.DomainHeaderCausationID:    causationID,
		eventcontract.RuntimeHeaderTraceID:       "trace-1",
		eventcontract.RuntimeHeaderSchemaVersion: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return Message{ID: 1, MessageID: messageID, CausationID: causationID, Topic: topic, PartitionKey: partitionKey, Payload: payload, HeadersJSON: headers, CreatedAtMs: createdAtMs}
}

func sortIsStable(headers []kgo.RecordHeader) bool {
	for index := 1; index < len(headers); index++ {
		if headers[index-1].Key > headers[index].Key {
			return false
		}
	}
	return true
}

type domainKafkaClientStub struct {
	records    []*kgo.Record
	produceErr error
	closed     bool
}

func (*domainKafkaClientStub) Ping(context.Context) error { return nil }

func (client *domainKafkaClientStub) ProduceSync(_ context.Context, records ...*kgo.Record) kgo.ProduceResults {
	client.records = append(client.records, records...)
	results := make(kgo.ProduceResults, 0, len(records))
	for _, record := range records {
		results = append(results, kgo.ProduceResult{Record: record, Err: client.produceErr})
	}
	return results
}

func (client *domainKafkaClientStub) Close() { client.closed = true }
