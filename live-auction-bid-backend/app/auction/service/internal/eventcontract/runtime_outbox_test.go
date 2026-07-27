package eventcontract

import (
	"errors"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const runtimeFactFixtureEventID = "018f22f2-c640-7f5a-8c8a-9af2b3459e71"

func TestRuntimeOutboxRoundTripPreservesFactAndProducesStableKafkaBytes(t *testing.T) {
	fact := validFact(t)
	item, err := EncodeRuntimeOutboxItem(fact)
	if err != nil {
		t.Fatalf("EncodeRuntimeOutboxItem: %v", err)
	}
	if !strings.HasPrefix(item, fact.GetEventId()+"\n{") {
		t.Fatalf("outbox item does not use the fenced event prefix: %q", item)
	}
	decoded, err := DecodeRuntimeOutboxItem(item)
	if err != nil {
		t.Fatalf("DecodeRuntimeOutboxItem: %v", err)
	}
	equal, err := RuntimeFactBinaryEqual(fact, decoded)
	if err != nil {
		t.Fatalf("RuntimeFactBinaryEqual: %v", err)
	}
	if !equal {
		t.Fatalf("decoded fact differs from input\ninput=%v\ndecoded=%v", fact, decoded)
	}
	first, err := MarshalRuntimeFactBinary(decoded)
	if err != nil {
		t.Fatalf("MarshalRuntimeFactBinary: %v", err)
	}
	second, err := MarshalRuntimeFactBinary(decoded)
	if err != nil {
		t.Fatalf("MarshalRuntimeFactBinary second: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Kafka protobuf encoding must be deterministic")
	}
}

func TestDecodeRuntimeOutboxItemRejectsMalformedAndConflictingEnvelopes(t *testing.T) {
	fact := validFact(t)
	item, err := EncodeRuntimeOutboxItem(fact)
	if err != nil {
		t.Fatal(err)
	}
	other := validFact(t)
	tests := []struct {
		name string
		item string
	}{
		{name: "empty", item: ""},
		{name: "missing separator", item: fact.GetEventId()},
		{name: "missing payload", item: fact.GetEventId() + "\n"},
		{name: "invalid prefix", item: "not-a-v7\n{}"},
		{name: "carriage return", item: fact.GetEventId() + "\r\n{}"},
		{name: "event mismatch", item: other.GetEventId() + item[strings.IndexByte(item, '\n'):]},
		{name: "unknown field", item: strings.TrimSuffix(item, "}") + `,"future_unrecognized":true}`},
		{name: "oversized", item: fact.GetEventId() + "\n" + strings.Repeat("x", maxRuntimeOutboxEnvelopeBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRuntimeOutboxItem(test.item); !errors.Is(err, ErrInvalidRuntimeOutboxItem) {
				t.Fatalf("error=%v want ErrInvalidRuntimeOutboxItem", err)
			}
		})
	}
}

func TestRuntimeOutboxMarshalRejectsInvalidFacts(t *testing.T) {
	fact := validFact(t)
	fact.StateAfter = nil
	if _, err := MarshalRuntimeFactJSON(fact); !errors.Is(err, ErrInvalidRuntimeOutboxItem) {
		t.Fatalf("JSON error=%v want ErrInvalidRuntimeOutboxItem", err)
	}
	if _, err := MarshalRuntimeFactBinary(fact); !errors.Is(err, ErrInvalidRuntimeOutboxItem) {
		t.Fatalf("binary error=%v want ErrInvalidRuntimeOutboxItem", err)
	}
	if _, err := EncodeRuntimeOutboxItem(fact); !errors.Is(err, ErrInvalidRuntimeOutboxItem) {
		t.Fatalf("encode error=%v want ErrInvalidRuntimeOutboxItem", err)
	}
}

func TestRuntimeFactBinaryEqualDetectsPayloadChanges(t *testing.T) {
	left := validFact(t)
	right := proto.Clone(left).(*v1.RuntimeFactV1)
	right.TraceId = "trace-2"
	equal, err := RuntimeFactBinaryEqual(left, right)
	if err != nil {
		t.Fatalf("RuntimeFactBinaryEqual: %v", err)
	}
	if equal {
		t.Fatal("changed runtime fact must not compare equal")
	}
}

func TestDecodeRuntimeOutboxItemAcceptsLuaJSONContractFixture(t *testing.T) {
	payload, err := os.ReadFile("testdata/runtime_fact_v1.lua.json")
	if err != nil {
		t.Fatalf("read Lua contract fixture: %v", err)
	}
	fact, err := DecodeRuntimeOutboxItem(runtimeFactFixtureEventID + "\n" + string(payload))
	if err != nil {
		t.Fatalf("DecodeRuntimeOutboxItem Lua fixture: %v", err)
	}
	if fact.GetEventId() != runtimeFactFixtureEventID {
		t.Fatalf("event_id=%q want %q", fact.GetEventId(), runtimeFactFixtureEventID)
	}
	if fact.GetCommand() != v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID {
		t.Fatalf("command=%v want PLACE_BID", fact.GetCommand())
	}
	if fact.GetStateAfter().GetCurrentPriceFen() != 12_000 {
		t.Fatalf("current_price_fen=%d want 12000", fact.GetStateAfter().GetCurrentPriceFen())
	}
	if fact.GetAcceptedBid().GetBidId() != "bid-1" {
		t.Fatalf("bid_id=%q want bid-1", fact.GetAcceptedBid().GetBidId())
	}
}
