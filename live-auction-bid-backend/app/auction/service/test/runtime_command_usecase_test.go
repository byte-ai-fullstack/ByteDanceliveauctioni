package test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
)

func TestAuctionUsecaseRoutesStartAndActiveCancelThroughRuntimeCommands(t *testing.T) {
	base := newTestStore()
	store := &runtimeCommandTestStore{testStore: base}
	publisher := &testPublisher{}
	usecase := auction.NewAuctionUsecase(store, store, store, publisher)
	ctx := requestctx.WithRequestContext(context.Background(), requestctx.RequestContext{TraceID: "trace-runtime-command"})
	lot, err := auction.NewLotFromRequest("lot-runtime-command", &v1.CreateLotRequest{
		RoomId: "room-runtime-command", Title: "Lua lifecycle", ImageUrl: "https://example.test/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"},
			DurationSeconds: 300, AntiSnipeWindowSeconds: 10, AntiSnipeExtendSeconds: 30, MaxExtendCount: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lot.MainAccountId = testMainAccountID
	lot.ConfigVersion = 1
	if err := base.Create(ctx, lot, "owner", nil); err != nil {
		t.Fatal(err)
	}

	started, err := usecase.StartLot(ctx, lot.GetId(), testMainAccountID)
	if err != nil {
		t.Fatalf("StartLot: %v", err)
	}
	if started.GetStatus() != v1.LotStatus_LOT_STATUS_LIVE || started.GetVersion() != lot.GetVersion()+1 || store.startCalls != 1 {
		t.Fatalf("started=%+v calls=%d", started, store.startCalls)
	}
	persisted, err := base.FindByID(ctx, lot.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GetStatus() != v1.LotStatus_LOT_STATUS_DRAFT || persisted.GetVersion() != lot.GetVersion() {
		t.Fatalf("runtime command must not directly mutate MySQL model: %+v", persisted)
	}

	cancelled, err := usecase.CancelLot(ctx, lot.GetId(), testMainAccountID, "operator-1", "operator cancelled")
	if err != nil {
		t.Fatalf("CancelLot: %v", err)
	}
	if cancelled.GetStatus() != v1.LotStatus_LOT_STATUS_CANCELLED || cancelled.GetVersion() != started.GetVersion()+1 || store.cancelCalls != 1 {
		t.Fatalf("cancelled=%+v calls=%d", cancelled, store.cancelCalls)
	}
	publisher.assertContains(t, v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED, v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED, v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED)
}

func TestRuntimeCommandSuccessIsNotRolledBackByRealtimePublishFailure(t *testing.T) {
	base := newTestStore()
	store := &runtimeCommandTestStore{testStore: base}
	publisher := &testPublisher{err: errors.New("realtime transport unavailable")}
	usecase := auction.NewAuctionUsecase(store, store, store, publisher)
	ctx := requestctx.WithRequestContext(context.Background(), requestctx.RequestContext{TraceID: "trace-runtime-publish-failure"})
	lot, err := auction.NewLotFromRequest("lot-runtime-publish-failure", &v1.CreateLotRequest{
		RoomId: "room-runtime-publish-failure", Title: "durable before realtime", ImageUrl: "https://example.test/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"},
			DurationSeconds: 300, AntiSnipeWindowSeconds: 10, AntiSnipeExtendSeconds: 30, MaxExtendCount: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lot.MainAccountId = testMainAccountID
	lot.ConfigVersion = 1
	if err := base.Create(ctx, lot, "owner", nil); err != nil {
		t.Fatal(err)
	}

	started, err := usecase.StartLot(ctx, lot.GetId(), testMainAccountID)
	if err != nil || started.GetStatus() != v1.LotStatus_LOT_STATUS_LIVE || store.startCalls != 1 {
		t.Fatalf("accepted runtime command must survive realtime failure: lot=%+v calls=%d error=%v", started, store.startCalls, err)
	}
}

func TestAuctionUsecaseRoutesManualSettlementThroughExpiredCloseCommand(t *testing.T) {
	base := newTestStore()
	store := &runtimeCommandTestStore{testStore: base}
	usecase := auction.NewAuctionUsecase(store, store, store, &testPublisher{})
	ctx := requestctx.WithRequestContext(context.Background(), requestctx.RequestContext{TraceID: "trace-runtime-settle"})
	lot, err := auction.NewLotFromRequest("lot-runtime-settle", &v1.CreateLotRequest{
		RoomId: "room-runtime-settle", Title: "runtime settlement", ImageUrl: "https://example.test/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"},
			DurationSeconds: 300, AntiSnipeWindowSeconds: 10, AntiSnipeExtendSeconds: 30, MaxExtendCount: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lot.MainAccountId = testMainAccountID
	lot.ConfigVersion = 1
	if err := base.Create(ctx, lot, "owner", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := usecase.StartLot(ctx, lot.GetId(), testMainAccountID); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.state.EndsAtUnixMs = 1
	store.state.CurrentPriceFen = 10_100
	store.state.LeadingUserID = "buyer-1"
	store.state.LeadingNickname = "测试买家"
	store.state.BidCount = 1
	store.state.ParticipantIDs = map[string]struct{}{"buyer-1": {}}
	store.mu.Unlock()

	settled, err := usecase.SettleLot(ctx, lot.GetId(), testMainAccountID, "operator-1")
	if err != nil {
		t.Fatal(err)
	}
	if settled.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED || settled.GetWinnerUserId() != "buyer-1" || store.closeCalls != 1 {
		t.Fatalf("settled=%+v close_calls=%d", settled, store.closeCalls)
	}
	persisted, err := base.FindByID(ctx, lot.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GetStatus() != v1.LotStatus_LOT_STATUS_DRAFT {
		t.Fatalf("manual settlement directly mutated MySQL model: %+v", persisted)
	}
}

type runtimeCommandTestStore struct {
	*testStore
	mu          sync.Mutex
	state       auction.RuntimeState
	startCalls  int
	cancelCalls int
	closeCalls  int
}

func (store *runtimeCommandTestStore) ExecuteStartLot(_ context.Context, lot *v1.Lot, traceID string) (auction.RuntimeStartResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.startCalls++
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return auction.RuntimeStartResult{}, err
	}
	rule := lot.GetRule()
	decision, err := auction.DecideRuntimeStartLot(auction.RuntimeStartLotCommand{
		Meta: auction.RuntimeCommandMeta{EventID: eventID, TraceID: traceID},
		Config: auction.RuntimeConfigSnapshot{
			LotID: lot.GetId(), RoomID: lot.GetRoomId(), MainAccountID: lot.GetMainAccountId(), Title: lot.GetTitle(), ImageURL: lot.GetImageUrl(),
			ConfigVersion: lot.GetConfigVersion(), Currency: rule.GetStartPrice().GetCurrency(), StartPriceFen: rule.GetStartPrice().GetAmount(),
			MinIncrementFen: rule.GetMinIncrement().GetAmount(), DurationMs: int64(rule.GetDurationSeconds()) * 1000,
			AntiSnipeWindowMs: int64(rule.GetAntiSnipeWindowSeconds()) * 1000, AntiSnipeExtendMs: int64(rule.GetAntiSnipeExtendSeconds()) * 1000,
			MaxExtendCount: rule.GetMaxExtendCount(),
		},
		PreviousStatus: lot.GetStatus(), PreviousLotVersion: lot.GetVersion(), NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		return auction.RuntimeStartResult{}, err
	}
	store.state = decision.State
	return auction.RuntimeStartResult{SourceLot: proto.Clone(lot).(*v1.Lot), Fact: decision.Fact}, nil
}

func (store *runtimeCommandTestStore) ExecuteCancelLot(_ context.Context, _ string, reason, operatorID, traceID string) (*v1.RuntimeFactV1, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cancelCalls++
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	decision, err := auction.DecideRuntimeCancelLot(store.state, auction.RuntimeCancelLotCommand{
		Meta: auction.RuntimeCommandMeta{EventID: eventID, TraceID: traceID}, Reason: reason, OperatorID: operatorID, NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	store.state = decision.State
	return decision.Fact, nil
}

func (store *runtimeCommandTestStore) ExecuteCloseIfExpired(_ context.Context, _, orderID, traceID string) (*v1.RuntimeFactV1, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.closeCalls++
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	decision, err := auction.DecideRuntimeCloseIfExpired(store.state, auction.RuntimeCloseIfExpiredCommand{
		Meta: auction.RuntimeCommandMeta{EventID: eventID, TraceID: traceID}, OrderID: orderID, NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	store.state = decision.State
	return decision.Fact, nil
}

func (store *runtimeCommandTestStore) ExecuteSyncLotConfig(context.Context, *v1.Lot, int64, string) (*v1.RuntimeFactV1, error) {
	return nil, &auction.RuntimeDecisionError{Code: auction.RuntimeCodeConfigFrozen}
}
