package auction

import (
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestPersonalStateForSnapshotScopesIdentityAndVersion(t *testing.T) {
	snapshot := &v1.RoomSnapshot{
		RoomId: "room-1",
		CurrentLot: &v1.Lot{
			Id: "lot-1", RoomId: "room-1", Status: v1.LotStatus_LOT_STATUS_LIVE, Version: 7,
			LeadingUserId: "buyer-a",
		},
		Ranking: []*v1.RankingItem{
			{Rank: 1, UserId: "buyer-a", Amount: &v1.Money{Amount: 12_000, Currency: "CNY"}},
			{Rank: 2, UserId: "buyer-b", Amount: &v1.Money{Amount: 11_000, Currency: "CNY"}},
		},
	}

	state, err := PersonalStateForSnapshot(snapshot, "buyer-a")
	if err != nil {
		t.Fatal(err)
	}
	if state.GetUserId() != "buyer-a" || state.GetLotVersion() != 7 || state.GetYourRank() != 1 ||
		state.GetYourAmountFen() != 12_000 || !state.GetYouAreLeading() || state.GetTombstone() {
		t.Fatalf("buyer overlay mismatch: %+v", state)
	}

	missing, err := PersonalStateForSnapshot(snapshot, "buyer-c")
	if err != nil {
		t.Fatal(err)
	}
	if missing.GetUserId() != "buyer-c" || missing.GetLotVersion() != 7 || !missing.GetTombstone() ||
		missing.YourRank != nil || missing.YourAmountFen != nil || missing.YourOrderId != nil {
		t.Fatalf("unranked buyer must receive a same-version tombstone: %+v", missing)
	}
}

func TestPersonalStateForSnapshotMarksWinnerOrderCreating(t *testing.T) {
	snapshot := &v1.RoomSnapshot{CurrentLot: &v1.Lot{
		Id: "lot-settled", Status: v1.LotStatus_LOT_STATUS_SETTLED, Version: 9, WinnerUserId: "buyer-a",
	}}
	state, err := PersonalStateForSnapshot(snapshot, "buyer-a")
	if err != nil {
		t.Fatal(err)
	}
	wantOrderID, err := eventcontract.RuntimeOrderID("lot-settled")
	if err != nil {
		t.Fatal(err)
	}
	if state.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_CREATING ||
		state.GetYourOrderId() != wantOrderID || state.GetTombstone() {
		t.Fatalf("settled winner state mismatch: %+v", state)
	}
}

func TestPersonalStateForSnapshotRejectsMissingUser(t *testing.T) {
	if _, err := PersonalStateForSnapshot(&v1.RoomSnapshot{}, " "); err == nil {
		t.Fatal("missing user id must be rejected")
	}
}
