package data

import (
	"encoding/json"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/biz/shop"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

func TestApplyAuctionOrderEnrichmentExposesPendingWithoutLegacySnapshots(t *testing.T) {
	order := &auction.Order{
		ID: "order-1", ShopName: "legacy", ShippingAddressID: "legacy-address",
		ShippingAddressSnapshot: &shop.DeliveryAddressSnapshot{AddressID: "legacy-address"},
	}
	if err := applyAuctionOrderEnrichment(order, AuctionOrderEnrichmentModel{}, false); err != nil {
		t.Fatal(err)
	}
	if order.EnrichmentStatus != orderenrichment.StatusPending || order.ShopName != "" || order.ShippingAddressSnapshot != nil {
		t.Fatalf("pending order = %+v", order)
	}
}

func TestApplyAuctionOrderEnrichmentComposesReadySnapshots(t *testing.T) {
	addressJSON, _ := json.Marshal(orderenrichment.AddressSnapshot{
		AddressID: "address-1", ReceiverName: "Buyer", Phone: "13800000000", Detail: "1号", FullAddress: "杭州市1号",
	})
	shopJSON, _ := json.Marshal(orderenrichment.ShopSnapshot{ShopID: "main-1", ShopName: "Seller"})
	model := AuctionOrderEnrichmentModel{
		OrderID: "order-1", SourceMessageID: "message-1", PayloadHash: string(make([]byte, 64)),
		AddressSnapshot: string(addressJSON), ShopSnapshot: string(shopJSON), Status: string(orderenrichment.StatusReady), UpdatedAtUnixMs: 100,
	}
	order := &auction.Order{ID: "order-1"}
	if err := applyAuctionOrderEnrichment(order, model, true); err != nil {
		t.Fatal(err)
	}
	if order.EnrichmentStatus != orderenrichment.StatusReady || order.ShopName != "Seller" || order.ShippingAddressID != "address-1" || order.EnrichmentUpdatedAtMs != 100 {
		t.Fatalf("ready order = %+v", order)
	}
}

func TestDecodeOrderEnrichmentRejectsIncompleteReadyRow(t *testing.T) {
	model := AuctionOrderEnrichmentModel{
		OrderID: "order-1", SourceMessageID: "message-1", PayloadHash: string(make([]byte, 64)),
		Status: string(orderenrichment.StatusReady), UpdatedAtUnixMs: 100,
	}
	if _, _, _, err := decodeOrderEnrichment(model); err == nil {
		t.Fatal("incomplete READY enrichment was accepted")
	}
	model.Status = string(orderenrichment.StatusPartial)
	if _, _, status, err := decodeOrderEnrichment(model); err != nil || status != orderenrichment.StatusPartial {
		t.Fatalf("empty PARTIAL enrichment status=%q error=%v", status, err)
	}
}

func TestApplyUserOrderEnrichmentKeepsShopOrdersReady(t *testing.T) {
	order := &shop.UserOrder{ID: "shop-order", Source: shop.OrderSourceShop, ShopName: "Native shop"}
	if err := applyUserOrderEnrichment(order, AuctionOrderEnrichmentModel{}, false); err != nil {
		t.Fatal(err)
	}
	if order.EnrichmentStatus != orderenrichment.StatusReady || order.ShopName != "Native shop" {
		t.Fatalf("shop order = %+v", order)
	}
}
