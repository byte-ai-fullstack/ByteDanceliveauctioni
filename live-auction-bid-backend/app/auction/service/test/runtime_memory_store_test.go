package test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

// testStore implements the Redis runtime and Kafka projector boundaries in
// memory. Production repositories deliberately have no MySQL adjudication
// fallback; this adapter keeps end-to-end use-case tests deterministic without
// weakening that boundary.
func (s *testStore) ExecuteStartLot(_ context.Context, lot *v1.Lot, traceID string) (auction.RuntimeStartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if lot == nil {
		return auction.RuntimeStartResult{}, errors.New("lot is required")
	}
	if activeID := s.runtimeActiveLotByRoom[lot.GetRoomId()]; activeID != "" && activeID != lot.GetId() {
		return auction.RuntimeStartResult{}, &auction.RuntimeDecisionError{Code: "ROOM_HAS_ACTIVE_LOT"}
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return auction.RuntimeStartResult{}, err
	}
	decision, err := auction.DecideRuntimeStartLot(auction.RuntimeStartLotCommand{
		Meta:               auction.RuntimeCommandMeta{EventID: eventID, TraceID: testRuntimeTraceID(traceID, eventID)},
		Config:             testRuntimeConfigFromLot(lot),
		PreviousStatus:     lot.GetStatus(),
		PreviousLotVersion: lot.GetVersion(),
		NowUnixMs:          time.Now().UnixMilli(),
	})
	if err != nil {
		return auction.RuntimeStartResult{}, err
	}
	s.runtimeStates[lot.GetId()] = decision.State
	s.projectRuntimeFactLocked(decision.Fact)
	return auction.RuntimeStartResult{
		SourceLot: proto.Clone(lot).(*v1.Lot),
		Fact:      proto.Clone(decision.Fact).(*v1.RuntimeFactV1),
	}, nil
}

