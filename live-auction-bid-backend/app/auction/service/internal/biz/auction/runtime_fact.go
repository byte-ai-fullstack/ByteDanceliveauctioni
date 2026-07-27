package auction

import (
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

// LotFromRuntimeFact overlays Redis-authoritative lifecycle state onto immutable lot metadata.
func LotFromRuntimeFact(base *v1.Lot, fact *v1.RuntimeFactV1) *v1.Lot {
	lot := &v1.Lot{}
	if base != nil {
		lot = proto.Clone(base).(*v1.Lot)
	}
	if fact == nil || fact.GetStateAfter() == nil {
		return lot
	}
	state := fact.GetStateAfter()
	lot.Id = fact.GetLotId()
	lot.RoomId = fact.GetRoomId()
	lot.ConfigVersion = fact.GetConfigVersion()
	lot.Status = state.GetStatus()
	lot.CurrentPrice = &v1.Money{Amount: state.GetCurrentPriceFen(), Currency: state.GetCurrency()}
	lot.LeadingUserId = state.GetLeadingUserId()
	lot.LeadingNickname = state.GetLeadingNickname()
	lot.WinnerUserId = state.GetWinnerUserId()
	lot.WinnerNickname = state.GetWinnerNickname()
	lot.FinalPrice = &v1.Money{Amount: state.GetFinalPriceFen(), Currency: state.GetCurrency()}
	lot.StartedAtUnixMs = state.GetStartedAtUnixMs()
	lot.EndsAtUnixMs = state.GetEndsAtUnixMs()
	lot.SettledAtUnixMs = state.GetSettledAtUnixMs()
	lot.CancelledAtUnixMs = state.GetCancelledAtUnixMs()
	lot.CancelReason = state.GetCancelReason()
	lot.Version = fact.GetLotVersion()
	lot.Stats = &v1.LotStats{BidCount: state.GetBidCount(), ParticipantCount: state.GetParticipantCount()}
	if fact.GetCommand() == v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT || runtimeFactTerminalStatus(state.GetStatus()) {
		lot.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE
		lot.QueuePosition = 0
	}
	lot.DuelState = MergeRuntimeDuelState(
		lot.GetDuelState(), lot.GetId(), state.GetEndsAtUnixMs(),
		state.GetExtendCount(), state.GetMaxExtendCount(), runtimeFactTerminalStatus(state.GetStatus()),
	)
	lot.Rule = &v1.BidRule{
		StartPrice:             &v1.Money{Amount: state.GetStartPriceFen(), Currency: state.GetCurrency()},
		MinIncrement:           &v1.Money{Amount: state.GetMinIncrementFen(), Currency: state.GetCurrency()},
		DurationSeconds:        int32(state.GetDurationMs() / 1000),
		AntiSnipeWindowSeconds: int32(state.GetAntiSnipeWindowMs() / 1000),
		AntiSnipeExtendSeconds: int32(state.GetAntiSnipeExtendMs() / 1000),
		MaxExtendCount:         state.GetMaxExtendCount(),
	}
	if state.CapPriceFen != nil {
		lot.Rule.CapPrice = &v1.Money{Amount: state.GetCapPriceFen(), Currency: state.GetCurrency()}
	}
	return lot
}

func runtimeFactTerminalStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

// RankingFromRuntimeFact returns the bounded privacy-safe public ranking embedded in a fact.
func RankingFromRuntimeFact(fact *v1.RuntimeFactV1) []*v1.RankingItem {
	if fact == nil || fact.GetStateAfter() == nil {
		return []*v1.RankingItem{}
	}
	state := fact.GetStateAfter()
	ranking := make([]*v1.RankingItem, 0, len(state.GetTopRanking()))
	for _, item := range state.GetTopRanking() {
		if item == nil {
			continue
		}
		ranking = append(ranking, &v1.RankingItem{
			Rank: item.GetRank(), UserId: item.GetUserId(), Nickname: item.GetMaskedNickname(), AvatarUrl: item.GetAvatarUrl(),
			Amount: &v1.Money{Amount: item.GetAmountFen(), Currency: state.GetCurrency()}, BidAtUnixMs: item.GetBidAtUnixMs(),
		})
	}
	return ranking
}
