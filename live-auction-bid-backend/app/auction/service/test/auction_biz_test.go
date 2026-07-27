package test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/clock"
)

func TestEventForViewerRedactsPublicSettlementAndBuyerIdentity(t *testing.T) {
	event := &v1.AuctionEvent{
		Type:   v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED,
		Reason: "order_id=order-secret",
		Lot: &v1.Lot{
			Id:              "lot_privacy",
			Status:          v1.LotStatus_LOT_STATUS_SETTLED,
			CurrentPrice:    &v1.Money{Amount: 12000, Currency: "CNY"},
			LeadingUserId:   "buyer1",
			LeadingNickname: "买家一号",
			WinnerUserId:    "buyer1",
			WinnerNickname:  "买家一号",
			FinalPrice:      &v1.Money{Amount: 12000, Currency: "CNY"},
		},
		Bid: &v1.Bid{
			UserId:   "buyer1",
			Nickname: "买家一号",
			Amount:   &v1.Money{Amount: 12000, Currency: "CNY"},
		},
		Ranking: []*v1.RankingItem{{
			Rank:     1,
			UserId:   "buyer1",
			Nickname: "买家一号",
			Amount:   &v1.Money{Amount: 12000, Currency: "CNY"},
		}},
	}

	publicEvent := auction.EventForViewer(event, auction.LotResultViewer{})
	if publicEvent.Reason != "" {
		t.Fatalf("public order event reason should not leak order data: %q", publicEvent.Reason)
	}
	if publicEvent.GetLot().GetFinalPrice().GetAmount() != 12000 || publicEvent.GetLot().GetWinnerUserId() != "" || publicEvent.GetLot().GetWinnerNickname() != "买***" || publicEvent.GetLot().GetLeadingNickname() != "" {
		t.Fatalf("public settlement lot should keep final price and masked winner nickname but hide buyer id: %+v", publicEvent.GetLot())
	}
	if publicEvent.GetBid().GetUserId() != "" || publicEvent.GetBid().GetNickname() != "买***" {
		t.Fatalf("public bid should mask buyer identity: %+v", publicEvent.GetBid())
	}
	if publicEvent.GetRanking()[0].GetUserId() != "" || publicEvent.GetRanking()[0].GetNickname() != "买***" {
		t.Fatalf("public ranking should mask buyer identity: %+v", publicEvent.GetRanking())
	}

	winnerEvent := auction.EventForViewer(event, auction.LotResultViewer{UserID: "buyer1", RoleCodes: []string{userbiz.RoleBuyer}, PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleBuyer)})
	if winnerEvent.GetLot().GetWinnerUserId() != "buyer1" || winnerEvent.GetBid().GetUserId() != "buyer1" || winnerEvent.GetRanking()[0].GetUserId() != "buyer1" {
		t.Fatalf("winning buyer should see own identity: lot=%+v bid=%+v ranking=%+v", winnerEvent.GetLot(), winnerEvent.GetBid(), winnerEvent.GetRanking())
	}
}

func TestBuildRanking(t *testing.T) {
	bids := []*v1.Bid{
		{UserId: "u1", Nickname: "用户1", Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, CreatedAtUnixMs: 1000},
		{UserId: "u2", Nickname: "用户2", Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, CreatedAtUnixMs: 2000},
		{UserId: "u1", Nickname: "用户1", Amount: &v1.Money{Amount: 13000, Currency: "CNY"}, CreatedAtUnixMs: 3000},
	}

	ranking := auction.BuildRanking(bids)
	if len(ranking) != 2 {
		t.Fatalf("期望 2 个用户，实际 %d", len(ranking))
	}
	if ranking[0].UserId != "u1" || ranking[0].GetAmount().GetAmount() != 13000 {
		t.Fatalf("排名第一错误：%+v", ranking[0])
	}
	if ranking[1].UserId != "u2" || ranking[1].GetAmount().GetAmount() != 12000 {
		t.Fatalf("排名第二错误：%+v", ranking[1])
	}
}

func TestCreateLotRejectsMismatchedCurrency(t *testing.T) {
	_, err := auction.NewLotFromRequest("lot_1", &v1.CreateLotRequest{
		RoomId:   "demo",
		Title:    "测试拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Currency: "CNY", Amount: 10000},
			MinIncrement:           &v1.Money{Currency: "USD", Amount: 1000},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "currency must match") {
		t.Fatalf("expected currency mismatch error, got %v", err)
	}
}

func TestCreateLotValidatesRequiredImageAndCapPrice(t *testing.T) {
	base := &v1.CreateLotRequest{
		RoomId:   "demo",
		Title:    "封顶价测试拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
			CapPrice:               &v1.Money{Amount: 50000, Currency: "CNY"},
		},
	}
	lot, err := auction.NewLotFromRequest("lot_cap", base)
	if err != nil {
		t.Fatalf("valid cap price should pass: %v", err)
	}
	if lot.GetRule().GetCapPrice().GetAmount() != 50000 {
		t.Fatalf("cap price should be kept on lot: %+v", lot.GetRule().GetCapPrice())
	}

	missingImage := proto.Clone(base).(*v1.CreateLotRequest)
	missingImage.ImageUrl = ""
	if _, err := auction.NewLotFromRequest("lot_no_image", missingImage); err == nil || !strings.Contains(err.Error(), "image url") {
		t.Fatalf("missing image should be rejected, got %v", err)
	}

	badCap := proto.Clone(base).(*v1.CreateLotRequest)
	badCap.Rule.CapPrice = &v1.Money{Amount: 9000, Currency: "CNY"}
	if _, err := auction.NewLotFromRequest("lot_bad_cap", badCap); err == nil || !strings.Contains(err.Error(), "greater than start price") {
		t.Fatalf("cap <= start should be rejected, got %v", err)
	}
}

func TestCreateLotKeepsAddLotDetailFields(t *testing.T) {
	lot, err := auction.NewLotFromRequest("lot_detail", &v1.CreateLotRequest{
		RoomId:           "demo",
		Title:            "添加拍品详情测试",
		Description:      "带图库和保障卡的拍品",
		ImageUrl:         "https://tos.example.com/main.jpg",
		GalleryImageUrls: []string{" https://tos.example.com/gallery-a.jpg ", "https://tos.example.com/gallery-b.jpg"},
		Category:         "珠宝首饰",
		Tags:             []string{" 翡翠 ", "收藏级"},
		EstimatePrice:    &v1.Money{Amount: 280000, Currency: "CNY"},
		Stock:            3,
		AfterSaleNotes:   "支持复检",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
		TrustCards: []*v1.TrustRevealCard{{
			Type:     v1.TrustCardType_TRUST_CARD_TYPE_CERTIFICATE,
			Title:    "证书",
			Content:  "NGTC 可查",
			ImageUrl: "https://tos.example.com/cert.jpg",
		}},
	})
	if err != nil {
		t.Fatalf("create lot with add-lot detail fields failed: %v", err)
	}
	if lot.GetGalleryImageUrls()[0] != "https://tos.example.com/gallery-a.jpg" || lot.GetCategory() != "珠宝首饰" || lot.GetTags()[0] != "翡翠" {
		t.Fatalf("detail fields should be normalized and kept: %+v", lot)
	}
	if lot.GetEstimatePrice().GetAmount() != 280000 || lot.GetStock() != 3 || lot.GetAfterSaleNotes() != "支持复检" {
		t.Fatalf("price/stock/after-sale fields should be kept: %+v", lot)
	}
	if lot.GetTrustCards()[0].GetImageUrl() != "https://tos.example.com/cert.jpg" || lot.GetTrustCards()[0].GetLotId() != lot.GetId() {
		t.Fatalf("trust card image and identity should be kept: %+v", lot.GetTrustCards()[0])
	}
}

func TestCommittedManagementWritesSurviveRealtimePublishFailure(t *testing.T) {
	store := newTestStore()
	publisher := &testPublisher{err: errors.New("nats unavailable")}
	usecase := auction.NewAuctionUsecase(store, store, store, publisher)
	ctx := context.Background()

	lot := createUsecaseTestLot(t, usecase, ctx, "room-management-publish-failure", "committed management lot")
	persisted, err := store.FindByID(ctx, lot.GetId())
	if err != nil || persisted.GetId() != lot.GetId() {
		t.Fatalf("committed lot missing after realtime failure: lot=%+v error=%v", persisted, err)
	}
	queued, position, err := usecase.QueueLot(ctx, lot.GetId(), testMainAccountID, "test-owner")
	if err != nil || queued.GetStatus() != v1.LotStatus_LOT_STATUS_QUEUED || position <= 0 {
		t.Fatalf("committed queue transition must survive realtime failure: lot=%+v position=%d error=%v", queued, position, err)
	}
}