func (s *testStore) ExecuteCancelLot(_ context.Context, lotID, reason, operatorID, traceID string) (*v1.RuntimeFactV1, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.runtimeStates[lotID]
	if !ok {
		return nil, &auction.RuntimeDecisionError{Code: auction.RuntimeCodeStateMissing}
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	decision, err := auction.DecideRuntimeCancelLot(state, auction.RuntimeCancelLotCommand{
		Meta:       auction.RuntimeCommandMeta{EventID: eventID, TraceID: testRuntimeTraceID(traceID, eventID)},
		Reason:     reason,
		OperatorID: operatorID,
		NowUnixMs:  time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	s.runtimeStates[lotID] = decision.State
	s.projectRuntimeFactLocked(decision.Fact)
	return proto.Clone(decision.Fact).(*v1.RuntimeFactV1), nil
}

func (s *testStore) ExecuteCloseIfExpired(_ context.Context, lotID, orderID, traceID string) (*v1.RuntimeFactV1, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.runtimeStates[lotID]
	if !ok {
		return nil, &auction.RuntimeDecisionError{Code: auction.RuntimeCodeStateMissing}
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	decision, err := auction.DecideRuntimeCloseIfExpired(state, auction.RuntimeCloseIfExpiredCommand{
		Meta:      auction.RuntimeCommandMeta{EventID: eventID, TraceID: testRuntimeTraceID(traceID, eventID)},
		OrderID:   orderID,
		NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	s.runtimeStates[lotID] = decision.State
	s.projectRuntimeFactLocked(decision.Fact)
	return proto.Clone(decision.Fact).(*v1.RuntimeFactV1), nil
}

func (s *testStore) ExecuteSyncLotConfig(_ context.Context, lot *v1.Lot, expectedConfigVersion int64, traceID string) (*v1.RuntimeFactV1, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if lot == nil {
		return nil, errors.New("lot is required")
	}
	state, ok := s.runtimeStates[lot.GetId()]
	if !ok {
		return nil, &auction.RuntimeDecisionError{Code: auction.RuntimeCodeStateMissing}
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	decision, err := auction.DecideRuntimeSyncLotConfig(state, auction.RuntimeSyncLotConfigCommand{
		Meta:                  auction.RuntimeCommandMeta{EventID: eventID, TraceID: testRuntimeTraceID(traceID, eventID)},
		ExpectedConfigVersion: expectedConfigVersion,
		NextConfig:            testRuntimeConfigFromLot(lot),
		NowUnixMs:             time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	s.runtimeStates[lot.GetId()] = decision.State
	s.projectRuntimeFactLocked(decision.Fact)
	return proto.Clone(decision.Fact).(*v1.RuntimeFactV1), nil
}

func (s *testStore) PlaceBidRuntime(_ context.Context, lot *v1.Lot, req *v1.PlaceBidRequest, bidderID, nickname, avatarURL, bidID string, nowMs int64) (auction.RuntimeBidResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if lot == nil || req == nil || req.GetAmount() == nil {
		return auction.RuntimeBidResult{}, apperr.ErrInvalidArgument
	}
	lotID := lot.GetId()
	state, ok := s.runtimeStates[lotID]
	if !ok {
		return auction.RuntimeBidResult{}, &auction.RuntimeBidRejectError{Code: string(apperr.CodeProjectionPending), Cause: apperr.ErrRuntimeProjectionGap}
	}
	if existing := s.idemByScope[testBidIdempotencyScope(lotID, bidderID, req.GetIdempotencyKey())]; existing != nil {
		projected := s.lots[lotID]
		return auction.RuntimeBidResult{
			Lot: proto.Clone(projected).(*v1.Lot), Bid: proto.Clone(existing).(*v1.Bid),
			Ranking: auction.BuildRealtimeRanking(s.bidsByLot[lotID]), Replayed: true,
		}, nil
	}
	orderID, err := eventcontract.RuntimeOrderID(lotID)
	if err != nil {
		return auction.RuntimeBidResult{}, err
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return auction.RuntimeBidResult{}, err
	}
	decision, err := auction.DecideRuntimePlaceBid(state, auction.RuntimePlaceBidCommand{
		Meta:         auction.RuntimeCommandMeta{EventID: eventID, TraceID: eventID, IdempotencyKey: req.GetIdempotencyKey()},
		BidID:        bidID,
		UserID:       bidderID,
		Nickname:     nickname,
		AvatarURL:    avatarURL,
		AmountFen:    req.GetAmount().GetAmount(),
		Currency:     req.GetAmount().GetCurrency(),
		OrderID:      orderID,
		RankingLimit: int(auction.RealtimeRankingLimit()),
		NowUnixMs:    nowMs,
	})
	if err != nil {
		var rejection *auction.RuntimeDecisionError
		if errors.As(err, &rejection) {
			code := rejection.Code
			cause := apperr.ErrorForBusinessCode(code)
			return auction.RuntimeBidResult{}, &auction.RuntimeBidRejectError{
				Code: code, CurrentAmount: state.CurrentPriceFen, CurrentCurrency: state.Config.Currency,
				MinIncrementAmount: state.Config.MinIncrementFen, NextBidAmount: rejection.MinimumBid,
				LeadingUserID: state.LeadingUserID, LeadingNickname: state.LeadingNickname,
				LotVersion: state.Version, EndsAtUnixMs: state.EndsAtUnixMs, Cause: cause,
			}
		}
		return auction.RuntimeBidResult{}, err
	}
	s.runtimeStates[lotID] = decision.State
	s.projectRuntimeFactLocked(decision.Fact)

	accepted := decision.Fact.GetAcceptedBid()
	updated := s.lots[lotID]
	bid := &v1.Bid{
		Id: accepted.GetBidId(), LotId: lotID, UserId: accepted.GetUserId(), Nickname: accepted.GetNickname(),
		AvatarUrl: accepted.GetAvatarUrl(), Amount: &v1.Money{Amount: accepted.GetAmountFen(), Currency: decision.State.Config.Currency},
		CreatedAtUnixMs: accepted.GetAcceptedAtUnixMs(),
	}
	extendBefore := decision.State.ExtendCount
	if decision.State.Status == v1.LotStatus_LOT_STATUS_EXTENDED && extendBefore > 0 {
		extendBefore--
	}
	return auction.RuntimeBidResult{
		Lot: proto.Clone(updated).(*v1.Lot), Bid: bid, Ranking: auction.RankingFromRuntimeFact(decision.Fact),
		ExtendCountBefore: extendBefore, RuntimeEventID: eventID,
		PreviousLotVersion: decision.Fact.GetPrevLotVersion(), LotVersion: decision.Fact.GetLotVersion(),
		OrderID: decision.State.OrderID,
	}, nil
}

func (s *testStore) ActiveRuntimeLotID(_ context.Context, roomID string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lotID := s.runtimeActiveLotByRoom[roomID]
	return lotID, lotID != "", nil
}

func (s *testStore) DisplayedRuntimeLotID(_ context.Context, roomID string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lotID := s.runtimeDisplayLotByRoom[roomID]
	return lotID, lotID != "", nil
}

func (s *testStore) SnapshotRuntime(_ context.Context, current *v1.Lot) (*v1.RoomSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if current == nil {
		return nil, errors.New("current lot is required")
	}
	lot := s.lots[current.GetId()]
	if lot == nil {
		lot = current
	}
	bids := s.bidsByLot[current.GetId()]
	return &v1.RoomSnapshot{
		RoomId: current.GetRoomId(), CurrentLot: proto.Clone(lot).(*v1.Lot),
		Ranking: auction.BuildRealtimeRanking(bids), RecentBids: cloneTestBids(bids),
		ServerTimeUnixMs: time.Now().UnixMilli(),
	}, nil
}

func (s *testStore) RankingRuntime(_ context.Context, lotID string, limit int64) ([]*v1.RankingItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ranking := auction.BuildRealtimeRanking(s.bidsByLot[lotID])
	if limit > 0 && int64(len(ranking)) > limit {
		ranking = ranking[:limit]
	}
	return ranking, nil
}

func (s *testStore) projectRuntimeFactLocked(fact *v1.RuntimeFactV1) {
	if fact == nil || fact.GetStateAfter() == nil {
		return
	}
	base := s.lots[fact.GetLotId()]
	projected := auction.LotFromRuntimeFact(base, fact)
	s.lots[fact.GetLotId()] = projected

	state := s.ensureRoomStateLocked(fact.GetRoomId(), projected.GetMainAccountId(), fact.GetOccurredAtUnixMs())
	s.runtimeDisplayLotByRoom[fact.GetRoomId()] = fact.GetLotId()
	state.DisplayLotID = fact.GetLotId()
	if auction.IsAuctionOpenStatus(projected.GetStatus()) {
		s.runtimeActiveLotByRoom[fact.GetRoomId()] = fact.GetLotId()
		state.ActiveLotID = fact.GetLotId()
		state.ActiveLotVersion = fact.GetLotVersion()
	} else {
		delete(s.runtimeActiveLotByRoom, fact.GetRoomId())
		state.ActiveLotID = ""
		state.ActiveLotVersion = 0
	}
	state.UpdatedAtUnixMs = fact.GetOccurredAtUnixMs()
	s.roomStates[fact.GetRoomId()] = state

	ranking := auction.RankingFromRuntimeFact(fact)
	accepted := fact.GetAcceptedBid()
	hadPreviousBid := len(s.bidsByLot[fact.GetLotId()]) > 0
	if accepted != nil {
		bid := &v1.Bid{
			Id: accepted.GetBidId(), LotId: fact.GetLotId(), UserId: accepted.GetUserId(), Nickname: accepted.GetNickname(),
			AvatarUrl: accepted.GetAvatarUrl(), Amount: &v1.Money{Amount: accepted.GetAmountFen(), Currency: fact.GetStateAfter().GetCurrency()},
			CreatedAtUnixMs: accepted.GetAcceptedAtUnixMs(),
		}
		s.bidsByLot[fact.GetLotId()] = append(s.bidsByLot[fact.GetLotId()], bid)
		s.idemByScope[testBidIdempotencyScope(fact.GetLotId(), accepted.GetUserId(), fact.GetIdempotencyKey())] = bid
	}
	if draft := fact.GetOrderDraft(); draft != nil {
		if _, exists := s.ordersByID[draft.GetOrderId()]; !exists {
			s.ordersByID[draft.GetOrderId()] = auction.Order{
				ID: draft.GetOrderId(), MainAccountID: draft.GetMainAccountId(), LotID: draft.GetLotId(), RoomID: draft.GetRoomId(),
				LotTitle: draft.GetTitle(), LotImageURL: draft.GetImageUrl(), BuyerUserID: draft.GetBuyerUserId(), BuyerNickname: draft.GetBuyerNickname(),
				Status: auction.OrderStatusPendingPayment, PaymentStatus: auction.PaymentStatusInit, EnrichmentStatus: orderenrichment.StatusPending,
				Amount: draft.GetTotalAmountFen(), Currency: draft.GetCurrency(), CreatedAtUnixMs: draft.GetCreatedAtUnixMs(),
				UpdatedAtUnixMs: draft.GetCreatedAtUnixMs(), ExpiresAtUnixMs: draft.GetCreatedAtUnixMs() + auction.OrderPaymentWindowMs, Version: 1,
			}
			s.orderIDByLot[draft.GetLotId()] = draft.GetOrderId()
		}
	}

	appendEvent := func(eventType v1.AuctionEventType, reason string) {
		event := auction.NewAuctionEvent(eventType, projected)
		event.Ranking = ranking
		event.Reason = reason
		s.events = append(s.events, event)
	}
	switch fact.GetCommand() {
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT:
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED, "")
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID:
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_ACCEPTED, "")
		if hadPreviousBid {
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_OUTBID, "")
		}
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_RANKING_UPDATED, "")
		if projected.GetStatus() == v1.LotStatus_LOT_STATUS_EXTENDED {
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_UPDATED, "")
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_EXTENDED, "")
		}
		if projected.GetStatus() == v1.LotStatus_LOT_STATUS_SETTLED {
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, "")
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED, "")
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED, "order_created")
		}
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT:
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED, projected.GetCancelReason())
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, projected.GetCancelReason())
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED:
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, projected.GetCancelReason())
		if projected.GetStatus() == v1.LotStatus_LOT_STATUS_SETTLED {
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED, "")
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED, "order_created")
		} else {
			appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED, projected.GetCancelReason())
		}
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG:
		appendEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_UPDATED, "")
	}
}

