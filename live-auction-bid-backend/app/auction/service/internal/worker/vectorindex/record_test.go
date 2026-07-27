package vectorindex

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestDecodeRecordValidatesLotStateEnvelope(t *testing.T) {
	record := validLotStateKafkaRecord(t)
	decoded, err := DecodeRecord(record)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if decoded.MessageID == "" || decoded.Document.LotID != "lot-1" || decoded.Document.LotVersion != 7 || decoded.Document.LastEventID != decoded.CausationID {
		t.Fatalf("decoded=%+v", decoded)
	}

	for name, mutate := range map[string]func(*kgo.Record){
		"topic": func(value *kgo.Record) { value.Topic = "wrong" },
		"key":   func(value *kgo.Record) { value.Key = []byte("other") },
		"body":  func(value *kgo.Record) { value.Value = []byte("invalid") },
		"message header": func(value *kgo.Record) {
			value.Headers = append(value.Headers, kgo.RecordHeader{Key: eventcontract.DomainHeaderMessageID, Value: []byte("duplicate")})
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := cloneKafkaRecord(record)
			mutate(invalid)
			if _, err := DecodeRecord(invalid); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func validLotStateKafkaRecord(t *testing.T) *kgo.Record {
	t.Helper()
	causationID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := eventcontract.DomainMessageID(causationID, eventcontract.LotStateTopicV1)
	if err != nil {
		t.Fatal(err)
	}
	event := &v1.LotStateDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{
			MessageId: messageID, CausationId: causationID, TraceId: "trace-1", SchemaVersion: 1, OccurredAtUnixMs: 1_700_000_000_000,
		},
		LotId: "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: 7,
		Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "Jade vase", Description: "Vintage", Category: "jewelry",
		Tags: []string{"jade"}, StartPriceFen: 10_000, CurrentPriceFen: 12_000, Currency: "CNY",
		StartsAtUnixMs: 1_700_000_000_000, EndsAtUnixMs: 1_700_000_060_000,
	}
	event.ContentHash, err = eventcontract.LotStateContentHash(event)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: eventcontract.LotStateTopicV1, Partition: 1, Offset: 7, Key: []byte("lot-1"), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.DomainEventContentType)},
			{Key: eventcontract.DomainHeaderMessageID, Value: []byte(messageID)},
			{Key: eventcontract.DomainHeaderCausationID, Value: []byte(causationID)},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte("trace-1")},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
		},
	}
}

func cloneKafkaRecord(source *kgo.Record) *kgo.Record {
	clone := *source
	clone.Key = append([]byte(nil), source.Key...)
	clone.Value = append([]byte(nil), source.Value...)
	clone.Headers = append([]kgo.RecordHeader(nil), source.Headers...)
	return &clone
}
