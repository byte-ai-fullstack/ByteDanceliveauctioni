package test

import (
	"context"
	"strings"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/aiassistant"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"
)

func TestBuyerAIConsultOnlyUsesPublicVisibleLots(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	svc := appsvc.NewAuctionService(uc).SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"}))
	ctx := context.Background()

	store.rooms["room-public"] = auction.Room{ID: "room-public", MainAccountID: testMainAccountID, Name: "翡翠专场", Platform: "douyin", Status: auction.RoomStatusActive}
	store.rooms["room-private"] = auction.Room{ID: "room-private", MainAccountID: testMainAccountID, Name: "草稿专场", Platform: "douyin", Status: auction.RoomStatusActive}
	store.lots["lot-live"] = aiTestLot("lot-live", "room-public", "冰糯翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
	store.lots["lot-queued"] = aiTestLot("lot-queued", "room-public", "和田玉吊坠", v1.LotStatus_LOT_STATUS_QUEUED)
	store.lots["lot-draft"] = aiTestLot("lot-draft", "room-private", "后台草稿翡翠", v1.LotStatus_LOT_STATUS_DRAFT)
	store.lots["lot-cancelled"] = aiTestLot("lot-cancelled", "room-public", "已取消翡翠", v1.LotStatus_LOT_STATUS_CANCELLED)

	reply, err := svc.ConsultBuyer(ctx, &v1.BuyerConsultRequest{Query: "想看翡翠", Budget: 2000000})
	if err != nil {
		t.Fatalf("consult buyer returned transport error: %v", err)
	}
	if reply.Result.GetCode() != appsvc.ResultCodeOK {
		t.Fatalf("consult buyer failed: %+v", reply.Result)
	}
	gotIDs := make([]string, 0, len(reply.Results))
	for _, result := range reply.Results {
		gotIDs = append(gotIDs, result.GetLotId())
	}
	joined := strings.Join(gotIDs, ",")
	if !strings.Contains(joined, "lot-live") {
		t.Fatalf("expected live lot in results, got %v", gotIDs)
	}
	if strings.Contains(joined, "lot-draft") || strings.Contains(joined, "lot-cancelled") {
		t.Fatalf("private or terminal lots must not leak into buyer AI results: %v", gotIDs)
	}
}

func TestBuyerAIConsultParsesBudgetAndRanksWithinBudget(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	svc := appsvc.NewAuctionService(uc).SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"}))
	ctx := context.Background()

	store.rooms["room-public"] = auction.Room{ID: "room-public", MainAccountID: testMainAccountID, Name: "翡翠专场", Platform: "douyin", Status: auction.RoomStatusActive}
	store.lots["lot-budget"] = aiTestLot("lot-budget", "room-public", "入门翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
	store.lots["lot-over"] = aiTestLot("lot-over", "room-public", "高货翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
	store.lots["lot-budget"].CurrentPrice = &v1.Money{Amount: 30000, Currency: "CNY"}
	store.lots["lot-budget"].Rule.StartPrice = &v1.Money{Amount: 10000, Currency: "CNY"}
	store.lots["lot-over"].CurrentPrice = &v1.Money{Amount: 90000, Currency: "CNY"}
	store.lots["lot-over"].Rule.StartPrice = &v1.Money{Amount: 80000, Currency: "CNY"}

	reply, err := svc.ConsultBuyer(ctx, &v1.BuyerConsultRequest{Query: "预算500以内翡翠手镯"})
	if err != nil {
		t.Fatalf("consult buyer returned transport error: %v", err)
	}
	if len(reply.Results) < 2 {
		t.Fatalf("expected budget results, got %+v", reply.Results)
	}
	if reply.Results[0].GetLotId() != "lot-budget" {
		t.Fatalf("expected budget-matching lot first, got %s", reply.Results[0].GetLotId())
	}
	if !strings.Contains(reply.Results[0].GetReason(), "预算内") {
		t.Fatalf("expected budget reason, got %q", reply.Results[0].GetReason())
	}
}

func TestBuyerAIConsultLiveIntentIncludesExtendedButNotQueued(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	svc := appsvc.NewAuctionService(uc).SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"}))
	ctx := context.Background()

	store.rooms["room-public"] = auction.Room{ID: "room-public", MainAccountID: testMainAccountID, Name: "直播竞拍场", Platform: "douyin", Status: auction.RoomStatusActive}
	store.lots["lot-live"] = aiTestLot("lot-live", "room-public", "正在拍翡翠", v1.LotStatus_LOT_STATUS_LIVE)
	store.lots["lot-extended"] = aiTestLot("lot-extended", "room-public", "加时翡翠", v1.LotStatus_LOT_STATUS_EXTENDED)
	store.lots["lot-queued"] = aiTestLot("lot-queued", "room-public", "待开拍翡翠", v1.LotStatus_LOT_STATUS_QUEUED)

	reply, err := svc.ConsultBuyer(ctx, &v1.BuyerConsultRequest{Query: "正在竞拍的拍品"})
	if err != nil {
		t.Fatalf("consult buyer returned transport error: %v", err)
	}
	gotIDs := make([]string, 0, len(reply.Results))
	for _, result := range reply.Results {
		gotIDs = append(gotIDs, result.GetLotId())
	}
	joined := strings.Join(gotIDs, ",")
	if !strings.Contains(joined, "lot-live") || !strings.Contains(joined, "lot-extended") {
		t.Fatalf("expected live and extended lots, got %v", gotIDs)
	}
	if strings.Contains(joined, "lot-queued") {
		t.Fatalf("queued lot should not match live intent: %v", gotIDs)
	}
}

func TestBuyerAIConsultFiltersByRoomName(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	svc := appsvc.NewAuctionService(uc).SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"}))
	ctx := context.Background()

	store.rooms["room-ggboy"] = auction.Room{ID: "room-ggboy", MainAccountID: testMainAccountID, Name: "Ggboy", Platform: "douyin", Status: auction.RoomStatusActive}
	store.rooms["room-yexieer"] = auction.Room{ID: "room-yexieer", MainAccountID: testMainAccountID, Name: "Yexieer", Platform: "douyin", Status: auction.RoomStatusActive}
	store.lots["lot-ggboy"] = aiTestLot("lot-ggboy", "room-ggboy", "Ggboy 翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
	store.lots["lot-yexieer"] = aiTestLot("lot-yexieer", "room-yexieer", "Yexieer 翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)

	reply, err := svc.ConsultBuyer(ctx, &v1.BuyerConsultRequest{Query: "Ggboy直播间有什么"})
	if err != nil {
		t.Fatalf("consult buyer returned transport error: %v", err)
	}
	if len(reply.Results) != 1 || reply.Results[0].GetLotId() != "lot-ggboy" {
		t.Fatalf("expected only Ggboy lot, got %+v", reply.Results)
	}
}

func aiTestLot(id, roomID, title string, status v1.LotStatus) *v1.Lot {
	return &v1.Lot{
		Id:            id,
		RoomId:        roomID,
		MainAccountId: testMainAccountID,
		Title:         title,
		Description:   title + "，适合直播竞拍",
		ImageUrl:      "https://example.com/" + id + ".jpg",
		Status:        status,
		Rule: &v1.BidRule{
			StartPrice:      &v1.Money{Amount: 1000000, Currency: "CNY"},
			MinIncrement:    &v1.Money{Amount: 100000, Currency: "CNY"},
			DurationSeconds: 300,
		},
		CurrentPrice: &v1.Money{Amount: 1800000, Currency: "CNY"},
		TrustCards: []*v1.TrustRevealCard{{
			Id:       id + "-card",
			LotId:    id,
			Title:    "证书卡",
			Content:  "天然材质证书",
			Revealed: false,
		}},
	}
}