func testRuntimeConfigFromLot(lot *v1.Lot) auction.RuntimeConfigSnapshot {
	rule := lot.GetRule()
	configVersion := lot.GetConfigVersion()
	if configVersion <= 0 {
		configVersion = 1
	}
	config := auction.RuntimeConfigSnapshot{
		LotID: lot.GetId(), RoomID: lot.GetRoomId(), MainAccountID: lot.GetMainAccountId(), Title: lot.GetTitle(), ImageURL: lot.GetImageUrl(),
		ConfigVersion: configVersion, Currency: rule.GetStartPrice().GetCurrency(), StartPriceFen: rule.GetStartPrice().GetAmount(),
		MinIncrementFen: rule.GetMinIncrement().GetAmount(), DurationMs: int64(rule.GetDurationSeconds()) * 1000,
		AntiSnipeWindowMs: int64(rule.GetAntiSnipeWindowSeconds()) * 1000, AntiSnipeExtendMs: int64(rule.GetAntiSnipeExtendSeconds()) * 1000,
		MaxExtendCount: rule.GetMaxExtendCount(),
	}
	if rule.GetCapPrice() != nil {
		capPrice := rule.GetCapPrice().GetAmount()
		config.CapPriceFen = &capPrice
	}
	return config
}

func testRuntimeTraceID(traceID, eventID string) string {
	if strings.TrimSpace(traceID) == "" {
		return eventID
	}
	return traceID
}

func cloneTestBids(bids []*v1.Bid) []*v1.Bid {
	clones := make([]*v1.Bid, 0, len(bids))
	for _, bid := range bids {
		if bid != nil {
			clones = append(clones, proto.Clone(bid).(*v1.Bid))
		}
	}
	return clones
}

func expireRuntimeLot(t *testing.T, store *testStore, lotID string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	state, ok := store.runtimeStates[lotID]
	if !ok {
		t.Fatalf("runtime lot not found: %s", lotID)
	}
	state.EndsAtUnixMs = time.Now().Add(-time.Second).UnixMilli()
	store.runtimeStates[lotID] = state
	if lot := store.lots[lotID]; lot != nil {
		lot.EndsAtUnixMs = state.EndsAtUnixMs
	}
}
