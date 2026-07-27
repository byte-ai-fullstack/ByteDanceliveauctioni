package auction

import (
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
)

func TestPrivacyCloneHelpersRedactAnonymousAndPreserveAuthorizedViewers(t *testing.T) {
	lot := privacyLotFixture()
	admin := LotResultViewer{MainAccountID: "main-1", PermissionCodes: []string{userbiz.PermissionLotViewAdmin}}
	if got := LotForViewer(lot, admin); got.GetMainAccountId() != "main-1" || got.GetLeadingUserId() != "buyer-1" {
		t.Fatalf("same-main admin lot redacted: %+v", got)
	}
	if got := LotForViewer(nil, admin); got != nil {
		t.Fatalf("nil lot clone=%v", got)
	}
	if got := LotsForViewer(nil, admin); got != nil {
		t.Fatalf("nil lots clone=%v", got)
	}

	publicLots := LotsForViewer([]*v1.Lot{lot, nil}, LotResultViewer{})
	if len(publicLots) != 2 || publicLots[1] != nil || publicLots[0] == lot {
		t.Fatalf("public lot clones=%v", publicLots)
	}
	if publicLots[0].GetMainAccountId() != "" || publicLots[0].GetLeadingUserId() != "" || publicLots[0].GetLeadingNickname() != "张***" ||
		publicLots[0].GetWinnerUserId() != "" || publicLots[0].GetWinnerNickname() != "李***" || publicLots[0].GetDuelState().GetUserAId() != "" {
		t.Fatalf("public lot leaked private identity: %+v", publicLots[0])
	}
	if lot.GetLeadingUserId() != "buyer-1" || lot.GetMainAccountId() != "main-1" {
		t.Fatal("privacy clone mutated source lot")
	}

	buyer := LotResultViewer{UserID: "buyer-1", PermissionCodes: []string{userbiz.PermissionOrderViewOwn}}
	buyerLot := LotForViewer(lot, buyer)
	if buyerLot.GetLeadingUserId() != "buyer-1" || buyerLot.GetWinnerUserId() != "" {
		t.Fatalf("buyer identity scope mismatch: %+v", buyerLot)
	}

	bid := &v1.Bid{UserId: "buyer-2", Nickname: "李四"}
	if BidForViewer(nil, buyer) != nil {
		t.Fatal("nil bid clone was non-nil")
	}
	publicBid := BidForViewer(bid, LotResultViewer{})
	if publicBid == bid || publicBid.GetUserId() != "" || publicBid.GetNickname() != "李***" || bid.GetUserId() != "buyer-2" {
		t.Fatalf("public bid redaction mismatch: source=%+v clone=%+v", bid, publicBid)
	}

	ranking := []*v1.RankingItem{nil, {UserId: "buyer-1", Nickname: "张三"}, {UserId: "buyer-2", Nickname: "李四"}}
	if RankingForViewer(nil, buyer) != nil {
		t.Fatal("nil ranking clone was non-nil")
	}
	buyerRanking := RankingForViewer(ranking, buyer)
	if buyerRanking[0] != nil || buyerRanking[1].GetUserId() != "buyer-1" || buyerRanking[2].GetUserId() != "" || buyerRanking[2].GetNickname() != "李***" {
		t.Fatalf("buyer ranking redaction mismatch: %+v", buyerRanking)
	}
}

