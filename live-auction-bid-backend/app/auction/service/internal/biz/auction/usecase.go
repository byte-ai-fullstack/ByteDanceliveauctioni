package auction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/clock"
	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
)

// AuctionUsecase 编排直播竞拍业务流程。
//
// 分层约束：
// - 业务流程和规则放在 biz；
// - 存储细节通过 Repository 接口隔离；
// - 实时广播通过 EventPublisher 接口隔离；
// - HTTP/WS 适配不放在 biz 层。
type AuctionUsecase struct {
	lots            LotRepository
	batchLots       LotBatchRepository
	presentations   LotPresentationRepository
	rooms           RoomRepository
	bids            BidRepository
	runtime         AuctionRuntime
	runtimeCommands RuntimeCommandRepository
	orders          OrderRepository
	payments        PaymentRepository
	deposits        DepositRepository
	addresses       DeliveryAddressRepository
	eventsStore     EventRepository
	events          EventPublisher
	paymentProvider PaymentProvider
}

const (
	orderCreatedPublicReason   = "order_created"
	paymentSuccessPublicReason = "payment_success"
	presentationWriteAttempts  = 3
)

func (uc *AuctionUsecase) EnsureDefaultRoom(ctx context.Context, mainAccountID, createdByUserID string) (*Room, error) {
	if uc.rooms == nil {
		return nil, errors.New("room repository is required")
	}
	mainAccountID = strings.TrimSpace(mainAccountID)
	if mainAccountID == "" {
		return nil, fmt.Errorf("%w: main account id is required", apperr.ErrPermissionDenied)
	}
	return uc.rooms.EnsureDefaultRoom(ctx, mainAccountID, strings.TrimSpace(createdByUserID), clock.NowMs())
}

func (uc *AuctionUsecase) ListRooms(ctx context.Context, query RoomQuery) ([]Room, error) {
	if uc.rooms == nil {
		return nil, errors.New("room repository is required")
	}
	return uc.rooms.ListRooms(ctx, query)
}

func (uc *AuctionUsecase) ValidateRoomInMainAccount(ctx context.Context, roomID, mainAccountID string) error {
	return uc.ensureRoomInMainAccount(ctx, roomID, mainAccountID)
}

func (uc *AuctionUsecase) ensureRoomInMainAccount(ctx context.Context, roomID, mainAccountID string) error {
	if uc.rooms == nil {
		return errors.New("room repository is required")
	}
	roomID = strings.TrimSpace(roomID)
	mainAccountID = strings.TrimSpace(mainAccountID)
	if roomID == "" {
		return fmt.Errorf("%w: room id is required", apperr.ErrInvalidArgument)
	}
	if mainAccountID == "" {
		return fmt.Errorf("%w: main account id is required", apperr.ErrPermissionDenied)
	}
	room, found, err := uc.rooms.FindRoomByID(ctx, roomID)
	if err != nil {
		return err
	}
	if !found {
		return apperr.ErrNotFound
	}
	if room.MainAccountID != mainAccountID {
		return apperr.ErrPermissionDenied
	}
	if room.Status != "" && room.Status != RoomStatusActive {
		return fmt.Errorf("%w: room is disabled", apperr.ErrInvalidArgument)
	}
	return nil
}

func (uc *AuctionUsecase) ensureActiveRoom(ctx context.Context, roomID string) error {
	_, err := uc.findActiveRoom(ctx, roomID)
	return err
}

func (uc *AuctionUsecase) findActiveRoom(ctx context.Context, roomID string) (*Room, error) {
	if uc.rooms == nil {
		return nil, errors.New("room repository is required")
	}
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return nil, fmt.Errorf("%w: room id is required", apperr.ErrInvalidArgument)
	}
	room, found, err := uc.rooms.FindRoomByID(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apperr.ErrNotFound
	}
	if room.Status != "" && room.Status != RoomStatusActive {
		return nil, apperr.ErrNotFound
	}
	return room, nil
}

func ensureLotMainAccount(lot *v1.Lot, mainAccountID string) error {
	if lot == nil {
		return apperr.ErrNotFound
	}
	if strings.TrimSpace(mainAccountID) == "" || lot.GetMainAccountId() != strings.TrimSpace(mainAccountID) {
		return apperr.ErrPermissionDenied
	}
	return nil
}

func NewAuctionUsecase(lots LotRepository, bids BidRepository, eventStore EventRepository, events EventPublisher) *AuctionUsecase {
	uc := &AuctionUsecase{lots: lots, bids: bids, eventsStore: eventStore, events: events}
	if repo, ok := lots.(LotBatchRepository); ok {
		uc.batchLots = repo
	}
	if repo, ok := lots.(LotPresentationRepository); ok {
		uc.presentations = repo
	}
	if repo, ok := lots.(RoomRepository); ok {
		uc.rooms = repo
	}
	if repo, ok := lots.(OrderRepository); ok {
		uc.orders = repo
	}
	if repo, ok := lots.(PaymentRepository); ok {
		uc.payments = repo
	}
	if repo, ok := lots.(DepositRepository); ok {
		uc.deposits = repo
	}
	if repo, ok := lots.(DeliveryAddressRepository); ok {
		uc.addresses = repo
	}
	if repo, ok := bids.(AuctionRuntime); ok {
		uc.runtime = repo
	} else if repo, ok := lots.(AuctionRuntime); ok {
		uc.runtime = repo
	}
	if repo, ok := bids.(RuntimeCommandRepository); ok {
		uc.runtimeCommands = repo
	} else if repo, ok := lots.(RuntimeCommandRepository); ok {
		uc.runtimeCommands = repo
	}
	return uc
}

