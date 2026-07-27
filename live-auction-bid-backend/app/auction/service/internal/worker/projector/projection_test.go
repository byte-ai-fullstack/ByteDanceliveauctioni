package projector

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestBuildProjectionDerivesDeterministicBidAndSearchMessages(t *testing.T) {
	fact := runtimeRecordFact(t)
	metadata := projectionLotMetadata(t)

	first, err := BuildProjection(fact, metadata)
	if err != nil {
		t.Fatalf("BuildProjection: %v", err)
	}
	second, err := BuildProjection(proto.Clone(fact).(*v1.RuntimeFactV1), metadata)
	if err != nil {
		t.Fatalf("BuildProjection repeated: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same fact did not produce byte-identical projection")
	}
	if first.CanonicalStateHash == "" || len(first.CanonicalStateHash) != 32 {
		t.Fatalf("canonical state hash=%q", first.CanonicalStateHash)
	}
	if len(first.DomainMessages) != 2 {
		t.Fatalf("message count=%d want 2", len(first.DomainMessages))
	}

	bidMessage := first.DomainMessages[0]
	assertDomainEnvelope(t, bidMessage, fact, eventcontract.BidAcceptedTopicV1, fact.GetLotId())
	bid := new(v1.BidAcceptedDomainEventV1)
	if err := proto.Unmarshal(bidMessage.Payload, bid); err != nil {
		t.Fatalf("decode bid accepted: %v", err)
	}
	if bid.GetBidId() != "bid-1" || bid.GetAmountFen() != 12_000 || bid.GetLotVersion() != 7 {
		t.Fatalf("bid accepted=%v", bid)
	}

	stateMessage := first.DomainMessages[1]
	assertDomainEnvelope(t, stateMessage, fact, eventcontract.LotStateTopicV1, fact.GetLotId())
	state := new(v1.LotStateDomainEventV1)
	if err := proto.Unmarshal(stateMessage.Payload, state); err != nil {
		t.Fatalf("decode lot state: %v", err)
	}
	if state.GetMainAccountId() != "merchant-1" || state.GetCategory() != "jewelry" ||
		!reflect.DeepEqual(state.GetTags(), []string{"jade", "vintage"}) ||
		state.GetStartPriceFen() != fact.GetStateAfter().GetStartPriceFen() ||
		state.GetStartsAtUnixMs() != fact.GetStateAfter().GetStartedAtUnixMs() {
		t.Fatalf("lot search document=%v", state)
	}
	wantContentHash := state.GetContentHash()
	gotContentHash, err := eventcontract.LotStateContentHash(state)
	if err != nil {
		t.Fatalf("PayloadHash: %v", err)
	}
	if gotContentHash != wantContentHash {
		t.Fatalf("content hash=%q want %q", wantContentHash, gotContentHash)
	}

	first.DomainMessages[0].HeadersJSON[0] ^= 0xff
	if string(first.DomainMessages[0].HeadersJSON) == string(first.DomainMessages[1].HeadersJSON) {
		t.Fatal("domain message headers share mutable storage")
	}
}

func TestBuildProjectionDerivesSettlementAndOrderMessagesInStableOrder(t *testing.T) {
	fact := runtimeRecordFact(t)
	fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED
	fact.AcceptedBid = nil
	fact.StateAfter.Status = v1.LotStatus_LOT_STATUS_SETTLED
	fact.StateAfter.WinnerUserId = "buyer-1"
	fact.StateAfter.WinnerNickname = "Buyer"
	fact.StateAfter.FinalPriceFen = 12_000
	fact.StateAfter.OrderId = "order-1"
	fact.StateAfter.SettledAtUnixMs = fact.GetOccurredAtUnixMs()
	fact.OrderDraft = &v1.OrderDraftV1{
		OrderId:         "order-1",
		LotId:           fact.GetLotId(),
		RoomId:          fact.GetRoomId(),
		MainAccountId:   "merchant-1",
		BuyerUserId:     "buyer-1",
		BuyerNickname:   "Buyer",
		Title:           "Jade vase",
		ImageUrl:        "https://example.test/lot.png",
		TotalAmountFen:  12_000,
		Currency:        "CNY",
		CreatedAtUnixMs: fact.GetOccurredAtUnixMs(),
	}

	projection, err := BuildProjection(fact, projectionLotMetadata(t))
	if err != nil {
		t.Fatalf("BuildProjection: %v", err)
	}
	wantTopics := []string{
		eventcontract.LotSettledTopicV1,
		eventcontract.OrderCreatedTopicV1,
		eventcontract.LotStateTopicV1,
		eventcontract.OrderEnrichmentTopicV1,
	}
	if len(projection.DomainMessages) != len(wantTopics) {
		t.Fatalf("message count=%d want %d", len(projection.DomainMessages), len(wantTopics))
	}
	for index, topic := range wantTopics {
		if projection.DomainMessages[index].Topic != topic {
			t.Fatalf("message %d topic=%q want %q", index, projection.DomainMessages[index].Topic, topic)
		}
	}

	settled := new(v1.LotSettledDomainEventV1)
	if err := proto.Unmarshal(projection.DomainMessages[0].Payload, settled); err != nil {
		t.Fatalf("decode settled: %v", err)
	}
	if settled.GetWinnerUserId() != "buyer-1" || settled.GetOrderId() != "order-1" {
		t.Fatalf("settled=%v", settled)
	}
	created := new(v1.OrderCreatedDomainEventV1)
	if err := proto.Unmarshal(projection.DomainMessages[1].Payload, created); err != nil {
		t.Fatalf("decode order created: %v", err)
	}
	if created.GetOrderId() != "order-1" || created.GetBuyerUserId() != "buyer-1" {
		t.Fatalf("order created=%v", created)
	}
	enrichment := new(v1.OrderEnrichmentRequestedDomainEventV1)
	if err := proto.Unmarshal(projection.DomainMessages[3].Payload, enrichment); err != nil {
		t.Fatalf("decode enrichment: %v", err)
	}
	if enrichment.GetOrderId() != "order-1" || enrichment.GetLotId() != fact.GetLotId() {
		t.Fatalf("enrichment=%v", enrichment)
	}
}