func TestSnapshotAndEventPrivacyRespectMainAccountBoundary(t *testing.T) {
	lot := privacyLotFixture()
	snapshot := &v1.RoomSnapshot{
		RoomId: "room-1", CurrentLot: lot,
		Ranking:    []*v1.RankingItem{{UserId: "buyer-1", Nickname: "张三"}},
		RecentBids: []*v1.Bid{{UserId: "buyer-2", Nickname: "李四"}},
	}
	admin := LotResultViewer{MainAccountID: "main-1", PermissionCodes: []string{userbiz.PermissionAuctionControl}}
	if SnapshotForViewer(snapshot, admin) != snapshot {
		t.Fatal("same-main admin snapshot was cloned")
	}
	if SnapshotForViewer(nil, admin) != nil {
		t.Fatal("nil snapshot clone was non-nil")
	}
	withoutLot := &v1.RoomSnapshot{RoomId: "room-1"}
	if SnapshotForViewer(withoutLot, LotResultViewer{}) != withoutLot {
		t.Fatal("snapshot without lot should be returned unchanged")
	}
	publicSnapshot := SnapshotForViewer(snapshot, LotResultViewer{})
	if publicSnapshot == snapshot || publicSnapshot.GetCurrentLot().GetMainAccountId() != "" ||
		publicSnapshot.GetRanking()[0].GetUserId() != "" || publicSnapshot.GetRecentBids()[0].GetUserId() != "" {
		t.Fatalf("public snapshot leaked identity: %+v", publicSnapshot)
	}

	event := &v1.AuctionEvent{
		Type: v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_OUTBID, MainAccountId: "main-1", Reason: "buyer-1",
		Lot: lot, Bid: &v1.Bid{UserId: "buyer-2", Nickname: "李四"},
		Ranking: []*v1.RankingItem{{UserId: "buyer-1", Nickname: "张三"}}, DuelState: lot.DuelState, Snapshot: snapshot,
	}
	if EventForViewer(event, admin) != event {
		t.Fatal("same-main event was cloned")
	}
	if EventForViewer(nil, admin) != nil {
		t.Fatal("nil event clone was non-nil")
	}
	publicEvent := EventForViewer(event, LotResultViewer{})
	if publicEvent == event || publicEvent.GetMainAccountId() != "" || publicEvent.GetReason() != "previous_leader_outbid" ||
		publicEvent.GetBid().GetUserId() != "" || publicEvent.GetSnapshot().GetCurrentLot().GetMainAccountId() != "" {
		t.Fatalf("public event leaked identity: %+v", publicEvent)
	}
	orderEvent := &v1.AuctionEvent{Type: v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED, MainAccountId: "main-1", Reason: "order-1"}
	RedactEventForViewer(orderEvent, LotResultViewer{})
	if orderEvent.GetReason() != "" || orderEvent.GetMainAccountId() != "" {
		t.Fatalf("order event leaked reason: %+v", orderEvent)
	}
	RedactEventForViewer(nil, LotResultViewer{})
	RedactSnapshotForViewer(nil, LotResultViewer{})
	RedactLotForViewer(nil, LotResultViewer{})
	RedactBidForViewer(nil, LotResultViewer{})
	RedactDuelStateForViewer(nil, LotResultViewer{})
}

func TestViewerForMainAccountDropsCrossTenantPrivileges(t *testing.T) {
	viewer := LotResultViewer{UserID: "operator", MainAccountID: "main-2", RoleCodes: []string{"operator"}, PermissionCodes: []string{userbiz.PermissionLotViewAdmin}}
	scoped := viewerForMainAccount(viewer, "main-1")
	if scoped.MainAccountID != "" || len(scoped.RoleCodes) != 0 || len(scoped.PermissionCodes) != 0 || scoped.UserID != viewer.UserID {
		t.Fatalf("cross-main viewer not downgraded: %+v", scoped)
	}
	if same := viewerForMainAccount(viewer, "main-2"); same.MainAccountID != "main-2" || len(same.PermissionCodes) == 0 {
		t.Fatalf("same-main viewer downgraded: %+v", same)
	}
	if got := MaskBuyerNickname("***"); got != "***" {
		t.Fatalf("all-mask nickname=%q", got)
	}
}

func privacyLotFixture() *v1.Lot {
	return &v1.Lot{
		Id: "lot-1", RoomId: "room-1", MainAccountId: "main-1", Status: v1.LotStatus_LOT_STATUS_LIVE,
		LeadingUserId: "buyer-1", LeadingNickname: "张三", WinnerUserId: "buyer-2", WinnerNickname: "李四",
		DuelState: &v1.DuelState{UserAId: "buyer-1", UserANickname: "张三", UserBId: "buyer-2", UserBNickname: "李四"},
	}
}