func TestCreateLotRejectsTemporaryPreviewImageURLs(t *testing.T) {
	base := &v1.CreateLotRequest{
		RoomId:   "demo",
		Title:    "临时图片地址测试",
		ImageUrl: "https://tos.example.com/main.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}

	cases := []struct {
		name    string
		mutate  func(*v1.CreateLotRequest)
		wantErr string
	}{
		{
			name: "main blob",
			mutate: func(req *v1.CreateLotRequest) {
				req.ImageUrl = "blob:http://localhost/preview"
			},
			wantErr: "imageUrl",
		},
		{
			name: "gallery data url",
			mutate: func(req *v1.CreateLotRequest) {
				req.GalleryImageUrls = []string{"data:image/png;base64,abc"}
			},
			wantErr: "galleryImageUrls",
		},
		{
			name: "trust card blob",
			mutate: func(req *v1.CreateLotRequest) {
				req.TrustCards = []*v1.TrustRevealCard{{
					Type:     v1.TrustCardType_TRUST_CARD_TYPE_CERTIFICATE,
					Title:    "证书",
					Content:  "NGTC 可查",
					ImageUrl: "blob:http://localhost/cert",
				}}
			},
			wantErr: "trustCards.imageUrl",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := proto.Clone(base).(*v1.CreateLotRequest)
			tc.mutate(req)
			if _, err := auction.NewLotFromRequest("lot_bad_preview", req); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %s error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestListLotsByQueryViewsRespectPageBoundaries(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	createLotWithStatus := func(id string, status v1.LotStatus) {
		t.Helper()
		lot, err := auction.NewLotFromRequest(id, &v1.CreateLotRequest{
			RoomId:   "room_views",
			Title:    "视图边界 " + id,
			ImageUrl: "https://example.com/" + id + ".jpg",
			Rule: &v1.BidRule{
				StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
				MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
				DurationSeconds:        300,
				AntiSnipeWindowSeconds: 15,
				AntiSnipeExtendSeconds: 15,
				MaxExtendCount:         3,
			},
		})
		if err != nil {
			t.Fatalf("create lot %s failed: %v", id, err)
		}
		lot.Status = status
		if status == v1.LotStatus_LOT_STATUS_QUEUED {
			lot.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED
			lot.QueuePosition = 1
		}
		if status == v1.LotStatus_LOT_STATUS_CANCELLED {
			lot.CancelReason = "误加入队列"
			lot.CancelledAtUnixMs = 1000
		}
		if err := store.Create(ctx, lot, "owner", nil); err != nil {
			t.Fatalf("store lot %s failed: %v", id, err)
		}
	}

	createLotWithStatus("lot_draft", v1.LotStatus_LOT_STATUS_DRAFT)
	createLotWithStatus("lot_ready", v1.LotStatus_LOT_STATUS_READY)
	createLotWithStatus("lot_queued", v1.LotStatus_LOT_STATUS_QUEUED)
	createLotWithStatus("lot_live", v1.LotStatus_LOT_STATUS_LIVE)
	createLotWithStatus("lot_settled", v1.LotStatus_LOT_STATUS_SETTLED)
	createLotWithStatus("lot_cancelled", v1.LotStatus_LOT_STATUS_CANCELLED)
	createLotWithStatus("lot_failed", v1.LotStatus_LOT_STATUS_FAILED)

	library, err := uc.ListLotsByQuery(ctx, auction.LotQuery{MainAccountID: testMainAccountID, RoomID: "room_views", View: "library", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list library failed: %v", err)
	}
	if library.Total != 2 {
		t.Fatalf("library should only include draft/ready, got total=%d lots=%v", library.Total, testLotIDs(library.Lots))
	}

	current, err := uc.ListLotsByQuery(ctx, auction.LotQuery{MainAccountID: testMainAccountID, RoomID: "room_views", View: "current", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list current failed: %v", err)
	}
	if current.Total != 4 {
		t.Fatalf("current should exclude terminal records, got total=%d lots=%v", current.Total, testLotIDs(current.Lots))
	}

	history, err := uc.ListLotsByQuery(ctx, auction.LotQuery{MainAccountID: testMainAccountID, RoomID: "room_views", View: "history", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list history failed: %v", err)
	}
	if history.Total != 3 {
		t.Fatalf("history should include settled/cancelled/failed, got total=%d lots=%v", history.Total, testLotIDs(history.Lots))
	}

	cancelledInLibrary, err := uc.ListLotsByQuery(ctx, auction.LotQuery{MainAccountID: testMainAccountID, RoomID: "room_views", View: "library", Status: v1.LotStatus_LOT_STATUS_CANCELLED, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list cancelled in library failed: %v", err)
	}
	if cancelledInLibrary.Total != 0 {
		t.Fatalf("cancelled lots must not leak into library view, got total=%d", cancelledInLibrary.Total)
	}

	if _, err := uc.ListLotsByQuery(ctx, auction.LotQuery{MainAccountID: testMainAccountID, RoomID: "room_views", View: "unknown"}); err == nil || !strings.Contains(err.Error(), "unsupported lot list view") {
		t.Fatalf("invalid view should failfast, got %v", err)
	}
}

func TestPublicLotReadsRequireActiveRoom(t *testing.T) {
	store := newTestStore()
	store.strictRooms = true
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()
	lot, err := auction.NewLotFromRequest("lot_orphan_room", &v1.CreateLotRequest{
		RoomId:   "orphan-room",
		Title:    "孤儿房间拍品",
		ImageUrl: "https://example.com/orphan.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	})
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if err := store.Create(ctx, lot, "owner", nil); err != nil {
		t.Fatalf("store lot failed: %v", err)
	}

	if _, err := uc.ListLots(ctx, "orphan-room", 0); !apperr.IsNotFound(err) {
		t.Fatalf("public list should reject orphan room, got %v", err)
	}
	if _, err := uc.Snapshot(ctx, "orphan-room"); !apperr.IsNotFound(err) {
		t.Fatalf("snapshot should reject orphan room, got %v", err)
	}
	if _, err := uc.GetLot(ctx, "lot_orphan_room"); !apperr.IsNotFound(err) {
		t.Fatalf("get lot should reject orphan room, got %v", err)
	}
}

func TestPublicRoomsRequireVisibleAuctionContent(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	cases := []struct {
		id         string
		status     auction.RoomStatus
		lotStatus  []v1.LotStatus
		wantPublic bool
	}{
		{id: "empty", status: auction.RoomStatusActive},
		{id: "draft", status: auction.RoomStatusActive, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_DRAFT}},
		{id: "ready", status: auction.RoomStatusActive, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_READY}},
		{id: "terminal", status: auction.RoomStatusActive, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED}},
		{id: "queued", status: auction.RoomStatusActive, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_QUEUED}, wantPublic: true},
		{id: "live", status: auction.RoomStatusActive, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_LIVE}, wantPublic: true},
		{id: "extended", status: auction.RoomStatusActive, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_EXTENDED}, wantPublic: true},
		{id: "disabled-queued", status: auction.RoomStatusDisabled, lotStatus: []v1.LotStatus{v1.LotStatus_LOT_STATUS_QUEUED}},
	}

	wantPublicIDs := make([]string, 0)
	for _, tc := range cases {
		roomID := "room_" + tc.id
		store.rooms[roomID] = auction.Room{
			ID:              roomID,
			MainAccountID:   testMainAccountID,
			Name:            "直播间 " + tc.id,
			Platform:        "douyin",
			Status:          tc.status,
			CreatedByUserID: "owner",
			CreatedAtUnixMs: int64(len(store.rooms) + 1),
			UpdatedAtUnixMs: int64(len(store.rooms) + 1),
		}
		for index, status := range tc.lotStatus {
			store.lots[roomID+"_lot_"+strconv.Itoa(index)] = &v1.Lot{
				Id:            roomID + "_lot_" + strconv.Itoa(index),
				RoomId:        roomID,
				MainAccountId: testMainAccountID,
				Title:         "可见性拍品",
				Status:        status,
			}
		}
		if tc.wantPublic {
			wantPublicIDs = append(wantPublicIDs, roomID)
		}
	}
	sort.Strings(wantPublicIDs)

	publicRooms, err := uc.ListRooms(ctx, auction.RoomQuery{PublicOnly: true, PublicVisibleOnly: true})
	if err != nil {
		t.Fatalf("list public rooms failed: %v", err)
	}
	if got := testRoomIDs(publicRooms); strings.Join(got, ",") != strings.Join(wantPublicIDs, ",") {
		t.Fatalf("public rooms mismatch got=%v want=%v", got, wantPublicIDs)
	}

	adminRooms, err := uc.ListRooms(ctx, auction.RoomQuery{MainAccountID: testMainAccountID})
	if err != nil {
		t.Fatalf("list admin rooms failed: %v", err)
	}
	if len(adminRooms) != len(cases) {
		t.Fatalf("admin room list should remain unfiltered, got=%d want=%d ids=%v", len(adminRooms), len(cases), testRoomIDs(adminRooms))
	}
}

func TestBuildRealtimeRankingHonorsEnvLimit(t *testing.T) {
	t.Setenv("AUCTION_REALTIME_RANKING_LIMIT", "2")
	ranking := auction.BuildRealtimeRanking([]*v1.Bid{
		{UserId: "u1", Nickname: "用户1", Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, CreatedAtUnixMs: 1},
		{UserId: "u2", Nickname: "用户2", Amount: &v1.Money{Amount: 13000, Currency: "CNY"}, CreatedAtUnixMs: 2},
		{UserId: "u3", Nickname: "用户3", Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, CreatedAtUnixMs: 3},
	})
	if len(ranking) != 2 {
		t.Fatalf("ranking should be capped to 2, got %d", len(ranking))
	}
	if ranking[0].UserId != "u2" || ranking[1].UserId != "u3" {
		t.Fatalf("ranking should keep sorted top bidders, got %+v", ranking)
	}
}

func TestConcurrentBidSmokeMaintainsLeaderRankingLimitIdempotencyAndCapOrder(t *testing.T) {
	t.Setenv("AUCTION_REALTIME_RANKING_LIMIT", "5")
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	const (
		concurrency  = 100
		startPrice   = int64(10000)
		minIncrement = int64(1000)
	)
	capPrice := startPrice + concurrency*minIncrement
	room, err := uc.EnsureDefaultRoom(ctx, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("ensure default room failed: %v", err)
	}
	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   room.ID,
		Title:    "并发封顶拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: startPrice, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: minIncrement, Currency: "CNY"},
			CapPrice:               &v1.Money{Amount: capPrice, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	publicRooms, err := uc.ListRooms(ctx, auction.RoomQuery{PublicOnly: true, PublicVisibleOnly: true})
	if err != nil || len(publicRooms) != 1 || publicRooms[0].ID != lot.RoomId {
		t.Fatalf("started lot should make room public: rooms=%+v err=%v", publicRooms, err)
	}

	type bidAttempt struct {
		index    int
		userID   string
		key      string
		amount   int64
		bidID    string
		accepted bool
		err      error
		latency  time.Duration
	}
	start := make(chan struct{})
	results := make(chan bidAttempt, concurrency)
	var wg sync.WaitGroup
	for i := 1; i <= concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			attempt := bidAttempt{
				index:  i,
				userID: "buyer-" + strconv.Itoa(i),
				key:    "concurrent-bid-" + strconv.Itoa(i),
				amount: startPrice + int64(i)*minIncrement,
			}
			started := time.Now()
			_, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
				LotId:          lot.Id,
				Amount:         &v1.Money{Amount: attempt.amount, Currency: "CNY"},
				IdempotencyKey: attempt.key,
			}, attempt.userID, "买家"+strconv.Itoa(i))
			attempt.latency = time.Since(started)
			attempt.err = err
			if err == nil && bid != nil {
				attempt.accepted = true
				attempt.bidID = bid.Id
			}
			results <- attempt
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var attempts []bidAttempt
	var highest bidAttempt
	accepted := 0
	rejected := 0
	for attempt := range results {
		attempts = append(attempts, attempt)
		if attempt.accepted {
			accepted++
			if !highest.accepted || attempt.amount > highest.amount {
				highest = attempt
			}
			continue
		}
		rejected++
		if attempt.err == nil {
			t.Fatalf("nil bid with nil error for attempt %+v", attempt)
		}
	}
	if accepted == 0 {
		t.Fatalf("expected at least one accepted bid, attempts=%+v", attempts)
	}
	if !highest.accepted || highest.amount != capPrice || highest.userID != "buyer-100" {
		t.Fatalf("highest accepted bid mismatch: highest=%+v cap=%d", highest, capPrice)
	}

	finalLot, err := store.FindByID(ctx, lot.Id)
	if err != nil {
		t.Fatalf("find final lot failed: %v", err)
	}
	if finalLot.Status != v1.LotStatus_LOT_STATUS_SETTLED || finalLot.WinnerUserId != highest.userID || finalLot.GetFinalPrice().GetAmount() != capPrice {
		t.Fatalf("cap bid should settle with highest accepted bidder: lot=%+v highest=%+v", finalLot, highest)
	}
	bids, err := store.ListByLot(ctx, lot.Id)
	if err != nil {
		t.Fatalf("list bids failed: %v", err)
	}
	if len(bids) != accepted {
		t.Fatalf("accepted count must match persisted bids: accepted=%d persisted=%d", accepted, len(bids))
	}
	ranking := auction.BuildRealtimeRanking(bids)
	if len(ranking) == 0 || len(ranking) > 5 {
		t.Fatalf("realtime ranking should be non-empty and capped to 5, got %d: %+v", len(ranking), ranking)
	}
	for i := 1; i < len(ranking); i++ {
		if ranking[i-1].GetAmount().GetAmount() < ranking[i].GetAmount().GetAmount() {
			t.Fatalf("ranking must be sorted descending: %+v", ranking)
		}
	}
	if ranking[0].UserId != highest.userID || ranking[0].GetAmount().GetAmount() != highest.amount {
		t.Fatalf("ranking leader mismatch: ranking=%+v highest=%+v", ranking[0], highest)
	}

	beforeReplayCount := len(bids)
	_, replayed, replayRanking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId:          lot.Id,
		Amount:         &v1.Money{Amount: highest.amount, Currency: "CNY"},
		IdempotencyKey: highest.key,
	}, highest.userID, "买家"+strconv.Itoa(highest.index))
	if err != nil || replayed == nil || replayed.Id != highest.bidID {
		t.Fatalf("idempotent replay should return original bid: bid=%+v highest=%+v ranking=%+v err=%v", replayed, highest, replayRanking, err)
	}
	afterReplayBids, err := store.ListByLot(ctx, lot.Id)
	if err != nil {
		t.Fatalf("list bids after replay failed: %v", err)
	}
	if len(afterReplayBids) != beforeReplayCount || len(replayRanking) == 0 || len(replayRanking) > 5 {
		t.Fatalf("idempotent replay must not append bid and must keep ranking cap: before=%d after=%d ranking=%d", beforeReplayCount, len(afterReplayBids), len(replayRanking))
	}

	orders, err := uc.ListOrders(ctx, auction.OrderQuery{Page: 1, PageSize: 10})
	if err != nil || orders.Total != 1 || len(orders.Orders) != 1 {
		t.Fatalf("cap settlement should create exactly one order: orders=%+v err=%v", orders, err)
	}
	if orders.Orders[0].BuyerUserID != highest.userID || orders.Orders[0].Amount != capPrice {
		t.Fatalf("created order should belong to highest accepted bidder: order=%+v highest=%+v", orders.Orders[0], highest)
	}

	latencies := make([]time.Duration, 0, len(attempts))
	for _, attempt := range attempts {
		latencies = append(latencies, attempt.latency)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("concurrent bid smoke: total=%d accepted=%d rejected=%d p50=%s p95=%s p99=%s final_price=%d leader=%s ranking_len=%d",
		len(attempts),
		accepted,
		rejected,
		latencies[len(latencies)*50/100],
		latencies[len(latencies)*95/100],
		latencies[len(latencies)*99/100],
		finalLot.GetFinalPrice().GetAmount(),
		finalLot.WinnerUserId,
		len(ranking),
	)
}

func testLotIDs(lots []*v1.Lot) []string {
	ids := make([]string, 0, len(lots))
	for _, lot := range lots {
		ids = append(ids, lot.GetId())
	}
	return ids
}

func testRoomIDs(rooms []auction.Room) []string {
	ids := make([]string, 0, len(rooms))
	for _, room := range rooms {
		ids = append(ids, room.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestPlaceBidRejectsMissingUserInsteadOfDefaulting(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "demo",
		Title:    "测试拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}

	_, _, _, err = uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId:  lot.Id,
		Amount: &v1.Money{Amount: 11000, Currency: "CNY"},
	}, "", "用户1")
	if err == nil || !strings.Contains(err.Error(), "user id is required") {
		t.Fatalf("expected user id error, got %v", err)
	}
}

func TestPlaceBidStatsOnlyCountAcceptedBidsAndUniqueParticipants(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_stats",
		Title:    "统计测试拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "stats-1"}, "u1", "用户1"); err != nil || bid == nil {
		t.Fatalf("first bid failed: bid=%+v err=%v", bid, err)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{LotId: lot.Id, Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, IdempotencyKey: "stats-leading-repeat"}, "u1", "用户1"); err == nil || !errors.Is(err, apperr.ErrBidAlreadyLeading) {
		t.Fatalf("leading bidder repeat should be rejected, got %v", err)
	}
	snapshot, err := uc.Snapshot(ctx, "room_stats")
	if err != nil {
		t.Fatalf("snapshot after rejected repeat failed: %v", err)
	}
	if snapshot.GetCurrentLot().GetStats().GetBidCount() != 1 || snapshot.GetCurrentLot().GetStats().GetParticipantCount() != 1 {
		t.Fatalf("rejected leading repeat must not change stats: %+v", snapshot.GetCurrentLot().GetStats())
	}
	if snapshot.GetLiveSourceUrl() == "" || snapshot.GetLiveStartedAtUnixMs() <= 0 {
		t.Fatalf("snapshot should expose stable live source metadata: %+v", snapshot)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{LotId: lot.Id, Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, IdempotencyKey: "stats-2"}, "u2", "用户2"); err != nil || bid == nil {
		t.Fatalf("second user bid failed: bid=%+v err=%v", bid, err)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{LotId: lot.Id, Amount: &v1.Money{Amount: 13000, Currency: "CNY"}, IdempotencyKey: "stats-3"}, "u1", "用户1"); err != nil || bid == nil {
		t.Fatalf("outbid user should be allowed to bid again: bid=%+v err=%v", bid, err)
	}
	snapshot, err = uc.Snapshot(ctx, "room_stats")
	if err != nil {
		t.Fatalf("snapshot after accepted bids failed: %v", err)
	}
	if snapshot.GetCurrentLot().GetStats().GetBidCount() != 3 || snapshot.GetCurrentLot().GetStats().GetParticipantCount() != 2 {
		t.Fatalf("stats should count accepted bids and unique participants: %+v", snapshot.GetCurrentLot().GetStats())
	}
	for _, item := range snapshot.GetRanking() {
		if item.GetAvatarUrl() == "" {
			t.Fatalf("ranking item should expose stable avatar url: %+v", item)
		}
	}
}

func TestLotSaveRejectsStaleExpectedVersion(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()
	lot, err := auction.NewLotFromRequest("lot_conflict", &v1.CreateLotRequest{
		RoomId:   "demo",
		Title:    "版本冲突测试",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	})
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if err := store.Create(ctx, lot, "", nil); err != nil {
		t.Fatalf("persist lot failed: %v", err)
	}

	fresh, err := store.FindByID(ctx, lot.Id)
	if err != nil {
		t.Fatalf("find lot failed: %v", err)
	}
	stale := proto.Clone(fresh).(*v1.Lot)
	expectedVersion := fresh.Version
	if err := auction.ApplyDraftPatch(fresh, &v1.PatchLotDraftRequest{LotId: lot.Id, Title: "首次修改"}); err != nil {
		t.Fatalf("patch fresh lot failed: %v", err)
	}
	if err := store.Save(ctx, fresh, expectedVersion, nil); err != nil {
		t.Fatalf("first save failed: %v", err)
	}

	if err := auction.ApplyDraftPatch(stale, &v1.PatchLotDraftRequest{LotId: lot.Id, Title: "过期修改"}); err != nil {
		t.Fatalf("patch stale lot failed: %v", err)
	}
	if err := store.Save(ctx, stale, expectedVersion, nil); err == nil || !strings.Contains(err.Error(), "lot version conflict") {
		t.Fatalf("expected stale version conflict, got %v", err)
	}
}

type testStore struct {
	mu                         sync.RWMutex
	lots                       map[string]*v1.Lot
	rooms                      map[string]auction.Room
	roomStates                 map[string]auction.RoomState
	strictRooms                bool
	bidsByLot                  map[string][]*v1.Bid
	idemByScope                map[string]*v1.Bid
	ordersByID                 map[string]auction.Order
	orderIDByLot               map[string]string
	paymentsByOrder            map[string]map[string]auction.Payment
	events                     []*v1.AuctionEvent
	batchFindCalls             int
	singleFindCalls            int
	runtimeStates              map[string]auction.RuntimeState
	runtimeActiveLotByRoom     map[string]string
	runtimeDisplayLotByRoom    map[string]string
	failNextPaymentCommit      error
	beforePaymentCommitFailure func(s *testStore, payment auction.Payment, order auction.Order, events []*v1.AuctionEvent)
}

type runtimeGuardStore struct {
	*testStore
	placeBidRuntimeCalled   bool
	runtimeActiveLotByRoom  map[string]string
	runtimeDisplayLotByRoom map[string]string
}

const testMainAccountID = "main-test"

func newTestStore() *testStore {
	return &testStore{
		lots:                    make(map[string]*v1.Lot),
		rooms:                   make(map[string]auction.Room),
		roomStates:              make(map[string]auction.RoomState),
		bidsByLot:               make(map[string][]*v1.Bid),
		idemByScope:             make(map[string]*v1.Bid),
		ordersByID:              make(map[string]auction.Order),
		orderIDByLot:            make(map[string]string),
		paymentsByOrder:         make(map[string]map[string]auction.Payment),
		runtimeStates:           make(map[string]auction.RuntimeState),
		runtimeActiveLotByRoom:  make(map[string]string),
		runtimeDisplayLotByRoom: make(map[string]string),
	}
}

func (s *runtimeGuardStore) ActiveRuntimeLotID(_ context.Context, roomID string) (string, bool, error) {
	lotID := s.runtimeActiveLotByRoom[roomID]
	return lotID, lotID != "", nil
}

func (s *runtimeGuardStore) DisplayedRuntimeLotID(_ context.Context, roomID string) (string, bool, error) {
	lotID := s.runtimeDisplayLotByRoom[roomID]
	return lotID, lotID != "", nil
}

func (s *runtimeGuardStore) PlaceBidRuntime(ctx context.Context, lot *v1.Lot, req *v1.PlaceBidRequest, bidderID, nickname, avatarURL, bidID string, nowMs int64) (auction.RuntimeBidResult, error) {
	s.placeBidRuntimeCalled = true
	bid := &v1.Bid{Id: bidID, LotId: lot.GetId(), UserId: bidderID, Nickname: nickname, AvatarUrl: avatarURL, Amount: req.GetAmount(), CreatedAtUnixMs: nowMs}
	updated := proto.Clone(lot).(*v1.Lot)
	updated.CurrentPrice = req.GetAmount()
	updated.LeadingUserId = bidderID
	updated.LeadingNickname = nickname
	updated.Version++
	return auction.RuntimeBidResult{
		Lot:                updated,
		Bid:                bid,
		Ranking:            []*v1.RankingItem{{Rank: 1, UserId: bidderID, Nickname: nickname, AvatarUrl: avatarURL, Amount: req.GetAmount(), BidAtUnixMs: nowMs}},
		RuntimeEventID:     "rte_test_projection",
		PreviousLotVersion: lot.GetVersion(),
		LotVersion:         updated.GetVersion(),
	}, nil
}

func (s *runtimeGuardStore) SnapshotRuntime(ctx context.Context, current *v1.Lot) (*v1.RoomSnapshot, error) {
	bids, err := s.ListByLot(ctx, current.GetId())
	if err != nil {
		return nil, err
	}
	return &v1.RoomSnapshot{RoomId: current.GetRoomId(), CurrentLot: proto.Clone(current).(*v1.Lot), Ranking: auction.BuildRealtimeRanking(bids), ServerTimeUnixMs: clock.NowMs()}, nil
}

func (s *runtimeGuardStore) RankingRuntime(ctx context.Context, lotID string, limit int64) ([]*v1.RankingItem, error) {
	bids, err := s.ListByLot(ctx, lotID)
	if err != nil {
		return nil, err
	}
	return auction.BuildRealtimeRanking(bids), nil
}

func TestSnapshotUsesRuntimeActiveLotBeforeMySQLProjection(t *testing.T) {
	store := &runtimeGuardStore{
		testStore:              newTestStore(),
		runtimeActiveLotByRoom: map[string]string{"room-runtime": "lot-runtime"},
	}
	store.lots["lot-runtime"] = &v1.Lot{
		Id: "lot-runtime", RoomId: "room-runtime", MainAccountId: testMainAccountID, Title: "投影中的拍品",
		Status: v1.LotStatus_LOT_STATUS_LIVE, Version: 2, CurrentPrice: &v1.Money{Amount: 10_000, Currency: "CNY"},
		Stats: &v1.LotStats{}, Rule: &v1.BidRule{StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"}},
	}
	store.roomStates["room-runtime"] = auction.RoomState{RoomID: "room-runtime", MainAccountID: testMainAccountID}
	uc := auction.NewAuctionUsecase(store, store, store, nil)

	snapshot, err := uc.Snapshot(context.Background(), "room-runtime")
	if err != nil {
		t.Fatalf("snapshot runtime active lot: %v", err)
	}
	if snapshot.GetCurrentLot().GetId() != "lot-runtime" || snapshot.GetCurrentLot().GetVersion() != 2 {
		t.Fatalf("snapshot waited for stale MySQL room state: %+v", snapshot)
	}
}

func TestSnapshotRetainsRuntimeTerminalLotAfterActivePointerIsReleased(t *testing.T) {
	store := &runtimeGuardStore{
		testStore:               newTestStore(),
		runtimeActiveLotByRoom:  map[string]string{},
		runtimeDisplayLotByRoom: map[string]string{"room-terminal": "lot-terminal"},
	}
	store.lots["lot-terminal"] = &v1.Lot{
		Id: "lot-terminal", RoomId: "room-terminal", MainAccountId: testMainAccountID, Title: "已落槌拍品",
		Status: v1.LotStatus_LOT_STATUS_SETTLED, Version: 9,
		CurrentPrice: &v1.Money{Amount: 12_000, Currency: "CNY"}, FinalPrice: &v1.Money{Amount: 12_000, Currency: "CNY"},
		WinnerUserId: "buyer-1", WinnerNickname: "买家一", Stats: &v1.LotStats{BidCount: 3, ParticipantCount: 2},
		Rule: &v1.BidRule{StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"}},
	}
	store.roomStates["room-terminal"] = auction.RoomState{RoomID: "room-terminal", MainAccountID: testMainAccountID}
	uc := auction.NewAuctionUsecase(store, store, store, nil)

	snapshot, err := uc.Snapshot(context.Background(), "room-terminal")
	if err != nil {
		t.Fatalf("snapshot terminal display lot: %v", err)
	}
	if snapshot.GetCurrentLot().GetId() != "lot-terminal" || snapshot.GetCurrentLot().GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED {
		t.Fatalf("terminal snapshot disappeared after active release: %+v", snapshot)
	}
}

func TestSnapshotUsesProjectedDisplayLotWhenRuntimeStateExpired(t *testing.T) {
	store := newTestStore()
	store.lots["lot-projected-terminal"] = &v1.Lot{
		Id: "lot-projected-terminal", RoomId: "room-projected-terminal", MainAccountId: testMainAccountID, Title: "已投影终态",
		Status: v1.LotStatus_LOT_STATUS_CANCELLED, Version: 5,
		CurrentPrice: &v1.Money{Amount: 10_000, Currency: "CNY"},
		Rule:         &v1.BidRule{StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"}},
	}
	store.roomStates["room-projected-terminal"] = auction.RoomState{
		RoomID: "room-projected-terminal", MainAccountID: testMainAccountID, DisplayLotID: "lot-projected-terminal",
	}
	uc := auction.NewAuctionUsecase(store, store, store, nil)

	snapshot, err := uc.Snapshot(context.Background(), "room-projected-terminal")
	if err != nil {
		t.Fatalf("snapshot projected display lot: %v", err)
	}
	if snapshot.GetCurrentLot().GetId() != "lot-projected-terminal" || snapshot.GetCurrentLot().GetStatus() != v1.LotStatus_LOT_STATUS_CANCELLED {
		t.Fatalf("projected terminal snapshot unavailable: %+v", snapshot)
	}
}

func (s *testStore) EnsureDefaultRoom(ctx context.Context, mainAccountID, createdByUserID string, nowMs int64) (*auction.Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, room := range s.rooms {
		if room.MainAccountID == mainAccountID && room.Status == auction.RoomStatusActive {
			return cloneTestRoom(room), nil
		}
	}
	room := auction.Room{
		ID:                  "room_default_" + mainAccountID,
		MainAccountID:       mainAccountID,
		Name:                "默认直播间",
		Platform:            "douyin",
		LiveSourceURL:       auction.LiveSourceURLForRoomID("room_default_" + mainAccountID),
		LiveStartedAtUnixMs: nowMs,
		Status:              auction.RoomStatusActive,
		CreatedByUserID:     createdByUserID,
		CreatedAtUnixMs:     nowMs,
		UpdatedAtUnixMs:     nowMs,
	}
	s.rooms[room.ID] = room
	s.ensureRoomStateLocked(room.ID, room.MainAccountID, nowMs)
	return cloneTestRoom(room), nil
}

func (s *testStore) ListRooms(ctx context.Context, query auction.RoomQuery) ([]auction.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rooms := make([]auction.Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		if query.MainAccountID != "" && room.MainAccountID != query.MainAccountID {
			continue
		}
		if query.PublicOnly && room.Status != auction.RoomStatusActive {
			continue
		}
		if query.PublicVisibleOnly && !s.roomHasPublicVisibleLotLocked(room) {
			continue
		}
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })
	return rooms, nil
}

func (s *testStore) roomHasPublicVisibleLotLocked(room auction.Room) bool {
	for _, lot := range s.lots {
		if lot.RoomId == room.ID && lot.MainAccountId == room.MainAccountID && auction.IsPublicVisibleLotStatus(lot.Status) {
			return true
		}
	}
	return false
}

func (s *testStore) FindRoomByID(ctx context.Context, roomID string) (*auction.Room, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if room, ok := s.rooms[roomID]; ok {
		return cloneTestRoom(room), true, nil
	}
	if s.strictRooms {
		return nil, false, nil
	}
	return &auction.Room{
		ID:                  roomID,
		MainAccountID:       testMainAccountID,
		Name:                "测试直播间",
		Platform:            "douyin",
		LiveSourceURL:       auction.LiveSourceURLForRoomID(roomID),
		LiveStartedAtUnixMs: clock.NowMs(),
		Status:              auction.RoomStatusActive,
	}, true, nil
}

func cloneTestRoom(room auction.Room) *auction.Room {
	next := room
	return &next
}

func cloneTestRoomState(state auction.RoomState) *auction.RoomState {
	next := state
	return &next
}

func (s *testStore) ensureRoomStateLocked(roomID, mainAccountID string, nowMs int64) auction.RoomState {
	if nowMs <= 0 {
		nowMs = clock.NowMs()
	}
	if state, ok := s.roomStates[roomID]; ok {
		return state
	}
	state := auction.RoomState{
		RoomID:            roomID,
		MainAccountID:     mainAccountID,
		NextQueuePosition: 1,
		UpdatedAtUnixMs:   nowMs,
	}
	s.roomStates[roomID] = state
	return state
}

func (s *testStore) releaseActiveLotLocked(lot *v1.Lot) {
	if lot == nil || !testTerminalLotStatus(lot.Status) {
		return
	}
	state, ok := s.roomStates[lot.RoomId]
	if !ok || state.ActiveLotID != lot.Id {
		return
	}
	state.ActiveLotID = ""
	state.DisplayLotID = lot.Id
	state.ActiveLotVersion = 0
	state.UpdatedAtUnixMs = clock.NowMs()
	s.roomStates[lot.RoomId] = state
}

func testTerminalLotStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

func (s *testStore) Create(ctx context.Context, lot *v1.Lot, ownerUserID string, events []*v1.AuctionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lot.MainAccountId == "" {
		lot.MainAccountId = testMainAccountID
	}
	s.ensureRoomStateLocked(lot.RoomId, lot.MainAccountId, clock.NowMs())
	s.lots[lot.Id] = proto.Clone(lot).(*v1.Lot)
	s.events = append(s.events, events...)
	return nil
}

func (s *testStore) Save(ctx context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.lots[lot.Id]
	if !ok {
		return errors.New("lot not found")
	}
	if expectedVersion <= 0 {
		return errors.New("lot expected version is required")
	}
	if current.Version != expectedVersion {
		return apperr.ErrLotVersionConflict
	}
	s.lots[lot.Id] = proto.Clone(lot).(*v1.Lot)
	s.releaseActiveLotLocked(lot)
	s.events = append(s.events, events...)
	return nil
}

func (s *testStore) SaveLotPresentation(ctx context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.lots[lot.GetId()]
	if !ok {
		return errors.New("lot not found")
	}
	if expectedVersion < 0 || current.GetPresentationVersion() != expectedVersion ||
		lot.GetPresentationVersion() != expectedVersion+1 {
		return apperr.ErrLotVersionConflict
	}
	next := proto.Clone(current).(*v1.Lot)
	next.TrustCards = make([]*v1.TrustRevealCard, 0, len(lot.GetTrustCards()))
	for _, card := range lot.GetTrustCards() {
		if card != nil {
			next.TrustCards = append(next.TrustCards, proto.Clone(card).(*v1.TrustRevealCard))
		}
	}
	if lot.GetDuelState() != nil {
		next.DuelState = proto.Clone(lot.GetDuelState()).(*v1.DuelState)
	}
	next.PlaybookStage = lot.GetPlaybookStage()
	next.PresentationVersion = lot.GetPresentationVersion()
	s.lots[lot.GetId()] = next
	s.events = append(s.events, events...)
	return nil
}

func (s *testStore) QueueLotAsNext(ctx context.Context, lotID, mainAccountID, ownerUserID string, nowMs int64) (*v1.Lot, int32, []*v1.AuctionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.lots[lotID]
	if !ok {
		return nil, 0, nil, errors.New("lot not found")
	}
	if current.MainAccountId != mainAccountID {
		return nil, 0, nil, apperr.ErrPermissionDenied
	}
	if current.RoomId == "" {
		return nil, 0, nil, errors.New("room id is required")
	}
	if nowMs <= 0 {
		nowMs = clock.NowMs()
	}
	lot := proto.Clone(current).(*v1.Lot)
	if lot.GetQueueStatus() == v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED && lot.GetQueuePosition() > 0 {
		return proto.Clone(lot).(*v1.Lot), lot.GetQueuePosition(), nil, nil
	}
	state := s.ensureRoomStateLocked(lot.RoomId, lot.MainAccountId, nowMs)
	queuePosition := state.NextQueuePosition
	if queuePosition <= 0 {
		queuePosition = 1
	}
	if err := auction.QueueLot(lot, queuePosition); err != nil {
		return nil, 0, nil, err
	}
	state.NextQueuePosition = queuePosition + 1
	state.UpdatedAtUnixMs = nowMs
	s.roomStates[lot.RoomId] = state
	s.lots[lot.Id] = proto.Clone(lot).(*v1.Lot)
	event := auction.NewAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_QUEUED, lot)
	events := []*v1.AuctionEvent{event}
	s.events = append(s.events, events...)
	return proto.Clone(lot).(*v1.Lot), queuePosition, events, nil
}

func (s *testStore) FindRoomState(ctx context.Context, roomID, mainAccountID string) (*auction.RoomState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureRoomStateLocked(roomID, mainAccountID, clock.NowMs())
	return cloneTestRoomState(state), nil
}

func (s *testStore) AttachAssets(ctx context.Context, ownerUserID string, lot *v1.Lot) error {
	return nil
}

func (s *testStore) FindByID(ctx context.Context, lotID string) (*v1.Lot, error) {
	s.mu.Lock()
	s.singleFindCalls++
	s.mu.Unlock()
	return s.FindCoreByID(ctx, lotID)
}

func (s *testStore) FindByIDs(_ context.Context, lotIDs []string) ([]*v1.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchFindCalls++
	lots := make([]*v1.Lot, 0, len(lotIDs))
	for _, lotID := range lotIDs {
		if lot := s.lots[lotID]; lot != nil {
			lots = append(lots, proto.Clone(lot).(*v1.Lot))
		}
	}
	return lots, nil
}

func (s *testStore) FindCoreByID(ctx context.Context, lotID string) (*v1.Lot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lot, ok := s.lots[lotID]
	if !ok {
		return nil, errors.New("lot not found")
	}
	return proto.Clone(lot).(*v1.Lot), nil
}

func (s *testStore) List(ctx context.Context, roomID string, status v1.LotStatus) ([]*v1.Lot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lots := make([]*v1.Lot, 0, len(s.lots))
	for _, lot := range s.lots {
		if roomID != "" && lot.RoomId != roomID {
			continue
		}
		if status != 0 && lot.Status != status && (status != v1.LotStatus_LOT_STATUS_LIVE || lot.Status != v1.LotStatus_LOT_STATUS_EXTENDED) {
			continue
		}
		lots = append(lots, proto.Clone(lot).(*v1.Lot))
	}
	sort.Slice(lots, func(i, j int) bool { return lots[i].Id < lots[j].Id })
	return lots, nil
}

func (s *testStore) ListLots(ctx context.Context, query auction.LotQuery) (auction.LotList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query.Page, query.PageSize = auction.NormalizePagination(query.Page, query.PageSize)
	lots := make([]*v1.Lot, 0, len(s.lots))
	keyword := strings.ToLower(strings.TrimSpace(query.Keyword))
	for _, lot := range s.lots {
		if query.MainAccountID != "" && lot.MainAccountId != query.MainAccountID {
			continue
		}
		if query.RoomID != "" && lot.RoomId != query.RoomID {
			continue
		}
		if query.Status != v1.LotStatus_LOT_STATUS_UNSPECIFIED && lot.Status != query.Status {
			continue
		}
		if strings.EqualFold(query.View, "current") && !isCurrentLotStatusForTest(lot.Status) {
			continue
		}
		if strings.EqualFold(query.View, "history") && !isHistoryLotStatusForTest(lot.Status) {
			continue
		}
		if strings.EqualFold(query.View, "library") && !isLibraryLotStatusForTest(lot.Status) {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(lot.Id+" "+lot.Title+" "+lot.Description+" "+lot.CancelReason), keyword) {
			continue
		}
		lots = append(lots, proto.Clone(lot).(*v1.Lot))
	}
	sort.Slice(lots, func(i, j int) bool { return lots[i].Id < lots[j].Id })
	total := int64(len(lots))
	start := auction.PageOffset(query.Page, query.PageSize)
	if start >= len(lots) {
		return auction.LotList{Lots: []*v1.Lot{}, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
	}
	end := start + query.PageSize
	if end > len(lots) {
		end = len(lots)
	}
	return auction.LotList{Lots: lots[start:end], Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func isCurrentLotStatusForTest(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_DRAFT, v1.LotStatus_LOT_STATUS_READY, v1.LotStatus_LOT_STATUS_QUEUED, v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED:
		return true
	default:
		return false
	}
}

func isHistoryLotStatusForTest(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

func isLibraryLotStatusForTest(status v1.LotStatus) bool {
	return status == v1.LotStatus_LOT_STATUS_DRAFT || status == v1.LotStatus_LOT_STATUS_READY
}

func (s *testStore) FindOrderByID(ctx context.Context, orderID string) (*auction.Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	order, ok := s.ordersByID[orderID]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return &order, nil
}

func (s *testStore) FindOrderByLot(ctx context.Context, lotID string) (*auction.Order, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	orderID, ok := s.orderIDByLot[lotID]
	if !ok {
		return nil, false, nil
	}
	order := s.ordersByID[orderID]
	return &order, true, nil
}

func (s *testStore) ListOrdersByBuyer(ctx context.Context, buyerUserID string) ([]auction.Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	orders := make([]auction.Order, 0, len(s.ordersByID))
	for _, order := range s.ordersByID {
		if order.BuyerUserID == buyerUserID {
			orders = append(orders, order)
		}
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAtUnixMs > orders[j].CreatedAtUnixMs })
	return orders, nil
}

func (s *testStore) ListOrders(ctx context.Context, query auction.OrderQuery) (auction.OrderList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query.Page, query.PageSize = auction.NormalizePagination(query.Page, query.PageSize)
	orders := make([]auction.OrderSummary, 0, len(s.ordersByID))
	buyer := strings.ToLower(strings.TrimSpace(query.Buyer))
	for _, order := range s.ordersByID {
		if query.MainAccountID != "" && order.MainAccountID != query.MainAccountID {
			continue
		}
		if query.BuyerUserID != "" && order.BuyerUserID != query.BuyerUserID {
			continue
		}
		if query.Status != "" && order.Status != query.Status {
			continue
		}
		if query.LotID != "" && order.LotID != query.LotID {
			continue
		}
		if buyer != "" && !strings.Contains(strings.ToLower(order.BuyerUserID+" "+order.BuyerNickname), buyer) {
			continue
		}
		orders = append(orders, order.Summary())
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAtUnixMs > orders[j].CreatedAtUnixMs })
	total := int64(len(orders))
	start := auction.PageOffset(query.Page, query.PageSize)
	if start >= len(orders) {
		return auction.OrderList{Orders: []auction.OrderSummary{}, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
	}
	end := start + query.PageSize
	if end > len(orders) {
		end = len(orders)
	}
	return auction.OrderList{Orders: orders[start:end], Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *testStore) FindPaymentByIdempotencyKey(ctx context.Context, orderID, key string) (*auction.Payment, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.paymentsByOrder[orderID] == nil {
		return nil, false, nil
	}
	payment, ok := s.paymentsByOrder[orderID][key]
	return &payment, ok, nil
}

func (s *testStore) CommitPaymentSuccess(ctx context.Context, payment auction.Payment, order auction.Order, expectedOrderVersion int64, events []*v1.AuctionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNextPaymentCommit != nil {
		if s.beforePaymentCommitFailure != nil {
			s.beforePaymentCommitFailure(s, payment, order, events)
		}
		err := s.failNextPaymentCommit
		s.failNextPaymentCommit = nil
		s.beforePaymentCommitFailure = nil
		return err
	}
	current, ok := s.ordersByID[order.ID]
	if !ok {
		return apperr.ErrNotFound
	}
	if current.Version != expectedOrderVersion {
		return apperr.ErrLotVersionConflict
	}
	if s.paymentsByOrder[order.ID] == nil {
		s.paymentsByOrder[order.ID] = make(map[string]auction.Payment)
	}
	if _, exists := s.paymentsByOrder[order.ID][payment.IdempotencyKey]; exists {
		return errors.New("payment already exists")
	}
	s.paymentsByOrder[order.ID][payment.IdempotencyKey] = payment
	s.ordersByID[order.ID] = order
	s.events = append(s.events, events...)
	return nil
}

func (s *testStore) PersistEvents(ctx context.Context, events []*v1.AuctionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}

func (s *testStore) ListRoomEvents(ctx context.Context, query auction.RoomEventQuery) (auction.RoomEventList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if query.RoomID == "" {
		return auction.RoomEventList{}, errors.New("room id is required")
	}
	_, pageSize := auction.NormalizePagination(1, query.PageSize)
	offset := 0
	if strings.TrimSpace(query.PageToken) != "" {
		nextOffset, err := strconv.Atoi(query.PageToken)
		if err != nil || nextOffset < 0 {
			return auction.RoomEventList{}, errors.New("invalid page token")
		}
		offset = nextOffset
	}
	events := make([]*v1.AuctionEvent, 0, len(s.events))
	for _, event := range s.events {
		if query.MainAccountID != "" && event.MainAccountId != query.MainAccountID {
			continue
		}
		if event.RoomId == query.RoomID {
			events = append(events, event)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].OccurredAtUnixMs == events[j].OccurredAtUnixMs {
			return events[i].Id > events[j].Id
		}
		return events[i].OccurredAtUnixMs > events[j].OccurredAtUnixMs
	})
	if offset > len(events) {
		offset = len(events)
	}
	end := offset + pageSize
	nextPageToken := ""
	if end < len(events) {
		nextPageToken = strconv.Itoa(end)
	} else {
		end = len(events)
	}
	result := make([]*v1.AuctionEvent, 0, end-offset)
	for _, event := range events[offset:end] {
		result = append(result, proto.Clone(event).(*v1.AuctionEvent))
	}
	return auction.RoomEventList{Events: result, NextPageToken: nextPageToken}, nil
}

func (s *testStore) eventTypes() []v1.AuctionEventType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	types := make([]v1.AuctionEventType, 0, len(s.events))
	for _, event := range s.events {
		types = append(types, event.Type)
	}
	return types
}

func (s *testStore) ListByLot(ctx context.Context, lotID string) ([]*v1.Bid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*v1.Bid(nil), s.bidsByLot[lotID]...), nil
}

func (s *testStore) ListBidRecordsByBuyer(ctx context.Context, buyerUserID string, query auction.BidRecordQuery) (auction.BidRecordList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query.Page, query.PageSize = auction.NormalizePagination(query.Page, query.PageSize)
	records := make([]auction.BidRecord, 0)
	for lotID, bids := range s.bidsByLot {
		if query.LotID != "" && lotID != query.LotID {
			continue
		}
		lot := s.lots[lotID]
		for _, bid := range bids {
			if bid.UserId != buyerUserID {
				continue
			}
			record := auction.BidRecord{
				ID:              bid.Id,
				LotID:           bid.LotId,
				UserID:          bid.UserId,
				Nickname:        bid.Nickname,
				Amount:          bid.GetAmount().GetAmount(),
				Currency:        bid.GetAmount().GetCurrency(),
				CreatedAtUnixMs: bid.CreatedAtUnixMs,
			}
			if lot != nil {
				record.RoomID = lot.RoomId
				record.LotTitle = lot.Title
				record.LotImageURL = lot.ImageUrl
				record.LotStatus = lot.Status.String()
				record.AuctionState = auction.AuctionStateOf(lot)
				record.Won = lot.WinnerUserId == buyerUserID
			}
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAtUnixMs > records[j].CreatedAtUnixMs })
	total := int64(len(records))
	start := auction.PageOffset(query.Page, query.PageSize)
	if start >= len(records) {
		return auction.BidRecordList{Bids: []auction.BidRecord{}, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
	}
	end := start + query.PageSize
	if end > len(records) {
		end = len(records)
	}
	return auction.BidRecordList{Bids: records[start:end], Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *testStore) FindByIdempotencyKey(ctx context.Context, lotID, userID, key string) (*v1.Bid, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bid, ok := s.idemByScope[testBidIdempotencyScope(lotID, userID, key)]
	return bid, ok, nil
}

func (s *testStore) CacheIdempotencyKey(ctx context.Context, lotID, userID, key string, bid *v1.Bid) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := testBidIdempotencyScope(lotID, userID, key)
	if _, exists := s.idemByScope[scope]; exists {
		return
	}
	s.idemByScope[scope] = bid
}

func testBidIdempotencyScope(lotID, userID, key string) string {
	return lotID + "\x00" + userID + "\x00" + key
}

func TestAuctionUsecaseCoreClosurePublishesEventsAndSnapshot(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:      "room_core",
		Title:       "核心闭环拍品",
		ImageUrl:    "https://example.com/lot.jpg",
		Description: "核心业务闭环测试",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
		TrustCards: []*v1.TrustRevealCard{{Title: "证书", Content: "可复检"}},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if len(lot.TrustCards) != 1 || lot.TrustCards[0].Id == "" || lot.TrustCards[0].LotId != lot.Id {
		t.Fatalf("trust card should be normalized on create: %+v", lot.TrustCards)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"},
	}, "u1", "用户1"); err == nil || !apperr.IsInvalidArgument(err) || !strings.Contains(err.Error(), "bid idempotency key is required") {
		t.Fatalf("missing bid idempotency key should be rejected as invalid argument, got %v", err)
	}
	_, firstBid, ranking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "idem-1",
	}, "u1", "用户1")
	if err != nil || firstBid == nil || len(ranking) != 1 {
		t.Fatalf("first bid failed: bid=%+v ranking=%+v err=%v", firstBid, ranking, err)
	}
	_, replayBid, ranking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "idem-1",
	}, "u1", "用户1")
	if err != nil || replayBid == nil || replayBid.Id != firstBid.Id || len(ranking) != 1 {
		t.Fatalf("idempotent bid replay failed: first=%+v replay=%+v ranking=%+v err=%v", firstBid, replayBid, ranking, err)
	}
	_, otherUserBid, ranking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, IdempotencyKey: "idem-1",
	}, "u2", "用户2")
	if err != nil || otherUserBid == nil || otherUserBid.Id == firstBid.Id || otherUserBid.UserId != "u2" || len(ranking) != 2 {
		t.Fatalf("same idempotency key from another user must not replay first user bid: first=%+v other=%+v ranking=%+v err=%v", firstBid, otherUserBid, ranking, err)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 13000, Currency: "CNY"}, IdempotencyKey: "idem-2",
	}, "u1", "用户1"); err != nil {
		t.Fatalf("second bid failed: %v", err)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 14000, Currency: "CNY"}, IdempotencyKey: "idem-3",
	}, "u2", "用户2"); err != nil {
		t.Fatalf("third bid failed: %v", err)
	}
	if _, card, err := uc.RevealTrustCard(ctx, lot.Id, testMainAccountID, lot.TrustCards[0].Id, "op"); err != nil || card == nil || !card.Revealed {
		t.Fatalf("reveal trust card failed: card=%+v err=%v", card, err)
	}
	if lotAfterDuel, duel, err := uc.StartDuel(ctx, lot.Id, testMainAccountID, "op", "u2", "u1"); err != nil || duel == nil || !duel.Active || duel.UserAId != "u2" || duel.UserBId != "u1" || lotAfterDuel.PlaybookStage != v1.PlaybookStage_PLAYBOOK_STAGE_DUEL_MODE {
		t.Fatalf("start duel failed: lot=%+v duel=%+v err=%v", lotAfterDuel, duel, err)
	}
	expireRuntimeLot(t, store, lot.Id)
	settled, err := uc.SettleLot(ctx, lot.Id, testMainAccountID, "op")
	if err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	if settled.Status != v1.LotStatus_LOT_STATUS_SETTLED || settled.WinnerUserId != "u2" || settled.GetFinalPrice().GetAmount() != 14000 || settled.GetDuelState().GetActive() {
		t.Fatalf("settled lot state mismatch: %+v", settled)
	}
	snapshot, err := uc.Snapshot(ctx, "room_core")
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.GetCurrentLot().GetId() != lot.Id || snapshot.GetCurrentLot().GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED ||
		len(snapshot.GetRanking()) != 2 || len(snapshot.GetRecentBids()) != 4 {
		t.Fatalf("terminal snapshot must remain recoverable: %+v", snapshot)
	}

	pub.assertContains(t,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CREATED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_TRUST_REVEALED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_DUEL_STARTED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED,
	)
	assertEventTypesContain(t, store.eventTypes(),
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CREATED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_ACCEPTED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_OUTBID,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_RANKING_UPDATED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_TRUST_REVEALED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_DUEL_STARTED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED,
	)
}