func (uc *AuctionUsecase) SetPaymentProvider(provider PaymentProvider) *AuctionUsecase {
	uc.paymentProvider = provider
	return uc
}

func (uc *AuctionUsecase) CreateLot(ctx context.Context, req *v1.CreateLotRequest, mainAccountID, ownerUserID string) (*v1.Lot, error) {
	lot, err := NewLotFromRequest(idgen.New("lot"), req)
	if err != nil {
		return nil, err
	}
	lot.MainAccountId = strings.TrimSpace(mainAccountID)
	if lot.RoomId == "" {
		room, err := uc.EnsureDefaultRoom(ctx, lot.MainAccountId, ownerUserID)
		if err != nil {
			return nil, err
		}
		lot.RoomId = room.ID
	} else if err := uc.ensureRoomInMainAccount(ctx, lot.RoomId, lot.MainAccountId); err != nil {
		return nil, err
	}
	event := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CREATED, lot)
	if err := uc.lots.Create(ctx, lot, ownerUserID, []*v1.AuctionEvent{event}); err != nil {
		return nil, err
	}
	uc.broadcastCommittedBestEffort(ctx, "create_lot", event)
	return proto.Clone(lot).(*v1.Lot), nil
}

func (uc *AuctionUsecase) CreateLotDraft(ctx context.Context, req *v1.CreateLotRequest, mainAccountID, ownerUserID string) (*v1.Lot, error) {
	lot, err := NewLotDraftFromRequest(idgen.New("lot"), req, false)
	if err != nil {
		return nil, err
	}
	lot.MainAccountId = strings.TrimSpace(mainAccountID)
	if lot.RoomId == "" {
		room, err := uc.EnsureDefaultRoom(ctx, lot.MainAccountId, ownerUserID)
		if err != nil {
			return nil, err
		}
		lot.RoomId = room.ID
	} else if err := uc.ensureRoomInMainAccount(ctx, lot.RoomId, lot.MainAccountId); err != nil {
		return nil, err
	}
	if err := uc.lots.Create(ctx, lot, ownerUserID, nil); err != nil {
		return nil, err
	}
	return proto.Clone(lot).(*v1.Lot), nil
}

func (uc *AuctionUsecase) PatchLotDraft(ctx context.Context, req *v1.PatchLotDraftRequest, mainAccountID, ownerUserID string) (*v1.Lot, error) {
	if req == nil {
		return nil, errors.New("patch lot draft request is required")
	}
	if req.GetLotId() == "" {
		return nil, errors.New("lot id is required")
	}
	lot, err := uc.lots.FindByID(ctx, req.GetLotId())
	if err != nil {
		return nil, err
	}
	if err := ensureLotMainAccount(lot, mainAccountID); err != nil {
		return nil, err
	}
	expectedVersion := lot.Version
	if err := ApplyDraftPatch(lot, req); err != nil {
		return nil, err
	}
	lot.MainAccountId = strings.TrimSpace(mainAccountID)
	if lot.RoomId == "" {
		room, err := uc.EnsureDefaultRoom(ctx, mainAccountID, ownerUserID)
		if err != nil {
			return nil, err
		}
		lot.RoomId = room.ID
	} else if err := uc.ensureRoomInMainAccount(ctx, lot.RoomId, mainAccountID); err != nil {
		return nil, err
	}
	if err := uc.lots.Save(ctx, lot, expectedVersion, nil); err != nil {
		return nil, err
	}
	if err := uc.lots.AttachAssets(ctx, ownerUserID, lot); err != nil {
		return nil, err
	}
	return proto.Clone(lot).(*v1.Lot), nil
}

func (uc *AuctionUsecase) QueueLot(ctx context.Context, lotID, mainAccountID, ownerUserID string) (*v1.Lot, int32, error) {
	if lotID == "" {
		return nil, 0, errors.New("lot id is required")
	}
	lot, queuePosition, events, err := uc.lots.QueueLotAsNext(ctx, lotID, mainAccountID, ownerUserID, clock.NowMs())
	if err != nil {
		return nil, 0, err
	}
	uc.broadcastCommittedBestEffort(ctx, "queue_lot", events...)
	return proto.Clone(lot).(*v1.Lot), queuePosition, nil
}

func (uc *AuctionUsecase) GetLot(ctx context.Context, lotID string) (*v1.Lot, error) {
	if lotID == "" {
		return nil, fmt.Errorf("%w: lot id is required", apperr.ErrInvalidArgument)
	}
	lot, err := uc.lots.FindByID(ctx, lotID)
	if err != nil {
		return nil, err
	}
	if err := uc.ensureActiveRoom(ctx, lot.GetRoomId()); err != nil {
		return nil, err
	}
	return lot, nil
}

