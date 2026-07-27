package data

import (
	"encoding/json"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
)

func TestOrderToModelUsesJSONNullForMissingShippingAddressSnapshot(t *testing.T) {
	model, _, err := auctionOrderToUserModels(auction.Order{
		ID:              "order-1",
		MainAccountID:   "main-1",
		LotID:           "lot-1",
		RoomID:          "room-1",
		LotTitle:        "test lot",
		LotImageURL:     "https://example.test/lot.png",
		BuyerUserID:     "buyer-1",
		BuyerNickname:   "buyer",
		Status:          auction.OrderStatusPendingPayment,
		PaymentStatus:   auction.PaymentStatusInit,
		Amount:          5000,
		Currency:        "CNY",
		CreatedAtUnixMs: 1,
		UpdatedAtUnixMs: 1,
		ExpiresAtUnixMs: 2,
		Version:         1,
	})
	if err != nil {
		t.Fatalf("orderToModel() error = %v", err)
	}
	if model.ShippingAddressSnapshot != "null" {
		t.Fatalf("missing address snapshot should be persisted as JSON null, got %q", model.ShippingAddressSnapshot)
	}
	if !json.Valid([]byte(model.ShippingAddressSnapshot)) {
		t.Fatalf("shipping address snapshot should be valid JSON, got %q", model.ShippingAddressSnapshot)
	}
}

func TestUserModelToAuctionOrderWithItemAcceptsOrderDraftPayload(t *testing.T) {
	payload, err := json.Marshal(map[string]string{
		"orderId":         "order-legacy",
		"mainAccountId":   "main-payload",
		"buyerUserId":     "buyer-payload",
		"buyerNickname":   "payload buyer",
		"title":           "payload title",
		"imageUrl":        "https://example.test/payload.png",
		"totalAmountFen":  "9000",
		"currency":        "CNY",
		"createdAtUnixMs": "123",
	})
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}
	order, err := userModelToAuctionOrderWithItem(&UserOrderModel{
		ID:              "order-legacy",
		MainAccountID:   "main-model",
		UserID:          "buyer-model",
		Nickname:        "model buyer",
		Status:          "pending_payment",
		PaymentStatus:   "init",
		Title:           "model title",
		TotalAmount:     11000,
		Currency:        "CNY",
		CreatedAtUnixMs: 456,
		UpdatedAtUnixMs: 789,
		ExpiresAtUnixMs: 999,
		Version:         7,
		SourcePayload:   string(payload),
	}, []UserOrderItemModel{{
		OrderID:  "order-legacy",
		LotID:    "lot-1",
		RoomID:   "room-1",
		Title:    "item title",
		ImageURL: "https://example.test/item.png",
	}})
	if err != nil {
		t.Fatalf("userModelToAuctionOrderWithItem() error = %v", err)
	}
	if order.ID != "order-legacy" || order.MainAccountID != "main-model" || order.BuyerUserID != "buyer-model" {
		t.Fatalf("authoritative model fields not applied: %+v", order)
	}
	if order.Amount != 11000 || order.CreatedAtUnixMs != 456 || order.UpdatedAtUnixMs != 789 || order.Version != 7 {
		t.Fatalf("canonical order columns not preserved: %+v", order)
	}
	if order.LotID != "lot-1" || order.RoomID != "room-1" || order.LotTitle != "item title" || order.LotImageURL != "https://example.test/item.png" {
		t.Fatalf("order item overlay missing: %+v", order)
	}
}

func TestUserModelToAuctionOrderRejectsInvalidLegacyOrderDraftPayload(t *testing.T) {
	_, err := userModelToAuctionOrder(&UserOrderModel{
		ID:            "order-legacy",
		MainAccountID: "main-model",
		UserID:        "buyer-model",
		SourcePayload: `{"orderId":"order-legacy","createdAtUnixMs":"oops"}`,
	})
	if err == nil {
		t.Fatal("invalid legacy payload was accepted")
	}
}
