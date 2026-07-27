package projector

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestDecodeRecordValidatesAndCopiesRuntimeFact(t *testing.T) {
	record := runtimeRecordFixture(t)
	originalPayload := append([]byte(nil), record.Value...)
	wantFact := new(v1.RuntimeFactV1)
	if err := proto.Unmarshal(originalPayload, wantFact); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	decoded, err := DecodeRecord(record)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if decoded.Topic != record.Topic || decoded.Partition != 4 || decoded.Offset != 99 || decoded.OwnerEpoch != 8 || decoded.OutboxShard != 3 {
		t.Fatalf("decoded metadata=%+v", decoded)
	}
	if decoded.PayloadHash == "" || len(decoded.PayloadHash) != 64 {
		t.Fatalf("payload hash=%q", decoded.PayloadHash)
	}
	if !proto.Equal(decoded.Fact, wantFact) {
		t.Fatalf("decoded fact=%v", decoded.Fact)
	}
	record.Value[0] ^= 0xff
	if string(decoded.Payload) != string(originalPayload) {
		t.Fatal("decoded payload aliases mutable Kafka record memory")
	}
}

func TestDecodeRecordRejectsBrokenMetadataAndContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*kgo.Record)
	}{
		{name: "wrong topic", mutate: func(record *kgo.Record) { record.Topic = "other" }},
		{name: "negative partition", mutate: func(record *kgo.Record) { record.Partition = -1 }},
		{name: "negative offset", mutate: func(record *kgo.Record) { record.Offset = -1 }},
		{name: "empty payload", mutate: func(record *kgo.Record) { record.Value = nil }},
		{name: "oversized payload", mutate: func(record *kgo.Record) { record.Value = make([]byte, eventcontract.MaxRuntimeFactBytes+1) }},
		{name: "malformed protobuf", mutate: func(record *kgo.Record) { record.Value = []byte{0xff} }},
		{name: "invalid fact", mutate: func(record *kgo.Record) {
			fact := runtimeRecordFact(t)
			fact.LotVersion++
			record.Value, _ = proto.Marshal(fact)
		}},
		{name: "wrong key", mutate: func(record *kgo.Record) { record.Key = []byte("other-lot") }},
		{name: "missing header", mutate: func(record *kgo.Record) { record.Headers = record.Headers[1:] }},
		{name: "duplicate header", mutate: func(record *kgo.Record) { record.Headers = append(record.Headers, record.Headers[0]) }},
		{name: "content type mismatch", mutate: replaceHeader(eventcontract.RuntimeHeaderContentType, "application/json")},
		{name: "event id mismatch", mutate: replaceHeader(eventcontract.RuntimeHeaderEventID, "other")},
		{name: "trace id mismatch", mutate: replaceHeader(eventcontract.RuntimeHeaderTraceID, "other")},
		{name: "schema mismatch", mutate: replaceHeader(eventcontract.RuntimeHeaderSchemaVersion, "01")},
		{name: "lot version mismatch", mutate: replaceHeader(eventcontract.RuntimeHeaderLotVersion, "8")},
		{name: "owner epoch zero", mutate: replaceHeader(eventcontract.RuntimeHeaderOwnerEpoch, "0")},
		{name: "owner epoch noncanonical", mutate: replaceHeader(eventcontract.RuntimeHeaderOwnerEpoch, "08")},
		{name: "outbox shard negative", mutate: replaceHeader(eventcontract.RuntimeHeaderOutboxShard, "-1")},
		{name: "outbox shard too high", mutate: replaceHeader(eventcontract.RuntimeHeaderOutboxShard, "16")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := runtimeRecordFixture(t)
			test.mutate(record)
			if _, err := DecodeRecord(record); !errors.Is(err, ErrInvalidRuntimeRecord) {
				t.Fatalf("error=%v want ErrInvalidRuntimeRecord", err)
			}
		})
	}
	if _, err := DecodeRecord(nil); !errors.Is(err, ErrInvalidRuntimeRecord) {
		t.Fatalf("nil error=%v want ErrInvalidRuntimeRecord", err)
	}
}

func TestRuntimeHeaderParserAllowsUntrackedHeadersButRequiresTrackedOnes(t *testing.T) {
	record := runtimeRecordFixture(t)
	record.Headers = append(record.Headers, kgo.RecordHeader{Key: "future_optional", Value: []byte("value")})
	if _, err := DecodeRecord(record); err != nil {
		t.Fatalf("future header should be ignored: %v", err)
	}
	if _, err := runtimeHeaders(nil); !errors.Is(err, ErrInvalidRuntimeRecord) {
		t.Fatalf("empty headers error=%v", err)
	}
}

func replaceHeader(name, value string) func(*kgo.Record) {
	return func(record *kgo.Record) {
		for index := range record.Headers {
			if record.Headers[index].Key == name {
				record.Headers[index].Value = []byte(value)
				return
			}
		}
	}
}

func runtimeRecordFixture(t *testing.T) *kgo.Record {
	t.Helper()
	fact := runtimeRecordFact(t)
	payload, err := eventcontract.MarshalRuntimeFactBinary(fact)
	if err != nil {
		t.Fatalf("MarshalRuntimeFactBinary: %v", err)
	}
	return &kgo.Record{
		Topic:     eventcontract.RuntimeProjectionTopicV1,
		Partition: 4,
		Offset:    99,
		Key:       []byte(fact.GetLotId()),
		Value:     payload,
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.RuntimeFactContentType)},
			{Key: eventcontract.RuntimeHeaderEventID, Value: []byte(fact.GetEventId())},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte(fact.GetTraceId())},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
			{Key: eventcontract.RuntimeHeaderLotVersion, Value: []byte("7")},
			{Key: eventcontract.RuntimeHeaderOwnerEpoch, Value: []byte("8")},
			{Key: eventcontract.RuntimeHeaderOutboxShard, Value: []byte("3")},
		},
	}
}

func runtimeRecordFact(t *testing.T) *v1.RuntimeFactV1 {
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
		AcceptedBid:    &v1.AcceptedBidV1{BidId: "bid-1", UserId: "user-1", Nickname: "User 1", AmountFen: 12_000, AcceptedAtUnixMs: 1_700_000_000_000},
		IdempotencyKey: "idem-1",
		SchemaVersion:  eventcontract.RuntimeSchemaVersionV1,
	}
}