func TestPlaceBidReplaysRedisRuntimeIdempotencyResult(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_bid_race",
		Title:    "并发幂等出价拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}

	updated, bid, ranking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "bid-race-1",
	}, "buyer1", "买家一号")
	if err != nil || bid == nil || updated == nil || len(ranking) != 1 {
		t.Fatalf("first runtime bid should succeed: lot=%+v bid=%+v ranking=%+v err=%v", updated, bid, ranking, err)
	}
	bids, err := store.ListByLot(ctx, lot.Id)
	if err != nil {
		t.Fatalf("list bids failed: %v", err)
	}
	if len(bids) != 1 || bids[0].Id != bid.Id {
		t.Fatalf("runtime projection must contain one bid: stored=%+v returned=%+v", bids, bid)
	}

	_, replayed, replayRanking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "bid-race-1",
	}, "buyer1", "买家一号")
	if err != nil || replayed == nil || replayed.Id != bid.Id || len(replayRanking) != 1 {
		t.Fatalf("second replay should return same bid: first=%+v replay=%+v ranking=%+v err=%v", bid, replayed, replayRanking, err)
	}
}

func TestAuctionUsecaseRejectsPreStartCancelOutsideRuntimeLifecycle(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_cancel",
		Title:    "取消拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 0, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	queued, _, err := uc.QueueLot(ctx, lot.Id, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("queue lot failed: %v", err)
	}
	if _, err := uc.CancelLot(ctx, queued.Id, testMainAccountID, "op", "未开拍误操作"); !errors.Is(err, apperr.ErrRuntimeProjectionGap) {
		t.Fatalf("pre-start cancel must fail closed when no runtime aggregate exists, got %v", err)
	}
	fresh, err := store.FindByID(ctx, lot.Id)
	if err != nil {
		t.Fatalf("find queued lot failed: %v", err)
	}
	if fresh.Status != v1.LotStatus_LOT_STATUS_QUEUED || fresh.CancelReason != "" {
		t.Fatalf("rejected pre-start cancel mutated the lot: %+v", fresh)
	}
}

