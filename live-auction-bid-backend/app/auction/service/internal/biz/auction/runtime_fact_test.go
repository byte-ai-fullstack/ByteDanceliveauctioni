package auction

import (
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestLotFromRuntimeFactOverlaysAuthoritativeStateWithoutMutatingBase(t *testing.T) {
	capPrice := int64(20_000)
	duration := int64(300_000)
	window := int64(10_000)
	extend := int64(30_000)
	base := &v1.Lot{
		Id: "old-id", Title: "immutable metadata", Status: v1.LotStatus_LOT_STATUS_DRAFT, Version: 1,
		PresentationVersion: 4,
		DuelState:           &v1.DuelState{Active: true, UserAId: "user-1", UserBId: "user-2", StartedAtUnixMs: 90},
	}
	fact := &v1.RuntimeFactV1{
		LotId: "lot-1", RoomId: "room-1", ConfigVersion: 3, LotVersion: 8,
		StateAfter: &v1.LotRuntimeStateV1{
			Status: v1.LotStatus_LOT_STATUS_SETTLED, Currency: "CNY", StartPriceFen: 10_000, MinIncrementFen: 100,
			CapPriceFen: &capPrice, CurrentPriceFen: 13_000, LeadingUserId: "user-1", LeadingNickname: "甲用户",
			WinnerUserId: "user-1", WinnerNickname: "甲用户", FinalPriceFen: 13_000, StartedAtUnixMs: 100,
			EndsAtUnixMs: 200, SettledAtUnixMs: 201, BidCount: 3, ParticipantCount: 2, ExtendCount: 1,
			MaxExtendCount: 3, DurationMs: &duration, AntiSnipeWindowMs: &window, AntiSnipeExtendMs: &extend,
		},
	}

	got := LotFromRuntimeFact(base, fact)
	if got == base || base.GetId() != "old-id" || base.GetStatus() != v1.LotStatus_LOT_STATUS_DRAFT {
		t.Fatalf("base lot was mutated: %+v", base)
	}
	if got.GetId() != "lot-1" || got.GetTitle() != base.GetTitle() || got.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED || got.GetVersion() != 8 {
		t.Fatalf("unexpected lot overlay: %+v", got)
	}
	if got.GetRule().GetCapPrice().GetAmount() != capPrice || got.GetRule().GetDurationSeconds() != 300 || got.GetStats().GetParticipantCount() != 2 {
		t.Fatalf("runtime rule or stats missing: %+v", got)
	}
	if got.GetPresentationVersion() != 4 || got.GetDuelState().GetUserAId() != "user-1" || got.GetDuelState().GetActive() ||
		got.GetDuelState().GetExtendCount() != 1 || got.GetDuelState().GetEndsAtUnixMs() != 200 {
		t.Fatalf("runtime fact did not merge presentation duel safely: %+v", got.GetDuelState())
	}
}

func TestRuntimeFactHelpersHandleMissingStateAndBuildPrivacySafeRanking(t *testing.T) {
	if got := LotFromRuntimeFact(nil, nil); got == nil || got.GetId() != "" {
		t.Fatalf("nil fact result=%+v", got)
	}
	if got := RankingFromRuntimeFact(nil); len(got) != 0 {
		t.Fatalf("nil ranking=%+v", got)
	}
	fact := &v1.RuntimeFactV1{StateAfter: &v1.LotRuntimeStateV1{
		Currency: "CNY",
		TopRanking: []*v1.RuntimeRankingItemV1{nil, {
			Rank: 1, UserId: "user-1", MaskedNickname: "甲***", AvatarUrl: "https://example.test/avatar.png",
			AmountFen: 12_000, BidAtUnixMs: 123,
		}},
	}}

	got := RankingFromRuntimeFact(fact)
	if len(got) != 1 || got[0].GetNickname() != "甲***" || got[0].GetAmount().GetAmount() != 12_000 || got[0].GetAmount().GetCurrency() != "CNY" {
		t.Fatalf("ranking=%+v", got)
	}
}