func (uc *AuctionUsecase) FindLotsByIDs(ctx context.Context, lotIDs []string) ([]*v1.Lot, error) {
	if uc == nil || uc.lots == nil {
		return nil, errors.New("lot repository is required")
	}
	normalized, err := normalizeBatchLotIDs(lotIDs)
	if err != nil || len(normalized) == 0 {
		return nil, err
	}
	if uc.batchLots != nil {
		return uc.batchLots.FindByIDs(ctx, normalized)
	}
	// Compatibility for in-memory/test repositories. Production Store implements
	// LotBatchRepository and therefore never takes this per-item fallback.
	lots := make([]*v1.Lot, 0, len(normalized))
	for _, lotID := range normalized {
		lot, err := uc.lots.FindByID(ctx, lotID)
		if errors.Is(err, apperr.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		lots = append(lots, lot)
	}
	return lots, nil
}

func normalizeBatchLotIDs(lotIDs []string) ([]string, error) {
	if len(lotIDs) > 100 {
		return nil, fmt.Errorf("%w: at most 100 lot ids are allowed", apperr.ErrInvalidArgument)
	}
	seen := make(map[string]struct{}, len(lotIDs))
	normalized := make([]string, 0, len(lotIDs))
	for _, lotID := range lotIDs {
		lotID = strings.TrimSpace(lotID)
		if lotID == "" || len(lotID) > 64 || strings.ContainsAny(lotID, "\r\n\x00") {
			return nil, fmt.Errorf("%w: lot id is invalid", apperr.ErrInvalidArgument)
		}
		if _, duplicate := seen[lotID]; duplicate {
			continue
		}
		seen[lotID] = struct{}{}
		normalized = append(normalized, lotID)
	}
	return normalized, nil
}

func (uc *AuctionUsecase) ListLots(ctx context.Context, roomID string, status v1.LotStatus) ([]*v1.Lot, error) {
	if err := uc.ensureActiveRoom(ctx, roomID); err != nil {
		return nil, err
	}
	return uc.lots.List(ctx, roomID, status)
}

func (uc *AuctionUsecase) ListLotsByQuery(ctx context.Context, query LotQuery) (LotList, error) {
	query.Page, query.PageSize = NormalizePagination(query.Page, query.PageSize)
	query.View = strings.ToLower(strings.TrimSpace(query.View))
	query.MainAccountID = strings.TrimSpace(query.MainAccountID)
	if query.MainAccountID == "" {
		return LotList{}, fmt.Errorf("%w: main account id is required", apperr.ErrPermissionDenied)
	}
	switch query.View {
	case "", "all", "current", "history", "library":
	default:
		return LotList{}, fmt.Errorf("%w: unsupported lot list view: %s", apperr.ErrInvalidArgument, query.View)
	}
	return uc.lots.ListLots(ctx, query)
}

func (uc *AuctionUsecase) StartLot(ctx context.Context, lotID, mainAccountID string) (*v1.Lot, error) {
	if lotID == "" {
		return nil, errors.New("lot id is required")
	}

	lot, err := uc.lots.FindByID(ctx, lotID)
	if err != nil {
		return nil, err
	}
	if err := ensureLotMainAccount(lot, mainAccountID); err != nil {
		return nil, err
	}
	if uc.runtimeCommands == nil {
		return nil, errors.New("runtime command repository is required")
	}
	result, err := uc.runtimeCommands.ExecuteStartLot(ctx, lot, requestctx.TraceID(ctx))
	if err != nil {
		return nil, mapRuntimeCommandError(err)
	}
	if result.SourceLot == nil || result.Fact == nil {
		return nil, errors.New("runtime start result is incomplete")
	}
	started := LotFromRuntimeFact(result.SourceLot, result.Fact)
	event := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED, started)
	event.Ranking = RankingFromRuntimeFact(result.Fact)
	uc.broadcastRuntimeBestEffort(ctx, result.Fact, event)
	return proto.Clone(started).(*v1.Lot), nil
}

func (uc *AuctionUsecase) PlaceBid(ctx context.Context, req *v1.PlaceBidRequest, bidderID, nickname string, avatarURLs ...string) (*v1.Lot, *v1.Bid, []*v1.RankingItem, error) {
	if req == nil {
		return nil, nil, nil, errors.New("place bid request is required")
	}
	if req.GetLotId() == "" {
		return nil, nil, nil, errors.New("lot id is required")
	}
	if bidderID == "" {
		return nil, nil, nil, errors.New("user id is required")
	}
	if nickname == "" {
		return nil, nil, nil, errors.New("nickname is required")
	}
	if req.GetAmount() == nil || req.GetAmount().GetCurrency() == "" {
		return nil, nil, nil, fmt.Errorf("%w: bid amount and currency are required", apperr.ErrInvalidArgument)
	}
	if req.GetIdempotencyKey() == "" {
		return nil, nil, nil, fmt.Errorf("%w: bid idempotency key is required", apperr.ErrInvalidArgument)
	}
	avatarURL := bidderAvatarURL(bidderID, avatarURLs...)
	depositLot, err := uc.lots.FindCoreByID(ctx, req.GetLotId())
	if err != nil {
		return nil, nil, nil, err
	}
	if err := uc.ensureDepositHeld(ctx, depositLot, bidderID); err != nil {
		return depositLot, nil, nil, err
	}
	if uc.runtime == nil {
		return nil, nil, nil, errors.New("auction runtime repository is required")
	}
	return uc.placeBidRuntime(ctx, req, bidderID, nickname, avatarURL)
}

func (uc *AuctionUsecase) placeBidRuntime(ctx context.Context, req *v1.PlaceBidRequest, bidderID, nickname, avatarURL string) (*v1.Lot, *v1.Bid, []*v1.RankingItem, error) {
	nowMs := clock.NowMs()
	lot := &v1.Lot{Id: req.GetLotId()}
	bidID := idgen.New("bid")
	result, err := uc.runtime.PlaceBidRuntime(ctx, lot, req, bidderID, nickname, avatarURL, bidID, nowMs)
	if err != nil {
		rejectLot := lot
		if reject, ok := RuntimeBidRejectFromError(err); ok {
			rejectLot = reject.Lot(req.GetLotId(), req.GetAmount())
		}
		return proto.Clone(rejectLot).(*v1.Lot), nil, nil, err
	}
	if result.Replayed {
		replayLot, replayBid, replayRanking, found, replayErr := uc.replayBidByIdempotencyKey(ctx, lot.Id, bidderID, req.GetIdempotencyKey())
		if replayErr == nil && found {
			return replayLot, replayBid, replayRanking, nil
		}
		if replayErr != nil {
			slog.Warn("runtime replay lookup failed; returning cached runtime result",
				"lot_id", lot.Id,
				"idempotency_key", req.GetIdempotencyKey(),
				"error", replayErr,
			)
		}
	}
	if result.Lot == nil || result.Bid == nil {
		return nil, nil, nil, errors.New("runtime bid result is incomplete")
	}
	bid := proto.Clone(result.Bid).(*v1.Bid)
	uc.bids.CacheIdempotencyKey(ctx, lot.Id, bidderID, req.GetIdempotencyKey(), bid)
	return proto.Clone(result.Lot).(*v1.Lot), bid, result.Ranking, nil
}