func TestBuildProjectionRejectsInvalidFactOrMetadata(t *testing.T) {
	validMetadata := projectionLotMetadata(t)
	tests := []struct {
		name     string
		fact     func() *v1.RuntimeFactV1
		metadata func() LotMetadata
	}{
		{name: "invalid fact", fact: func() *v1.RuntimeFactV1 { return nil }, metadata: func() LotMetadata { return validMetadata }},
		{name: "lot identity", fact: func() *v1.RuntimeFactV1 { return runtimeRecordFact(t) }, metadata: func() LotMetadata {
			value := validMetadata
			value.LotID = "other"
			return value
		}},
		{name: "room identity", fact: func() *v1.RuntimeFactV1 { return runtimeRecordFact(t) }, metadata: func() LotMetadata {
			value := validMetadata
			value.RoomID = "other"
			return value
		}},
		{name: "missing account", fact: func() *v1.RuntimeFactV1 { return runtimeRecordFact(t) }, metadata: func() LotMetadata {
			value := validMetadata
			value.MainAccountID = " "
			return value
		}},
		{name: "malformed lot payload", fact: func() *v1.RuntimeFactV1 { return runtimeRecordFact(t) }, metadata: func() LotMetadata {
			value := validMetadata
			value.LotPayloadJSON = []byte("{")
			return value
		}},
		{name: "payload lot identity", fact: func() *v1.RuntimeFactV1 { return runtimeRecordFact(t) }, metadata: func() LotMetadata {
			value := validMetadata
			value.LotPayloadJSON = []byte(`{"id":"other"}`)
			return value
		}},
		{name: "order account identity", fact: func() *v1.RuntimeFactV1 {
			fact := runtimeRecordFact(t)
			fact.OrderDraft = &v1.OrderDraftV1{MainAccountId: "other"}
			return fact
		}, metadata: func() LotMetadata { return validMetadata }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildProjection(test.fact(), test.metadata()); !errors.Is(err, ErrInvalidProjection) {
				t.Fatalf("error=%v want ErrInvalidProjection", err)
			}
		})
	}
}

func TestProjectionHelpersRejectUnsupportedDomainMessage(t *testing.T) {
	if err := setDomainMetadata(&structpb.Struct{}, &v1.DomainEventMetadataV1{}); err == nil {
		t.Fatal("unsupported domain message was accepted")
	}
	lot, err := metadataLotDocument(LotMetadata{})
	if err != nil || !proto.Equal(lot, &v1.Lot{}) {
		t.Fatalf("empty metadata lot=%v error=%v", lot, err)
	}
}

func assertDomainEnvelope(t *testing.T, message DomainMessage, fact *v1.RuntimeFactV1, topic, key string) {
	t.Helper()
	wantMessageID, err := eventcontract.DomainMessageID(fact.GetEventId(), topic)
	if err != nil {
		t.Fatalf("DomainMessageID: %v", err)
	}
	if message.MessageID != wantMessageID || message.CausationID != fact.GetEventId() ||
		message.Topic != topic || message.PartitionKey != key || message.CreatedAtMs != fact.GetOccurredAtUnixMs() {
		t.Fatalf("domain envelope=%+v", message)
	}
	var headers map[string]string
	if err := json.Unmarshal(message.HeadersJSON, &headers); err != nil {
		t.Fatalf("decode headers: %v", err)
	}
	wantHeaders := map[string]string{
		eventcontract.RuntimeHeaderContentType:   eventcontract.DomainEventContentType,
		eventcontract.DomainHeaderMessageID:      wantMessageID,
		eventcontract.DomainHeaderCausationID:    fact.GetEventId(),
		eventcontract.RuntimeHeaderTraceID:       fact.GetTraceId(),
		eventcontract.RuntimeHeaderSchemaVersion: "1",
	}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers=%v want %v", headers, wantHeaders)
	}
}

func projectionLotMetadata(t *testing.T) LotMetadata {
	t.Helper()
	payload, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(&v1.Lot{
		Id:       "lot-1",
		RoomId:   "room-1",
		Category: "jewelry",
		Tags:     []string{"jade", "vintage"},
	})
	if err != nil {
		t.Fatalf("marshal lot metadata: %v", err)
	}
	return LotMetadata{
		LotID:          "lot-1",
		RoomID:         "room-1",
		MainAccountID:  "merchant-1",
		Title:          "Jade vase",
		Description:    "Vintage carved jade vase",
		ImageURL:       "https://example.test/lot.png",
		LotPayloadJSON: payload,
	}
}