func TestAuctionUsecaseEmergencyCancelLiveLotClosesAuctionAndBlocksBids(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	room, err := uc.EnsureDefaultRoom(ctx, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("ensure room failed: %v", err)
	}
	liveLot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   room.ID,
		Title:    "直播中异常取消拍品",
		ImageUrl: "https://example.com/live-cancel.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create live lot failed: %v", err)
	}
	nextLot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   room.ID,
		Title:    "下一件拍品",
		ImageUrl: "https://example.com/next.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 20000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create next lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, liveLot.Id, testMainAccountID); err != nil {
		t.Fatalf("start live lot failed: %v", err)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: liveLot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "before-cancel",
	}, "buyer1", "买家一号"); err != nil || bid == nil {
		t.Fatalf("bid before cancel should be accepted: bid=%+v err=%v", bid, err)
	}

	cancelled, err := uc.CancelLot(ctx, liveLot.Id, testMainAccountID, "op", "主播网络异常")
	if err != nil {
		t.Fatalf("live cancel failed: %v", err)
	}
	if cancelled.Status != v1.LotStatus_LOT_STATUS_CANCELLED || cancelled.CancelReason != "主播网络异常" {
		t.Fatalf("cancelled live lot mismatch: %+v", cancelled)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: liveLot.Id, Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, IdempotencyKey: "after-cancel",
	}, "buyer2", "买家二号"); err == nil || !errors.Is(err, apperr.ErrLotCancelled) {
		t.Fatalf("bid after cancel should be rejected, got %v", err)
	}
	if order, found, err := store.FindOrderByLot(ctx, liveLot.Id); err != nil || found || order != nil {
		t.Fatalf("cancelled lot should not create order: order=%+v found=%v err=%v", order, found, err)
	}
	if _, err := uc.StartLot(ctx, nextLot.Id, testMainAccountID); err != nil {
		t.Fatalf("next lot should start after cancel: %v", err)
	}
	pub.assertContains(t, v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED)
	pub.assertContains(t, v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED)
}