func mapRuntimeCommandError(err error) error {
	if err == nil {
		return nil
	}
	var rejection *RuntimeDecisionError
	if !errors.As(err, &rejection) {
		return err
	}
	switch rejection.Code {
	case "ROOM_HAS_ACTIVE_LOT":
		return fmt.Errorf("%w: %s", apperr.ErrRoomActiveLotExists, rejection.Code)
	case RuntimeCodeStateMissing, RuntimeCodeNotActive, RuntimeCodeLotFrozen:
		return fmt.Errorf("%w: %s", apperr.ErrRuntimeProjectionGap, rejection.Code)
	case RuntimeCodeBidNotLive:
		return fmt.Errorf("%w: %s", apperr.ErrBidNotLive, rejection.Code)
	case RuntimeCodeBidEnded:
		return fmt.Errorf("%w: %s", apperr.ErrBidEnded, rejection.Code)
	case RuntimeCodeNotExpired:
		return fmt.Errorf("%w: %s", apperr.ErrInvalidArgument, rejection.Code)
	default:
		return fmt.Errorf("%w: %s", apperr.ErrInvalidArgument, rejection.Code)
	}
}

func (uc *AuctionUsecase) replayBidByIdempotencyKey(ctx context.Context, lotID, userID, key string) (*v1.Lot, *v1.Bid, []*v1.RankingItem, bool, error) {
	old, found, err := uc.bids.FindByIdempotencyKey(ctx, lotID, userID, key)
	if err != nil || !found {
		return nil, nil, nil, false, err
	}
	lot, err := uc.lots.FindByID(ctx, lotID)
	if err != nil {
		return nil, nil, nil, false, err
	}
	bids, err := uc.bids.ListByLot(ctx, lotID)
	if err != nil {
		return nil, nil, nil, false, err
	}
	return proto.Clone(lot).(*v1.Lot), old, BuildRealtimeRanking(bids), true, nil
}

func (uc *AuctionUsecase) RevealTrustCard(ctx context.Context, lotID, mainAccountID, cardID, operatorID string) (*v1.Lot, *v1.TrustRevealCard, error) {
	if lotID == "" {
		return nil, nil, errors.New("lot id is required")
	}
	if cardID == "" {
		return nil, nil, errors.New("trust card id is required")
	}
	if uc.presentations == nil {
		return nil, nil, errors.New("lot presentation repository is required")
	}

	var conflict error
	for attempt := 0; attempt < presentationWriteAttempts; attempt++ {
		lot, err := uc.lots.FindByID(ctx, lotID)
		if err != nil {
			return nil, nil, err
		}
		if err := ensureLotMainAccount(lot, mainAccountID); err != nil {
			return nil, nil, err
		}
		lot, ranking, err := uc.presentationRuntimeSnapshot(ctx, lot)
		if err != nil {
			return nil, nil, err
		}
		expectedVersion := lot.GetPresentationVersion()
		card, err := RevealTrustCard(lot, cardID, clock.NowMs())
		if err != nil {
			return nil, nil, err
		}
		if lot.GetPresentationVersion() == expectedVersion {
			return proto.Clone(lot).(*v1.Lot), proto.Clone(card).(*v1.TrustRevealCard), nil
		}
		event := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_TRUST_REVEALED, lot)
		event.TrustCard = proto.Clone(card).(*v1.TrustRevealCard)
		event.Ranking = ranking
		if err := uc.presentations.SaveLotPresentation(ctx, lot, expectedVersion, []*v1.AuctionEvent{event}); err != nil {
			if apperr.IsLotVersionConflict(err) {
				conflict = err
				continue
			}
			return nil, nil, err
		}
		uc.broadcastCommittedBestEffort(ctx, "reveal_trust_card", event)
		return proto.Clone(lot).(*v1.Lot), proto.Clone(card).(*v1.TrustRevealCard), nil
	}
	return nil, nil, conflict
}

func (uc *AuctionUsecase) StartDuel(ctx context.Context, lotID, mainAccountID, operatorID, userAID, userBID string) (*v1.Lot, *v1.DuelState, error) {
	if lotID == "" {
		return nil, nil, errors.New("lot id is required")
	}
	if uc.presentations == nil {
		return nil, nil, errors.New("lot presentation repository is required")
	}

	var conflict error
	for attempt := 0; attempt < presentationWriteAttempts; attempt++ {
		lot, err := uc.lots.FindByID(ctx, lotID)
		if err != nil {
			return nil, nil, err
		}
		if err := ensureLotMainAccount(lot, mainAccountID); err != nil {
			return nil, nil, err
		}
		lot, ranking, err := uc.presentationRuntimeSnapshot(ctx, lot)
		if err != nil {
			return nil, nil, err
		}
		expectedVersion := lot.GetPresentationVersion()
		if err := StartDuel(lot, ranking, clock.NowMs(), userAID, userBID); err != nil {
			return nil, nil, err
		}
		event := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_DUEL_STARTED, lot)
		event.Ranking = ranking
		event.DuelState = proto.Clone(lot.DuelState).(*v1.DuelState)
		if err := uc.presentations.SaveLotPresentation(ctx, lot, expectedVersion, []*v1.AuctionEvent{event}); err != nil {
			if apperr.IsLotVersionConflict(err) {
				conflict = err
				continue
			}
			return nil, nil, err
		}
		uc.broadcastCommittedBestEffort(ctx, "start_duel", event)
		return proto.Clone(lot).(*v1.Lot), proto.Clone(lot.DuelState).(*v1.DuelState), nil
	}
	return nil, nil, conflict
}

