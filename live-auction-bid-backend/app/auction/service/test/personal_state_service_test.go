package test

import (
	"context"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"
)

func TestRoomPersonalStateTransitionsFromCreatingToReady(t *testing.T) {
	store := &runtimeGuardStore{
		testStore:               newTestStore(),
		runtimeActiveLotByRoom:  map[string]string{},
		runtimeDisplayLotByRoom: map[string]string{"room-personal": "lot-personal"},
	}
	store.lots["lot-personal"] = &v1.Lot{
		Id: "lot-personal", RoomId: "room-personal", MainAccountId: testMainAccountID,
		Status: v1.LotStatus_LOT_STATUS_SETTLED, Version: 11, WinnerUserId: "buyer-a",
		CurrentPrice: &v1.Money{Amount: 12_000, Currency: "CNY"}, FinalPrice: &v1.Money{Amount: 12_000, Currency: "CNY"},
		Rule: &v1.BidRule{StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"}},
	}
	store.roomStates["room-personal"] = auction.RoomState{RoomID: "room-personal", MainAccountID: testMainAccountID}
	store.bidsByLot["lot-personal"] = []*v1.Bid{{
		Id: "bid-1", LotId: "lot-personal", UserId: "buyer-a", Amount: &v1.Money{Amount: 12_000, Currency: "CNY"}, CreatedAtUnixMs: 1,
	}}
	uc := auction.NewAuctionUsecase(store, store, store, nil)

	creating, err := uc.GetRoomPersonalState(context.Background(), "room-personal", "buyer-a")
	if err != nil {
		t.Fatalf("creating personal state: %v", err)
	}
	if creating.State.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_CREATING ||
		creating.State.GetYourOrderId() == "" || creating.RetryAfterMs != auction.RoomPersonalStateRetryAfterMs {
		t.Fatalf("creating state mismatch: %+v", creating)
	}

	store.ordersByID["projected-order"] = auction.Order{ID: "projected-order", LotID: "lot-personal", RoomID: "room-personal", BuyerUserID: "buyer-a"}
	store.orderIDByLot["lot-personal"] = "projected-order"
	ready, err := uc.GetRoomPersonalState(context.Background(), "room-personal", "buyer-a")
	if err != nil {
		t.Fatalf("ready personal state: %v", err)
	}
	if ready.State.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_READY ||
		ready.State.GetYourOrderId() != "projected-order" || ready.RetryAfterMs != 0 {
		t.Fatalf("ready state mismatch: %+v", ready)
	}
}

func TestAuctionServiceRoomPersonalStateRequiresAuthentication(t *testing.T) {
	store := newTestStore()
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	svc := appsvc.NewAuctionService(uc)

	unauthenticated, err := svc.GetRoomPersonalState(context.Background(), &v1.GetRoomPersonalStateRequest{RoomId: "room-personal"})
	if err != nil {
		t.Fatal(err)
	}
	if unauthenticated.GetResult().GetCode() == 0 || unauthenticated.GetPersonalState() != nil {
		t.Fatalf("anonymous caller received private state: %+v", unauthenticated)
	}

	ctx := auth.WithClaims(context.Background(), &auth.Claims{UserID: "buyer-a", Status: v1.UserStatus_USER_STATUS_ACTIVE})
	authenticated, err := svc.GetRoomPersonalState(ctx, &v1.GetRoomPersonalStateRequest{RoomId: "room-personal"})
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.GetResult().GetCode() != 0 || authenticated.GetPersonalState().GetUserId() != "buyer-a" || !authenticated.GetPersonalState().GetTombstone() {
		t.Fatalf("authenticated empty-room state mismatch: %+v", authenticated)
	}
}