func TestAuctionUsecaseCancelBidRaceDoesNotSettleCancelledLot(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_cancel_race",
		Title:    "取消出价并发拍品",
		ImageUrl: "https://example.com/race.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var cancelErr, bidErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, cancelErr = uc.CancelLot(ctx, lot.Id, testMainAccountID, "op", "主播异常取消")
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _, _, bidErr = uc.PlaceBid(ctx, &v1.PlaceBidRequest{
			LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "race-bid",
		}, "buyer-race", "并发买家")
	}()
	close(start)
	wg.Wait()
	if cancelErr != nil {
		t.Fatalf("cancel should eventually succeed: %v", cancelErr)
	}
	finalLot, err := store.FindByID(ctx, lot.Id)
	if err != nil {
		t.Fatalf("find final lot failed: %v", err)
	}
	if finalLot.Status != v1.LotStatus_LOT_STATUS_CANCELLED {
		t.Fatalf("final lot should be cancelled, bidErr=%v lot=%+v", bidErr, finalLot)
	}
	if order, found, err := store.FindOrderByLot(ctx, lot.Id); err != nil || found || order != nil {
		t.Fatalf("cancel race should not create order: order=%+v found=%v err=%v", order, found, err)
	}
}

func TestRuntimePlaceBidDoesNotUseStaleMySQLStatusAsAuthority(t *testing.T) {
	store := &runtimeGuardStore{testStore: newTestStore()}
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_runtime_guard",
		Title:    "Redis 残留状态防线拍品",
		ImageUrl: "https://example.com/runtime-guard.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	if _, err := uc.CancelLot(ctx, lot.Id, testMainAccountID, "op", "主播取消"); err != nil {
		t.Fatalf("cancel lot failed: %v", err)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "runtime-stale-live",
	}, "buyer-runtime", "Redis权威买家"); err != nil {
		t.Fatalf("authoritative runtime acceptance should not be overridden by stale MySQL status: %v", err)
	}
	if !store.placeBidRuntimeCalled {
		t.Fatal("PlaceBidRuntime was not called")
	}
}