func (uc *AuctionUsecase) presentationRuntimeSnapshot(ctx context.Context, base *v1.Lot) (*v1.Lot, []*v1.RankingItem, error) {
	if uc.runtime == nil {
		return nil, nil, errors.New("auction runtime repository is required for live presentation commands")
	}
	snapshot, err := uc.runtime.SnapshotRuntime(ctx, base)
	if err != nil {
		return nil, nil, err
	}
	if snapshot.GetCurrentLot() == nil || snapshot.GetCurrentLot().GetId() != base.GetId() ||
		snapshot.GetCurrentLot().GetMainAccountId() != base.GetMainAccountId() {
		return nil, nil, errors.New("auction runtime presentation snapshot identity mismatch")
	}
	return snapshot.GetCurrentLot(), snapshot.GetRanking(), nil
}

func (uc *AuctionUsecase) SettleLot(ctx context.Context, lotID, mainAccountID, operatorID string) (*v1.Lot, error) {
	if lotID == "" {
		return nil, errors.New("lot id is required")
	}

	lot, err := uc.lots.FindByID(ctx, lotID)
	if err != nil {
		return nil, err
	}
	if err := ensureLotMainAccount(lot, mainAccountID); err != nil {
		return nil, err
	}
	if uc.runtimeCommands == nil {
		return nil, errors.New("runtime command repository is required")
	}
	orderID, err := eventcontract.RuntimeOrderID(lotID)
	if err != nil {
		return nil, err
	}
	fact, err := uc.runtimeCommands.ExecuteCloseIfExpired(ctx, lotID, orderID, requestctx.TraceID(ctx))
	if err != nil {
		return nil, mapRuntimeCommandError(err)
	}
	closed := LotFromRuntimeFact(lot, fact)
	ranking := RankingFromRuntimeFact(fact)
	closedEvent := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, closed)
	closedEvent.Ranking = ranking
	if closed.GetStatus() == v1.LotStatus_LOT_STATUS_SETTLED {
		settledEvent := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED, closed)
		settledEvent.Ranking = ranking
		orderEvent := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED, closed)
		orderEvent.Ranking = ranking
		orderEvent.Reason = orderCreatedPublicReason
		uc.broadcastRuntimeBestEffort(ctx, fact, closedEvent, settledEvent, orderEvent)
	} else {
		closedEvent.Reason = closed.GetCancelReason()
		failedEvent := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED, closed)
		failedEvent.Ranking = ranking
		failedEvent.Reason = closed.GetCancelReason()
		uc.broadcastRuntimeBestEffort(ctx, fact, closedEvent, failedEvent)
	}
	return proto.Clone(closed).(*v1.Lot), nil
}

func (uc *AuctionUsecase) GetLotResult(ctx context.Context, lotID string, viewer LotResultViewer) (*LotResult, error) {
	if lotID == "" {
		return nil, fmt.Errorf("%w: lot id is required", apperr.ErrInvalidArgument)
	}
	lot, err := uc.lots.FindByID(ctx, lotID)
	if err != nil {
		return nil, err
	}
	if err := uc.ensureActiveRoom(ctx, lot.GetRoomId()); err != nil {
		return nil, err
	}
	result := &LotResult{Lot: LotForViewer(lot, viewer), AuctionState: AuctionStateOf(lot)}
	if uc.orders != nil {
		order, found, err := uc.orders.FindOrderByLot(ctx, lotID)
		if err != nil {
			return nil, err
		}
		if found && viewer.CanViewOrder(order) {
			summary := order.Summary()
			result.Order = &summary
		}
	}
	return result, nil
}

func (uc *AuctionUsecase) ListOrdersByBuyer(ctx context.Context, buyerUserID string) ([]OrderSummary, error) {
	if buyerUserID == "" {
		return nil, fmt.Errorf("%w: buyer user id is required", apperr.ErrInvalidArgument)
	}
	if uc.orders == nil {
		return nil, errors.New("order repository is required")
	}
	list, err := uc.ListOrders(ctx, OrderQuery{BuyerUserID: buyerUserID})
	if err != nil {
		return nil, err
	}
	return list.Orders, nil
}

func (uc *AuctionUsecase) ListOrders(ctx context.Context, query OrderQuery) (OrderList, error) {
	if uc.orders == nil {
		return OrderList{}, errors.New("order repository is required")
	}
	query.Page, query.PageSize = NormalizePagination(query.Page, query.PageSize)
	return uc.orders.ListOrders(ctx, query)
}

func (uc *AuctionUsecase) ListRoomEvents(ctx context.Context, query RoomEventQuery) (RoomEventList, error) {
	if query.RoomID == "" {
		return RoomEventList{}, errors.New("room id is required")
	}
	if uc.eventsStore == nil {
		return RoomEventList{}, errors.New("event repository is required")
	}
	return uc.eventsStore.ListRoomEvents(ctx, query)
}

