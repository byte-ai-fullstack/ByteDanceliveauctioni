package auction

import (
	"errors"
	"fmt"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestRuntimeBidRejectErrorPreservesStructuredState(t *testing.T) {
	var nilReject *RuntimeBidRejectError
	if nilReject.Error() != "" || nilReject.Unwrap() != nil || nilReject.Lot("lot-nil", nil).GetId() != "lot-nil" {
		t.Fatal("nil runtime rejection helpers are not safe")
	}
	cause := errors.New("cause")
	if got := (&RuntimeBidRejectError{Cause: cause}).Error(); got != "cause" {
		t.Fatalf("cause error=%q", got)
	}
	if got := (&RuntimeBidRejectError{}).Error(); got != string(apperr.CodeBidRejected) {
		t.Fatalf("default error=%q", got)
	}
	reject := &RuntimeBidRejectError{
		Code: string(apperr.CodeLotCancelled), CurrentAmount: 12_000, MinIncrementAmount: 100,
		LeadingUserID: "buyer-1", LeadingNickname: "buyer", LotVersion: 9, EndsAtUnixMs: 2_000, Cause: cause,
	}
	wrapped := fmt.Errorf("runtime: %w", reject)
	got, ok := RuntimeBidRejectFromError(wrapped)
	if !ok || got != reject || !errors.Is(wrapped, cause) {
		t.Fatalf("structured rejection=%+v ok=%t unwrap=%v", got, ok, errors.Unwrap(reject))
	}
	lot := reject.Lot("lot-1", &v1.Money{Currency: "CNY"})
	if lot.GetStatus() != v1.LotStatus_LOT_STATUS_CANCELLED || lot.GetCurrentPrice().GetAmount() != 12_000 ||
		lot.GetCurrentPrice().GetCurrency() != "CNY" || lot.GetRule().GetMinIncrement().GetAmount() != 100 ||
		lot.GetLeadingUserId() != "buyer-1" || lot.GetVersion() != 9 || lot.GetEndsAtUnixMs() != 2_000 {
		t.Fatalf("reconstructed rejected lot=%+v", lot)
	}
	reject.Code = string(apperr.CodeBidEnded)
	reject.CurrentCurrency = "USD"
	if lot := reject.Lot("lot-2", &v1.Money{Currency: "CNY"}); lot.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED || lot.GetCurrentPrice().GetCurrency() != "USD" {
		t.Fatalf("ended rejected lot=%+v", lot)
	}
	if _, ok := RuntimeBidRejectFromError(errors.New("plain")); ok {
		t.Fatal("plain error detected as runtime rejection")
	}
}

func TestRuntimeCommandErrorMappingCoversLifecycleRejections(t *testing.T) {
	sentinel := errors.New("sentinel")
	if mapRuntimeCommandError(nil) != nil || !errors.Is(mapRuntimeCommandError(sentinel), sentinel) {
		t.Fatal("mapRuntimeCommandError changed nil or unrelated error")
	}
	tests := []struct {
		code string
		want error
	}{
		{"ROOM_HAS_ACTIVE_LOT", apperr.ErrRoomActiveLotExists},
		{RuntimeCodeStateMissing, apperr.ErrRuntimeProjectionGap},
		{RuntimeCodeNotActive, apperr.ErrRuntimeProjectionGap},
		{RuntimeCodeLotFrozen, apperr.ErrRuntimeProjectionGap},
		{RuntimeCodeBidNotLive, apperr.ErrBidNotLive},
		{RuntimeCodeBidEnded, apperr.ErrBidEnded},
		{RuntimeCodeNotExpired, apperr.ErrInvalidArgument},
		{"UNKNOWN", apperr.ErrInvalidArgument},
	}
	for _, test := range tests {
		rejection := &RuntimeDecisionError{Code: test.code}
		mapped := mapRuntimeCommandError(rejection)
		if !errors.Is(mapped, test.want) {
			t.Fatalf("code=%s mapped=%v want=%v", test.code, mapped, test.want)
		}
	}
}

func TestSmallDomainHelpersAreStableAndBounded(t *testing.T) {
	nilEvent := NewAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED, nil)
	if nilEvent.GetId() == "" || nilEvent.GetType() != v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED || nilEvent.GetOccurredAtUnixMs() <= 0 {
		t.Fatalf("nil-lot event=%+v", nilEvent)
	}
	lot := &v1.Lot{Id: "lot-1", RoomId: "room-1", MainAccountId: "main-1"}
	event := NewAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED, lot)
	lot.Id = "mutated"
	if event.GetLotId() != "lot-1" || event.GetLot().GetId() != "lot-1" || event.GetMainAccountId() != "main-1" {
		t.Fatalf("event did not clone lot: %+v", event)
	}
	if first, second := LiveSourceURLForRoomID(""), LiveSourceURLForRoomID(" "); first == "" || first != second {
		t.Fatalf("default live source is unstable: %q %q", first, second)
	}
	if first, second := LiveSourceURLForRoomID("room-1"), LiveSourceURLForRoomID("room-1"); first != second {
		t.Fatalf("room live source is unstable: %q %q", first, second)
	}
}
