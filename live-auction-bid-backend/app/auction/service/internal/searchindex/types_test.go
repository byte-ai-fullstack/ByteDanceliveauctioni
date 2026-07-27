package searchindex

import (
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestStableEmbeddingHashIgnoresRuntimePriceAndStatus(t *testing.T) {
	document := LotDocument{
		LotID: "lot-1", MainAccountID: "merchant-1", Title: "Jade vase", Description: "Vintage carved vase",
		Category: "jewelry", Tags: []string{"vintage", "jade"}, Status: "LIVE",
		CurrentPrice: &v1.Money{Amount: 12_000, Currency: "CNY"}, LotVersion: 7,
	}
	first := document.StableEmbeddingHash("dashscope", "text-embedding-v4", "2026-06", 1024)
	document.Status = "EXTENDED"
	document.CurrentPrice.Amount = 15_000
	document.LotVersion++
	document.Tags = []string{"jade", "vintage"}
	second := document.StableEmbeddingHash("dashscope", "text-embedding-v4", "2026-06", 1024)
	if first != second || len(first) != 64 {
		t.Fatalf("runtime-only change altered embedding hash: first=%q second=%q", first, second)
	}
	document.Description = "Different description"
	if changed := document.StableEmbeddingHash("dashscope", "text-embedding-v4", "2026-06", 1024); changed == first {
		t.Fatal("stable searchable content change did not alter embedding hash")
	}
}

func TestLotDocumentFromDomainEventBuildsStableDocument(t *testing.T) {
	event := &v1.LotStateDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{CausationId: "event-1"},
		LotId:    "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: 4,
		Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "Jade vase", Description: "Vintage", Category: "jewelry",
		Tags: []string{"jade"}, ImageUrl: "https://example.test/lot.png", StartPriceFen: 10_000,
		CurrentPriceFen: 12_000, Currency: "CNY", StartsAtUnixMs: 100, EndsAtUnixMs: 200, ContentHash: "hash",
	}
	document := LotDocumentFromDomainEvent(event)
	if document.LotID != "lot-1" || document.LastEventID != "event-1" || document.LotVersion != 4 ||
		!document.PublicVisible || document.StartPrice.GetAmount() != 10_000 || document.CurrentPrice.GetAmount() != 12_000 ||
		document.SearchText != "Jade vase\nVintage\njewelry\njade" {
		t.Fatalf("document=%+v", document)
	}
	if got := LotDocumentFromDomainEvent(nil); got.LotID != "" {
		t.Fatalf("nil event document=%+v", got)
	}
}

func TestBuildStableSearchTextSkipsEmptyFields(t *testing.T) {
	if got := BuildStableSearchText(LotDocument{Title: " Lot ", Tags: []string{"", "jade"}}); got != "Lot\njade" {
		t.Fatalf("search text=%q", got)
	}
}