func (uc *AuctionUsecase) ListOrdersByBuyerQuery(ctx context.Context, buyerUserID string, query OrderQuery) (OrderList, error) {
	if buyerUserID == "" {
		return OrderList{}, fmt.Errorf("%w: buyer user id is required", apperr.ErrInvalidArgument)
	}
	query.BuyerUserID = buyerUserID
	query.Buyer = ""
	return uc.ListOrders(ctx, query)
}

func (uc *AuctionUsecase) ListBidRecordsByBuyer(ctx context.Context, buyerUserID string, query BidRecordQuery) (BidRecordList, error) {
	if buyerUserID == "" {
		return BidRecordList{}, fmt.Errorf("%w: buyer user id is required", apperr.ErrInvalidArgument)
	}
	query.Page, query.PageSize = NormalizePagination(query.Page, query.PageSize)
	return uc.bids.ListBidRecordsByBuyer(ctx, buyerUserID, query)
}

func (uc *AuctionUsecase) CreateDepositHold(ctx context.Context, buyerUserID, buyerNickname string, req CreateDepositHoldRequest) (*DepositHoldResult, error) {
	buyerUserID = strings.TrimSpace(buyerUserID)
	buyerNickname = strings.TrimSpace(buyerNickname)
	req.LotID = strings.TrimSpace(req.LotID)
	req.AddressID = strings.TrimSpace(req.AddressID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if buyerUserID == "" {
		return nil, apperr.ErrUnauthenticated
	}
	if req.LotID == "" {
		return nil, fmt.Errorf("%w: lot id is required", apperr.ErrInvalidArgument)
	}
	if req.AddressID == "" {
		return nil, fmt.Errorf("%w: address id is required", apperr.ErrAddressRequired)
	}
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: deposit idempotency key is required", apperr.ErrInvalidArgument)
	}
	if uc.deposits == nil || uc.addresses == nil || uc.lots == nil {
		return nil, errors.New("deposit, address and lot repositories are required")
	}
	if uc.paymentProvider == nil || strings.TrimSpace(uc.paymentProvider.Name()) == "" {
		return nil, apperr.ErrPaymentProviderNotConfigured
	}
	if existing, found, err := uc.deposits.FindDepositHoldByIdempotencyKey(ctx, req.LotID, buyerUserID, req.IdempotencyKey); err != nil {
		return nil, err
	} else if found {
		return &DepositHoldResult{DepositHold: *existing, Paid: existing.Status == DepositStatusHeld}, nil
	}
	if existing, found, err := uc.deposits.FindDepositHoldByLotBuyer(ctx, req.LotID, buyerUserID); err != nil {
		return nil, err
	} else if found && existing.Status == DepositStatusHeld {
		return &DepositHoldResult{DepositHold: *existing, Paid: true}, nil
	} else if found {
		return nil, fmt.Errorf("%w: deposit already exists with status %s", apperr.ErrInvalidArgument, existing.Status)
	}
	lot, err := uc.lots.FindCoreByID(ctx, req.LotID)
	if err != nil {
		return nil, err
	}
	depositAmount, depositCurrency := requiredDepositMoney(lot)
	address, err := uc.addresses.FindDeliveryAddress(ctx, buyerUserID, req.AddressID)
	if err != nil {
		return nil, err
	}
	nowMs := clock.NowMs()
	hold := DepositHold{
		ID:              idgen.New("deposit"),
		MainAccountID:   lot.GetMainAccountId(),
		RoomID:          lot.GetRoomId(),
		LotID:           lot.GetId(),
		BuyerUserID:     buyerUserID,
		BuyerNickname:   buyerNickname,
		Status:          DepositStatusProcessing,
		Amount:          depositAmount,
		Currency:        depositCurrency,
		PaymentProvider: uc.paymentProvider.Name(),
		IdempotencyKey:  req.IdempotencyKey,
		AddressID:       address.ID,
		AddressSnapshot: address.Snapshot(),
		CreatedAtUnixMs: nowMs,
		UpdatedAtUnixMs: nowMs,
	}
	payment, err := uc.paymentProvider.Pay(ctx, PaymentProviderRequest{
		BusinessID:     hold.ID,
		BusinessType:   "auction_deposit",
		UserID:         buyerUserID,
		Amount:         hold.Amount,
		Currency:       hold.Currency,
		IdempotencyKey: hold.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	hold.PaymentID = payment.ProviderPaymentID
	hold.Status = DepositStatusHeld
	hold.HeldAtUnixMs = payment.PaidAtUnixMs
	hold.UpdatedAtUnixMs = payment.PaidAtUnixMs
	committed, err := uc.deposits.CommitDepositHold(ctx, hold)
	if err != nil {
		return nil, err
	}
	return &DepositHoldResult{DepositHold: *committed, Paid: committed.Status == DepositStatusHeld}, nil
}

func (uc *AuctionUsecase) GetMyDepositHold(ctx context.Context, lotID, buyerUserID string) (*DepositHold, bool, error) {
	if uc.deposits == nil {
		return nil, false, errors.New("deposit repository is required")
	}
	lotID = strings.TrimSpace(lotID)
	buyerUserID = strings.TrimSpace(buyerUserID)
	if lotID == "" {
		return nil, false, fmt.Errorf("%w: lot id is required", apperr.ErrInvalidArgument)
	}
	if buyerUserID == "" {
		return nil, false, apperr.ErrUnauthenticated
	}
	return uc.deposits.FindDepositHoldByLotBuyer(ctx, lotID, buyerUserID)
}

func requiredDepositMoney(lot *v1.Lot) (int64, string) {
	if lot == nil {
		return 0, "CNY"
	}
	deposit := lot.GetDepositAmount()
	currency := strings.TrimSpace(deposit.GetCurrency())
	if currency == "" {
		currency = "CNY"
	}
	amount := deposit.GetAmount()
	if amount < 0 {
		amount = 0
	}
	return amount, currency
}

func (uc *AuctionUsecase) ensureDepositHeld(ctx context.Context, lot *v1.Lot, bidderID string) error {
	if lot == nil {
		return apperr.ErrNotFound
	}
	depositAmount, depositCurrency := requiredDepositMoney(lot)
	if uc.deposits == nil && depositAmount <= 0 {
		return nil
	}
	if uc.deposits == nil {
		return errors.New("deposit repository is required")
	}
	hold, found, err := uc.deposits.FindDepositHoldByLotBuyer(ctx, lot.GetId(), strings.TrimSpace(bidderID))
	if err != nil {
		return err
	}
	if !found || hold.Status != DepositStatusHeld {
		return fmt.Errorf("%w: lot deposit is required", apperr.ErrDepositRequired)
	}
	if hold.Amount != depositAmount || hold.Currency != depositCurrency {
		return fmt.Errorf("%w: deposit amount changed, please pay again", apperr.ErrDepositRequired)
	}
	return nil
}

func bidderAvatarURL(userID string, avatarURLs ...string) string {
	for _, avatarURL := range avatarURLs {
		if trimmed := strings.TrimSpace(avatarURL); trimmed != "" {
			return trimmed
		}
	}
	return userbiz.AvatarURLForUserID(userID)
}

func (uc *AuctionUsecase) MockPayOrder(ctx context.Context, buyerUserID, orderID string, req MockPayRequest) (*PaymentResult, error) {
	if buyerUserID == "" {
		return nil, fmt.Errorf("%w: buyer user id is required", apperr.ErrInvalidArgument)
	}
	if orderID == "" {
		return nil, fmt.Errorf("%w: order id is required", apperr.ErrInvalidArgument)
	}
	if uc.orders == nil || uc.payments == nil {
		return nil, errors.New("order and payment repositories are required")
	}
	order, err := uc.orders.FindOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.BuyerUserID != buyerUserID {
		return nil, fmt.Errorf("%w: order does not belong to current buyer", apperr.ErrPermissionDenied)
	}
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: payment idempotency key is required", apperr.ErrInvalidArgument)
	}
	if existing, found, err := uc.payments.FindPaymentByIdempotencyKey(ctx, orderID, req.IdempotencyKey); err != nil {
		return nil, err
	} else if found {
		return uc.replayPayment(ctx, orderID, existing)
	}
	if req.Currency == "" {
		return nil, fmt.Errorf("%w: payment currency is required", apperr.ErrInvalidArgument)
	}
	expectedVersion := order.Version
	nowMs := clock.NowMs()
	if order.ExpiresAtUnixMs > 0 && order.ExpiresAtUnixMs <= nowMs {
		return nil, fmt.Errorf("%w: order payment window expired", apperr.ErrInvalidArgument)
	}
	payment, err := NewPayment(idgen.New("pay"), *order, req.IdempotencyKey, req.Amount, req.Currency, nowMs)
	if err != nil {
		return nil, err
	}
	if order.EnrichmentStatus == orderenrichment.StatusPending {
		return nil, fmt.Errorf("%w: order details are still being prepared", apperr.ErrOrderEnrichmentPending)
	}
	if err := payment.MarkProcessing(nowMs); err != nil {
		return nil, err
	}
	if err := payment.MarkSuccess(nowMs); err != nil {
		return nil, err
	}
	if err := MarkOrderPaid(order, *payment, nowMs); err != nil {
		return nil, err
	}
	lot, err := uc.lots.FindByID(ctx, order.LotID)
	if err != nil {
		return nil, err
	}
	event := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_PAYMENT_SUCCESS, lot)
	event.Reason = paymentSuccessPublicReason
	if err := uc.payments.CommitPaymentSuccess(ctx, *payment, *order, expectedVersion, []*v1.AuctionEvent{event}); err != nil {
		if existing, found, replayErr := uc.payments.FindPaymentByIdempotencyKey(ctx, orderID, req.IdempotencyKey); replayErr != nil {
			return nil, replayErr
		} else if found {
			return uc.replayPayment(ctx, orderID, existing)
		}
		return nil, err
	}
	uc.broadcastCommittedBestEffort(ctx, "payment_success", event)
	return &PaymentResult{Order: order.Summary(), Payment: payment.Summary(), Paid: true}, nil
}