func TestRuntimeAcceptedBidDoesNotSynchronouslyWriteMySQL(t *testing.T) {
	store := &runtimeGuardStore{testStore: newTestStore()}
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_runtime_projection_retry",
		Title:    "运行时投影补偿拍品",
		ImageUrl: "https://example.com/runtime-projection.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	pub.mu.Lock()
	publishedBeforeBid := len(pub.events)
	pub.mu.Unlock()
	updated, bid, ranking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "runtime-projection-retry",
	}, "buyer-runtime-projection", "投影补偿买家")
	if err != nil || bid == nil || updated == nil || len(ranking) != 1 {
		t.Fatalf("runtime accepted bid should return before Kafka projection: lot=%+v bid=%+v ranking=%+v err=%v", updated, bid, ranking, err)
	}
	bids, err := store.ListByLot(ctx, lot.Id)
	if err != nil {
		t.Fatalf("list bids failed: %v", err)
	}
	if len(bids) != 0 {
		t.Fatalf("runtime path should leave MySQL writes to the Kafka projector: %+v", bids)
	}
	pub.mu.Lock()
	publishedAfterBid := len(pub.events)
	pub.mu.Unlock()
	if publishedAfterBid != publishedBeforeBid {
		t.Fatalf("runtime path should leave projected event broadcast to the asynchronous path: before=%d after=%d", publishedBeforeBid, publishedAfterBid)
	}
}

