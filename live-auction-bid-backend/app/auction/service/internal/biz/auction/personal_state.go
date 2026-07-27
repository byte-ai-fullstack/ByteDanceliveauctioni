package auction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

const RoomPersonalStateRetryAfterMs int64 = 1_000

type RoomPersonalStateResult struct {
	State        *v1.RoomPersonalState
	RetryAfterMs int64
}

// PersonalStateForSnapshot derives only the named user's private overlay from
// an authoritative snapshot. It never includes another user's stable identity.
func PersonalStateForSnapshot(snapshot *v1.RoomSnapshot, userID string) (*v1.RoomPersonalState, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("%w: user id is required", apperr.ErrInvalidArgument)
	}
	state := &v1.RoomPersonalState{
		UserId:          userID,
		OrderVisibility: v1.OrderVisibility_ORDER_VISIBILITY_NONE,
		Tombstone:       true,
	}
	if snapshot == nil || snapshot.GetCurrentLot() == nil {
		return state, nil
	}

	lot := snapshot.GetCurrentLot()
	state.LotId = lot.GetId()
	state.LotVersion = lot.GetVersion()
	for _, item := range snapshot.GetRanking() {
		if item == nil || strings.TrimSpace(item.GetUserId()) != userID {
			continue
		}
		rank := item.GetRank()
		amount := item.GetAmount().GetAmount()
		state.YourRank = &rank
		state.YourAmountFen = &amount
		break
	}
	state.YouAreLeading = strings.TrimSpace(lot.GetLeadingUserId()) == userID
	if lot.GetStatus() == v1.LotStatus_LOT_STATUS_SETTLED && strings.TrimSpace(lot.GetWinnerUserId()) == userID {
		orderID, err := eventcontract.RuntimeOrderID(lot.GetId())
		if err != nil {
			return nil, err
		}
		state.YourOrderId = &orderID
		state.OrderVisibility = v1.OrderVisibility_ORDER_VISIBILITY_CREATING
	}
	state.Tombstone = state.YourRank == nil && !state.YouAreLeading && state.OrderVisibility == v1.OrderVisibility_ORDER_VISIBILITY_NONE
	if state.Tombstone {
		clearRoomPersonalState(state)
	}
	return state, nil
}

// GetRoomPersonalState resolves the Redis-backed private auction state first,
// then consults MySQL only to decide whether the projected order is queryable.
func (uc *AuctionUsecase) GetRoomPersonalState(ctx context.Context, roomID, userID string) (RoomPersonalStateResult, error) {
	roomID = strings.TrimSpace(roomID)
	userID = strings.TrimSpace(userID)
	if roomID == "" {
		return RoomPersonalStateResult{}, fmt.Errorf("%w: room id is required", apperr.ErrInvalidArgument)
	}
	if userID == "" {
		return RoomPersonalStateResult{}, apperr.ErrUnauthenticated
	}
	snapshot, err := uc.Snapshot(ctx, roomID)
	if err != nil {
		return RoomPersonalStateResult{}, err
	}
	state, err := PersonalStateForSnapshot(snapshot, userID)
	if err != nil {
		return RoomPersonalStateResult{}, err
	}
	result := RoomPersonalStateResult{State: state}
	if state.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_CREATING {
		return result, nil
	}
	if uc.orders == nil {
		return RoomPersonalStateResult{}, errors.New("order repository is required")
	}
	order, found, err := uc.orders.FindOrderByLot(ctx, state.GetLotId())
	if err != nil {
		return RoomPersonalStateResult{}, err
	}
	if !found {
		result.RetryAfterMs = RoomPersonalStateRetryAfterMs
		return result, nil
	}
	if order == nil || order.BuyerUserID != userID || order.LotID != state.GetLotId() || strings.TrimSpace(order.ID) == "" {
		return RoomPersonalStateResult{}, errors.New("projected order does not match runtime settlement")
	}
	orderID := order.ID
	state.YourOrderId = &orderID
	state.OrderVisibility = v1.OrderVisibility_ORDER_VISIBILITY_READY
	return result, nil
}

func clearRoomPersonalState(state *v1.RoomPersonalState) {
	if state == nil {
		return
	}
	state.YourRank = nil
	state.YourAmountFen = nil
	state.YouAreLeading = false
	state.YourOrderId = nil
	state.OrderVisibility = v1.OrderVisibility_ORDER_VISIBILITY_NONE
	state.Tombstone = true
}
