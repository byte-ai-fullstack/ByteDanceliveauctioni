package data

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestBuildPreStartLotStateOutboxMessageProducesCanonicalEvent(t *testing.T) {
	lot := preStartLotStateFixture()
	message, err := buildPreStartLotStateOutboxMessage(context.Background(), lot, 1_700_000_000_000)
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if message.PartitionKey != lot.GetId() || message.CreatedAtMs != 1_700_000_000_000 || len(message.HeadersJSON) == 0 {
		t.Fatalf("message=%+v", message)
	}
	event := new(v1.LotStateDomainEventV1)
	if err := proto.Unmarshal(message.Payload, event); err != nil {
		t.Fatal(err)
	}
	if err := eventcontract.ValidateLotStateDomainEvent(event); err != nil {
		t.Fatalf("validate event: %v", err)
	}
	if event.GetMetadata().GetMessageId() != message.MessageID || event.GetMetadata().GetCausationId() != message.CausationID ||
		event.GetMetadata().GetTraceId() != message.CausationID || event.GetLotVersion() != lot.GetVersion() {
		t.Fatalf("event=%+v message=%+v", event, message)
	}
}

func TestBuildPreStartLotStateOutboxMessageRejectsIncompleteDocument(t *testing.T) {
	lot := preStartLotStateFixture()
	lot.Title = ""
	if _, err := buildPreStartLotStateOutboxMessage(context.Background(), lot, 1); !errors.Is(err, errLotStateDocumentIncomplete) {
		t.Fatalf("error=%v", err)
	}
}

func preStartLotStateFixture() *v1.Lot {
	return &v1.Lot{
		Id: "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", Version: 2,
		Status: v1.LotStatus_LOT_STATUS_QUEUED, Title: "翡翠手镯", Description: "冰糯种", Category: "珠宝",
		Tags: []string{"翡翠", "手镯"}, ImageUrl: "https://example.test/lot.jpg",
		Rule:         &v1.BidRule{StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}},
		CurrentPrice: &v1.Money{Amount: 10_000, Currency: "CNY"},
	}
}