func (uc *AuctionUsecase) replayPayment(ctx context.Context, orderID string, payment *Payment) (*PaymentResult, error) {
	if payment == nil {
		return nil, fmt.Errorf("%w: payment is required", apperr.ErrInvalidArgument)
	}
	order, err := uc.orders.FindOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return &PaymentResult{Order: order.Summary(), Payment: payment.Summary(), Paid: payment.Status == PaymentStatusSuccess}, nil
}

func (uc *AuctionUsecase) CancelLot(ctx context.Context, lotID, mainAccountID, operatorID, reason string) (*v1.Lot, error) {
	if lotID == "" {
		return nil, errors.New("lot id is required")
	}

	lot, err := uc.lots.FindByID(ctx, lotID)
	if err != nil {
		return nil, err
	}
	if err := ensureLotMainAccount(lot, mainAccountID); err != nil {
		return nil, err
	}
	if uc.runtimeCommands == nil {
		return nil, errors.New("runtime command repository is required")
	}
	fact, err := uc.runtimeCommands.ExecuteCancelLot(ctx, lot.GetId(), reason, operatorID, requestctx.TraceID(ctx))
	if err != nil {
		return nil, mapRuntimeCommandError(err)
	}
	cancelled := LotFromRuntimeFact(lot, fact)
	ranking := RankingFromRuntimeFact(fact)
	event := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED, cancelled)
	event.Ranking = ranking
	event.Reason = cancelled.GetCancelReason()
	closedEvent := newAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED, cancelled)
	closedEvent.Ranking = ranking
	closedEvent.Reason = cancelled.GetCancelReason()
	uc.broadcastRuntimeBestEffort(ctx, fact, event, closedEvent)
	return proto.Clone(cancelled).(*v1.Lot), nil
}

