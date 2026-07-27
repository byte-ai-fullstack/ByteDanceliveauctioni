package enrichment

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const enrichmentTestCausationID = "01890f3e-8b7a-7cc2-98c4-dc0c0c07398f"

func TestDecodeRecordValidatesDomainIdentity(t *testing.T) {
	record := validKafkaRecord(t)
	decoded, err := DecodeRecord(record)
	if err != nil {
		t.Fatalf("DecodeRecord() error = %v", err)
	}
	if decoded.Event.GetOrderId() != "order-1" || decoded.MessageID == "" || len(decoded.PayloadHash) != 64 {
		t.Fatalf("decoded record = %+v", decoded)
	}
	if &decoded.Payload[0] == &record.Value[0] {
		t.Fatal("decoded payload aliases Kafka buffer")
	}
}

func TestDecodeRecordRejectsHeaderAndPayloadConflicts(t *testing.T) {
	tests := map[string]func(*kgo.Record){
		"wrong key": func(record *kgo.Record) { record.Key = []byte("other-order") },
		"duplicate message header": func(record *kgo.Record) {
			record.Headers = append(record.Headers, kgo.RecordHeader{Key: eventcontract.DomainHeaderMessageID, Value: []byte("other")})
		},
		"wrong content type": func(record *kgo.Record) { record.Headers[0].Value = []byte("application/json") },
		"invalid protobuf":   func(record *kgo.Record) { record.Value = []byte{0xff} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := validKafkaRecord(t)
			mutate(record)
			if _, err := DecodeRecord(record); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("DecodeRecord() error = %v", err)
			}
		})
	}
}

func validKafkaRecord(t *testing.T) *kgo.Record {
	t.Helper()
	messageID, err := eventcontract.DomainMessageID(enrichmentTestCausationID, eventcontract.OrderEnrichmentTopicV1)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&v1.OrderEnrichmentRequestedDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{
			MessageId: messageID, CausationId: enrichmentTestCausationID, TraceId: "trace-1", SchemaVersion: 1, OccurredAtUnixMs: 1_700_000_000_000,
		},
		OrderId: "order-1", LotId: "lot-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: eventcontract.OrderEnrichmentTopicV1, Partition: 1, Offset: 7, Key: []byte("order-1"), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.DomainEventContentType)},
			{Key: eventcontract.DomainHeaderMessageID, Value: []byte(messageID)},
			{Key: eventcontract.DomainHeaderCausationID, Value: []byte(enrichmentTestCausationID)},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte("trace-1")},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
		},
	}
}