func TestStartLotAllowsOnlyOneActiveLotPerRoom(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()

	room, err := uc.EnsureDefaultRoom(ctx, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("ensure room failed: %v", err)
	}
	first := createUsecaseTestLot(t, uc, ctx, room.ID, "并发开拍一")
	second := createUsecaseTestLot(t, uc, ctx, room.ID, "并发开拍二")

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	ids := []string{first.Id, second.Id}
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = uc.StartLot(ctx, ids[i], testMainAccountID)
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case apperr.IsRoomActiveLotExists(err):
			conflicts++
		default:
			t.Fatalf("unexpected start error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected one success and one active-lot conflict, got successes=%d conflicts=%d errs=%v", successes, conflicts, errs)
	}
	snapshot, err := uc.Snapshot(ctx, room.ID)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.CurrentLot == nil || !auction.IsAuctionOpenStatus(snapshot.CurrentLot.Status) {
		t.Fatalf("snapshot should expose exactly one active current lot: %+v", snapshot)
	}
	state, err := store.FindRoomState(ctx, room.ID, testMainAccountID)
	if err != nil {
		t.Fatalf("find room state failed: %v", err)
	}
	if state.ActiveLotID == "" || state.ActiveLotID != snapshot.CurrentLot.Id {
		t.Fatalf("room state and snapshot current mismatch: state=%+v current=%+v", state, snapshot.CurrentLot)
	}
}

func TestStartLotAllowsDifferentRoomsToStartConcurrently(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()

	first := createUsecaseTestLot(t, uc, ctx, "room_active_a", "房间 A")
	second := createUsecaseTestLot(t, uc, ctx, "room_active_b", "房间 B")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{first.Id, second.Id} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			<-start
			_, errs[i] = uc.StartLot(ctx, id, testMainAccountID)
		}(i, id)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("different room start should succeed, errs=%v", errs)
		}
	}
}

func TestQueueLotAllocatesUniquePositionsConcurrently(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()
	roomID := "room_queue_parallel"
	const total = 100
	lots := make([]*v1.Lot, 0, total)
	for i := 0; i < total; i++ {
		lots = append(lots, createUsecaseTestLot(t, uc, ctx, roomID, fmt.Sprintf("并发排队-%02d", i)))
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, total)
	positions := make([]int32, total)
	for i, lot := range lots {
		wg.Add(1)
		go func(i int, lotID string) {
			defer wg.Done()
			<-start
			_, position, err := uc.QueueLot(ctx, lotID, testMainAccountID, "test-owner")
			errs[i] = err
			positions[i] = position
		}(i, lot.Id)
	}
	close(start)
	wg.Wait()

	seen := make(map[int32]bool, total)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("queue %d failed: %v", i, err)
		}
		if positions[i] <= 0 {
			t.Fatalf("queue %d returned invalid position %d", i, positions[i])
		}
		if seen[positions[i]] {
			t.Fatalf("duplicate queue position %d in %v", positions[i], positions)
		}
		seen[positions[i]] = true
	}
	for position := int32(1); position <= total; position++ {
		if !seen[position] {
			t.Fatalf("missing queue position %d in %v", position, positions)
		}
	}
}

func TestQueueLotIsIdempotentAndDoesNotConsumePositionTwice(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()
	roomID := "room_queue_idempotent"
	first := createUsecaseTestLot(t, uc, ctx, roomID, "重复排队")
	second := createUsecaseTestLot(t, uc, ctx, roomID, "后续排队")

	_, firstPosition, err := uc.QueueLot(ctx, first.Id, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("first queue failed: %v", err)
	}
	_, repeatedPosition, err := uc.QueueLot(ctx, first.Id, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("repeated queue failed: %v", err)
	}
	if firstPosition != repeatedPosition {
		t.Fatalf("repeated queue should return same position: first=%d repeated=%d", firstPosition, repeatedPosition)
	}
	_, secondPosition, err := uc.QueueLot(ctx, second.Id, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("second queue failed: %v", err)
	}
	if secondPosition != firstPosition+1 {
		t.Fatalf("repeated queue consumed an extra position: first=%d repeated=%d second=%d", firstPosition, repeatedPosition, secondPosition)
	}
	state, err := store.FindRoomState(ctx, roomID, testMainAccountID)
	if err != nil {
		t.Fatalf("find room state failed: %v", err)
	}
	if state.NextQueuePosition != secondPosition+1 {
		t.Fatalf("next queue position mismatch: state=%+v second=%d", state, secondPosition)
	}
}

func TestQueueLotDifferentRoomsAllocateIndependently(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()
	firstRoomLot := createUsecaseTestLot(t, uc, ctx, "room_queue_a", "房间 A 排队")
	secondRoomLot := createUsecaseTestLot(t, uc, ctx, "room_queue_b", "房间 B 排队")

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	positions := make([]int32, 2)
	for i, lot := range []*v1.Lot{firstRoomLot, secondRoomLot} {
		wg.Add(1)
		go func(i int, lotID string) {
			defer wg.Done()
			<-start
			_, position, err := uc.QueueLot(ctx, lotID, testMainAccountID, "test-owner")
			errs[i] = err
			positions[i] = position
		}(i, lot.Id)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("queue in room %d failed: %v", i, err)
		}
		if positions[i] != 1 {
			t.Fatalf("different rooms should each allocate position 1, got positions=%v", positions)
		}
	}
}

func TestTerminalLotReleasesRoomActiveLot(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()

	roomID := "room_release"
	first := createUsecaseTestLot(t, uc, ctx, roomID, "成交释放")
	second := createUsecaseTestLot(t, uc, ctx, roomID, "下一件")
	if _, err := uc.StartLot(ctx, first.Id, testMainAccountID); err != nil {
		t.Fatalf("start first failed: %v", err)
	}
	if _, _, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: first.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "release-bid-1",
	}, "buyer-release", "释放买家"); err != nil {
		t.Fatalf("bid failed: %v", err)
	}
	expireRuntimeLot(t, store, first.Id)
	if _, err := uc.SettleLot(ctx, first.Id, testMainAccountID, "op"); err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, second.Id, testMainAccountID); err != nil {
		t.Fatalf("second lot should start after settle releases active lot: %v", err)
	}
}

func TestExpiredRuntimeLotReleasesRoomActiveLot(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := context.Background()

	roomID := "room_expired_release"
	first := createUsecaseTestLot(t, uc, ctx, roomID, "流拍释放")
	second := createUsecaseTestLot(t, uc, ctx, roomID, "流拍后下一件")
	if _, err := uc.StartLot(ctx, first.Id, testMainAccountID); err != nil {
		t.Fatalf("start first failed: %v", err)
	}
	expireRuntimeLot(t, store, first.Id)
	closed, err := uc.SettleLot(ctx, first.Id, testMainAccountID, "close-worker")
	if err != nil {
		t.Fatalf("close expired runtime lot failed: %v", err)
	}
	if closed.GetStatus() != v1.LotStatus_LOT_STATUS_FAILED {
		t.Fatalf("expected failed expired lot, got %+v", closed)
	}
	if _, err := uc.StartLot(ctx, second.Id, testMainAccountID); err != nil {
		t.Fatalf("second lot should start after failed expired release: %v", err)
	}
}

func createUsecaseTestLot(t *testing.T, uc *auction.AuctionUsecase, ctx context.Context, roomID, title string) *v1.Lot {
	t.Helper()
	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   roomID,
		Title:    title,
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create test lot failed: %v", err)
	}
	return lot
}

func TestPlaceBidAntiSnipeExtensionPersistsLotUpdatedEvent(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_extend",
		Title:    "延时事件拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        60,
			AntiSnipeWindowSeconds: 70,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	started, err := uc.StartLot(ctx, lot.Id, testMainAccountID)
	if err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	originalEndsAt := started.EndsAtUnixMs
	updated, bid, ranking, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "bid-extend-1"}, "u1", "用户1")
	if err != nil || bid == nil || len(ranking) != 1 {
		t.Fatalf("extension bid failed: updated=%+v bid=%+v ranking=%+v err=%v", updated, bid, ranking, err)
	}
	if updated.EndsAtUnixMs <= originalEndsAt || updated.GetDuelState().GetExtendCount() != 1 || updated.GetDuelState().GetLotId() != updated.Id || updated.GetDuelState().GetEndsAtUnixMs() != updated.EndsAtUnixMs {
		t.Fatalf("accepted bid should extend live lot and sync duel state: before=%d after=%+v", originalEndsAt, updated)
	}
	assertEventTypesContain(t, store.eventTypes(), v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_UPDATED, v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_EXTENDED)
}

func TestCapPriceCreatesOrderAndMockPaymentIsIdempotent(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_payment",
		Title:    "封顶成交拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			CapPrice:               &v1.Money{Amount: 11000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	settled, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "bid-cap-1",
	}, "buyer1", "买家一号")
	if err != nil || bid == nil {
		t.Fatalf("cap bid failed: lot=%+v bid=%+v err=%v", settled, bid, err)
	}
	if settled.Status != v1.LotStatus_LOT_STATUS_SETTLED || settled.WinnerUserId != "buyer1" || settled.GetFinalPrice().GetAmount() != 11000 {
		t.Fatalf("cap bid should settle lot, got %+v", settled)
	}
	order, found, err := store.FindOrderByLot(ctx, lot.Id)
	if err != nil || !found {
		t.Fatalf("settled lot should create order: found=%v err=%v", found, err)
	}
	if order.Status != auction.OrderStatusPendingPayment || order.PaymentStatus != auction.PaymentStatusInit || order.Amount != 11000 || order.BuyerUserID != "buyer1" {
		t.Fatalf("created order mismatch: %+v", order)
	}
	if order.ExpiresAtUnixMs-order.CreatedAtUnixMs != auction.OrderPaymentWindowMs {
		t.Fatalf("created order payment window mismatch: %+v", order)
	}
	if _, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "pay-bad", Amount: 10000, Currency: "CNY"}); err == nil || !strings.Contains(err.Error(), "amount") {
		t.Fatalf("payment with wrong amount should fail, got %v", err)
	}
	store.mu.Lock()
	pendingOrder := store.ordersByID[order.ID]
	pendingOrder.EnrichmentStatus = orderenrichment.StatusPending
	store.ordersByID[order.ID] = pendingOrder
	store.mu.Unlock()
	if _, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "pay-pending", Amount: 11000, Currency: "CNY"}); !errors.Is(err, apperr.ErrOrderEnrichmentPending) {
		t.Fatalf("payment should wait for order enrichment, got %v", err)
	}
	store.mu.Lock()
	readyOrder := store.ordersByID[order.ID]
	readyOrder.EnrichmentStatus = orderenrichment.StatusReady
	store.ordersByID[order.ID] = readyOrder
	store.mu.Unlock()
	pub.mu.Lock()
	pub.err = errors.New("nats unavailable")
	pub.mu.Unlock()
	paid, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "pay-1", Amount: 11000, Currency: "CNY"})
	if err != nil || !paid.Paid || paid.Order.Status != auction.OrderStatusPaid || paid.Payment.Status != auction.PaymentStatusSuccess {
		t.Fatalf("payment should succeed: result=%+v err=%v", paid, err)
	}
	replayed, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "pay-1", Amount: 11000, Currency: "CNY"})
	if err != nil || !replayed.Paid || replayed.Payment.ID != paid.Payment.ID {
		t.Fatalf("payment replay should return same payment: first=%+v replay=%+v err=%v", paid, replayed, err)
	}
	pub.mu.Lock()
	published := append([]*v1.AuctionEvent(nil), pub.events...)
	pub.mu.Unlock()
	seenPaymentSuccess := false
	for _, event := range published {
		if event.Type == v1.AuctionEventType_AUCTION_EVENT_TYPE_PAYMENT_SUCCESS {
			seenPaymentSuccess = true
			if event.Reason != "payment_success" || strings.Contains(event.Reason, "payment_id=") || strings.Contains(event.Reason, paid.Payment.ID) || strings.Contains(event.Reason, order.ID) {
				t.Fatalf("PAYMENT_SUCCESS broadcast reason must not leak order/payment id: reason=%q order=%s payment=%s", event.Reason, order.ID, paid.Payment.ID)
			}
		}
	}
	if !seenPaymentSuccess {
		t.Fatalf("expected PAYMENT_SUCCESS broadcast, got %+v", published)
	}
	orders, err := uc.ListOrdersByBuyer(ctx, "buyer1")
	if err != nil || len(orders) != 1 || orders[0].Status != auction.OrderStatusPaid {
		t.Fatalf("buyer orders mismatch: orders=%+v err=%v", orders, err)
	}
	result, err := uc.GetLotResult(ctx, lot.Id, auction.LotResultViewer{UserID: "buyer1", RoleCodes: []string{userbiz.RoleBuyer}, PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleBuyer)})
	if err != nil || result.Order == nil || result.Order.ID != order.ID || result.AuctionState != auction.AuctionStateSettled {
		t.Fatalf("lot result mismatch: result=%+v err=%v", result, err)
	}
	pub.assertContains(t,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_PAYMENT_SUCCESS,
	)
	assertEventTypesContain(t, store.eventTypes(),
		v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_PAYMENT_SUCCESS,
	)
}