func (uc *AuctionUsecase) broadcast(ctx context.Context, events ...*v1.AuctionEvent) error {
	if uc.events == nil {
		return nil
	}
	for _, event := range events {
		if event == nil {
			continue
		}
		if err := uc.events.Publish(ctx, event); err != nil {
			return err
		}
		switch event.GetType() {
		case v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_STARTED:
			observability.AddActiveLots(event.GetRoomId(), 1)
		case v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED:
			observability.AddActiveLots(event.GetRoomId(), -1)
		case v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED:
			observability.IncOrderCreated()
		}
	}
	return nil
}

func (uc *AuctionUsecase) broadcastRuntimeBestEffort(ctx context.Context, fact *v1.RuntimeFactV1, events ...*v1.AuctionEvent) {
	if err := uc.broadcast(ctx, events...); err != nil {
		slog.Warn("runtime command accepted but realtime publish failed; clients recover from authoritative snapshot",
			"event_id", fact.GetEventId(), "lot_id", fact.GetLotId(), "lot_version", fact.GetLotVersion(), "error", err,
		)
	}
}

func (uc *AuctionUsecase) broadcastCommittedBestEffort(ctx context.Context, operation string, events ...*v1.AuctionEvent) {
	if err := uc.broadcast(ctx, events...); err != nil {
		slog.Warn("committed auction change realtime publish failed; clients recover from authoritative snapshot",
			"operation", operation, "error", err,
		)
	}
}

func (uc *AuctionUsecase) Snapshot(ctx context.Context, roomID string) (*v1.RoomSnapshot, error) {
	room, err := uc.findActiveRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}

	var current *v1.Lot
	runtimeSnapshotAvailable := false
	if uc.runtime != nil {
		var runtimeLotID string
		var found bool
		if display, ok := uc.runtime.(AuctionRuntimeDisplay); ok {
			runtimeLotID, found, err = display.DisplayedRuntimeLotID(ctx, roomID)
			if err != nil {
				return nil, err
			}
		}
		if !found {
			runtimeLotID, found, err = uc.runtime.ActiveRuntimeLotID(ctx, roomID)
		}
		if err != nil {
			return nil, err
		}
		if found {
			current, err = uc.lots.FindByID(ctx, runtimeLotID)
			if err != nil {
				return nil, err
			}
			runtimeSnapshotAvailable = true
		}
	}

	state, err := uc.lots.FindRoomState(ctx, roomID, room.MainAccountID)
	if err != nil {
		return nil, err
	}
	if current == nil && state != nil && strings.TrimSpace(state.DisplayLotID) != "" {
		current, err = uc.lots.FindByID(ctx, state.DisplayLotID)
		if err != nil {
			return nil, err
		}
	}
	if current == nil && state != nil && strings.TrimSpace(state.ActiveLotID) != "" {
		lot, err := uc.lots.FindByID(ctx, state.ActiveLotID)
		if err != nil {
			if !apperr.IsNotFound(err) && !strings.Contains(err.Error(), "lot not found") {
				return nil, err
			}
		} else if IsAuctionOpenStatus(lot.Status) {
			current = lot
		}
	}

	snapshot := &v1.RoomSnapshot{
		RoomId:              roomID,
		RoomName:            room.Name,
		AnchorName:          room.Name,
		LiveSourceUrl:       room.LiveSourceURL,
		LiveStartedAtUnixMs: room.LiveStartedAtUnixMs,
		PlaybookStage:       v1.PlaybookStage_PLAYBOOK_STAGE_WARM_UP,
		ServerTimeUnixMs:    clock.NowMs(),
	}
	if current == nil {
		return snapshot, nil
	}
	if uc.runtime != nil && runtimeSnapshotAvailable {
		runtimeSnapshot, err := uc.runtime.SnapshotRuntime(ctx, current)
		if err != nil {
			return nil, err
		}
		runtimeSnapshot.RoomName = room.Name
		runtimeSnapshot.AnchorName = room.Name
		runtimeSnapshot.LiveSourceUrl = room.LiveSourceURL
		runtimeSnapshot.LiveStartedAtUnixMs = room.LiveStartedAtUnixMs
		return runtimeSnapshot, nil
	}

	bids, err := uc.bids.ListByLot(ctx, current.Id)
	if err != nil {
		return nil, err
	}
	snapshot.CurrentLot = current
	snapshot.Ranking = BuildRealtimeRanking(bids)
	start := 0
	if len(bids) > 20 {
		start = len(bids) - 20
	}
	for i := start; i < len(bids); i++ {
		snapshot.RecentBids = append(snapshot.RecentBids, bids[i])
	}
	snapshot.PlaybookStage = current.PlaybookStage
	return snapshot, nil
}