func TestMockPayOrderRejectsExpiredPendingOrder(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, validCreateLotRequest("room_expired_pay"), testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "expired-pay-bid-1",
	}, "buyer1", "买家一号"); err != nil || bid == nil {
		t.Fatalf("place bid failed: bid=%+v err=%v", bid, err)
	}
	expireRuntimeLot(t, store, lot.Id)
	if _, err := uc.SettleLot(ctx, lot.Id, testMainAccountID, "op"); err != nil {
		t.Fatalf("settle lot failed: %v", err)
	}

	order, found, err := store.FindOrderByLot(ctx, lot.Id)
	if err != nil || !found {
		t.Fatalf("settled lot should create order: found=%v err=%v", found, err)
	}
	store.mu.Lock()
	expiredOrder := store.ordersByID[order.ID]
	expiredOrder.ExpiresAtUnixMs = clock.NowMs() - 1
	store.ordersByID[order.ID] = expiredOrder
	store.mu.Unlock()

	if _, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "expired-pay-1", Amount: 11000, Currency: "CNY"}); !apperr.IsInvalidArgument(err) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired order payment should fail with invalid argument, got %v", err)
	}
	orders, err := uc.ListOrdersByBuyer(ctx, "buyer1")
	if err != nil || len(orders) != 1 {
		t.Fatalf("list expired buyer orders failed: orders=%+v err=%v", orders, err)
	}
	if orders[0].Status != auction.OrderStatusExpired || orders[0].PaymentStatus != auction.PaymentStatusClosed {
		t.Fatalf("expired order summary mismatch: %+v", orders[0])
	}
}

func TestMockPayOrderReplaysPaymentAfterConcurrentCommitConflict(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_payment_race",
		Title:    "并发幂等支付拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			CapPrice:               &v1.Money{Amount: 11000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{
		LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "bid-payment-race",
	}, "buyer1", "买家一号"); err != nil || bid == nil {
		t.Fatalf("cap bid failed: bid=%+v err=%v", bid, err)
	}
	order, found, err := store.FindOrderByLot(ctx, lot.Id)
	if err != nil || !found {
		t.Fatalf("settled lot should create order: found=%v err=%v", found, err)
	}
	store.mu.Lock()
	readyOrder := store.ordersByID[order.ID]
	readyOrder.EnrichmentStatus = orderenrichment.StatusReady
	store.ordersByID[order.ID] = readyOrder
	store.mu.Unlock()

	store.failNextPaymentCommit = apperr.ErrLotVersionConflict
	store.beforePaymentCommitFailure = func(s *testStore, payment auction.Payment, order auction.Order, events []*v1.AuctionEvent) {
		if s.paymentsByOrder[order.ID] == nil {
			s.paymentsByOrder[order.ID] = make(map[string]auction.Payment)
		}
		s.paymentsByOrder[order.ID][payment.IdempotencyKey] = payment
		s.ordersByID[order.ID] = order
		s.events = append(s.events, events...)
	}

	paid, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "pay-race-1", Amount: 11000, Currency: "CNY"})
	if err != nil || paid == nil || !paid.Paid || paid.Order.Status != auction.OrderStatusPaid || paid.Payment.Status != auction.PaymentStatusSuccess {
		t.Fatalf("idempotent payment race replay should succeed: result=%+v err=%v", paid, err)
	}
	replayed, err := uc.MockPayOrder(ctx, "buyer1", order.ID, auction.MockPayRequest{IdempotencyKey: "pay-race-1", Amount: 11000, Currency: "CNY"})
	if err != nil || replayed == nil || replayed.Payment.ID != paid.Payment.ID || !replayed.Paid {
		t.Fatalf("second payment replay should return same payment: first=%+v replay=%+v err=%v", paid, replayed, err)
	}
}

func TestExpiredRuntimeLotSettlesLeadingBidAndCreatesOneOrder(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_auto_close",
		Title:    "倒计时成交拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	if _, bid, _, err := uc.PlaceBid(ctx, &v1.PlaceBidRequest{LotId: lot.Id, Amount: &v1.Money{Amount: 11000, Currency: "CNY"}, IdempotencyKey: "bid-auto-close-1"}, "buyer1", "买家一号"); err != nil || bid == nil {
		t.Fatalf("bid before expiry failed: bid=%+v err=%v", bid, err)
	}
	expireRuntimeLot(t, store, lot.Id)

	closedLot, err := uc.SettleLot(ctx, lot.Id, testMainAccountID, "close-worker")
	if err != nil {
		t.Fatalf("close expired runtime lot failed: %v", err)
	}
	if closedLot.Status != v1.LotStatus_LOT_STATUS_SETTLED || closedLot.WinnerUserId != "buyer1" || closedLot.GetFinalPrice().GetAmount() != 11000 {
		t.Fatalf("expired leading lot should be sold: %+v", closedLot)
	}
	order, found, err := store.FindOrderByLot(ctx, lot.Id)
	if err != nil || !found {
		t.Fatalf("expired leading lot should create order: found=%v err=%v", found, err)
	}
	if order.BuyerUserID != "buyer1" || order.Amount != 11000 || order.Status != auction.OrderStatusPendingPayment {
		t.Fatalf("auto-created order mismatch: %+v", order)
	}
	if _, err := uc.SettleLot(ctx, lot.Id, testMainAccountID, "close-worker"); err == nil {
		t.Fatal("terminal runtime lot must reject a duplicate close command")
	}
	if len(store.orderIDByLot) != 1 {
		t.Fatalf("duplicate close must not create another order: %+v", store.orderIDByLot)
	}
	pub.assertContains(t, v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED, v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED)
	assertEventTypesContain(t, store.eventTypes(), v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED, v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED)
}

func TestExpiredRuntimeLotWithoutBidFailsWithoutOrder(t *testing.T) {
	store := newTestStore()
	pub := &testPublisher{}
	uc := auction.NewAuctionUsecase(store, store, store, pub)
	ctx := context.Background()

	lot, err := uc.CreateLot(ctx, &v1.CreateLotRequest{
		RoomId:   "room_auto_fail",
		Title:    "倒计时流拍拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}, testMainAccountID, "test-owner")
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	if _, err := uc.StartLot(ctx, lot.Id, testMainAccountID); err != nil {
		t.Fatalf("start lot failed: %v", err)
	}
	expireRuntimeLot(t, store, lot.Id)

	closedLot, err := uc.SettleLot(ctx, lot.Id, testMainAccountID, "close-worker")
	if err != nil {
		t.Fatalf("close expired runtime lot failed: %v", err)
	}
	if closedLot.Status != v1.LotStatus_LOT_STATUS_FAILED || closedLot.CancelReason == "" {
		t.Fatalf("expired no-bid lot should be failed with reason: %+v", closedLot)
	}
	if _, found, err := store.FindOrderByLot(ctx, lot.Id); err != nil || found {
		t.Fatalf("expired no-bid lot must not create order: found=%v err=%v", found, err)
	}
	pub.assertContains(t, v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED)
	assertEventTypesContain(t, store.eventTypes(), v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED)
}

type testPublisher struct {
	mu     sync.Mutex
	events []*v1.AuctionEvent
	err    error
}

func assertEventTypesContain(t *testing.T, got []v1.AuctionEventType, want ...v1.AuctionEventType) {
	t.Helper()
	seen := make(map[v1.AuctionEventType]bool, len(got))
	for _, typ := range got {
		seen[typ] = true
	}
	for _, typ := range want {
		if !seen[typ] {
			t.Fatalf("missing persisted event type %s in %+v", typ, got)
		}
	}
}

func (p *testPublisher) Publish(ctx context.Context, event *v1.AuctionEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return p.err
}

func (p *testPublisher) assertContains(t *testing.T, types ...v1.AuctionEventType) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := make(map[v1.AuctionEventType]bool)
	for _, event := range p.events {
		seen[event.Type] = true
	}
	for _, typ := range types {
		if !seen[typ] {
			t.Fatalf("missing event type %s in %+v", typ, p.events)
		}
	}
}

func TestStartDuelWithOnlyUserBDoesNotDuplicateUsers(t *testing.T) {
	lot, err := auction.NewLotFromRequest("lot_duel", &v1.CreateLotRequest{
		RoomId:   "demo",
		Title:    "Duel 指定测试",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	})
	if err != nil {
		t.Fatalf("create lot failed: %v", err)
	}
	lot.Status = v1.LotStatus_LOT_STATUS_LIVE
	lot.StartedAtUnixMs = 1000
	lot.EndsAtUnixMs = 301000
	ranking := []*v1.RankingItem{
		{Rank: 1, UserId: "u1", Nickname: "用户1", Amount: &v1.Money{Amount: 13000, Currency: "CNY"}, BidAtUnixMs: 3000},
		{Rank: 2, UserId: "u2", Nickname: "用户2", Amount: &v1.Money{Amount: 12000, Currency: "CNY"}, BidAtUnixMs: 2000},
	}
	if err := auction.StartDuel(lot, ranking, 4000, "", "u1"); err != nil {
		t.Fatalf("start duel failed: %v", err)
	}
	if lot.GetDuelState().GetUserAId() != "u2" || lot.GetDuelState().GetUserBId() != "u1" {
		t.Fatalf("expected service to fill distinct user A around requested B, got %+v", lot.GetDuelState())
	}
}
