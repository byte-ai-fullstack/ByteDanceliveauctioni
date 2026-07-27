package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
)

const (
	runtimeReplicaWaitTimeout = 50 * time.Millisecond
	runtimeRequiredReplicas   = 1
)

type runtimeCommandReplyV1 struct {
	OK               bool   `json:"ok"`
	Code             string `json:"code"`
	Message          string `json:"message"`
	Replayed         bool   `json:"replayed"`
	EventID          string `json:"event_id"`
	LotVersion       int64  `json:"lot_version"`
	OccurredAtUnixMs int64  `json:"occurred_at_unix_ms"`
	OrderID          string `json:"order_id"`
	Settled          bool   `json:"settled"`
	FactJSON         string `json:"fact_json"`
	CurrentPriceFen  int64  `json:"current_price_fen"`
	MinIncrementFen  int64  `json:"min_increment_fen"`
	MinimumBidFen    int64  `json:"minimum_bid_fen"`
	EndsAtUnixMs     int64  `json:"ends_at_unix_ms"`
}

// PlaceBidRuntime is the production AuctionRuntime adapter backed by the atomic Redis List outbox.
func (s *Store) PlaceBidRuntime(ctx context.Context, lot *v1.Lot, req *v1.PlaceBidRequest, bidderID, nickname, avatarURL, bidID string, _ int64) (auction.RuntimeBidResult, error) {
	if lot == nil || req == nil || req.GetAmount() == nil {
		return auction.RuntimeBidResult{}, fmt.Errorf("%w: lot, request, and amount are required", apperr.ErrInvalidArgument)
	}
	orderID, err := eventcontract.RuntimeOrderID(lot.GetId())
	if err != nil {
		return auction.RuntimeBidResult{}, err
	}
	fact, replayed, err := s.ExecutePlaceBid(ctx, lot.GetId(), req, bidderID, nickname, avatarURL, bidID, orderID, requestctx.TraceID(ctx))
	if err != nil {
		var rejection *auction.RuntimeDecisionError
		if errors.As(err, &rejection) {
			code := rejection.Code
			cause := apperr.ErrorForBusinessCode(code)
			if code == auction.RuntimeCodeStateMissing || code == auction.RuntimeCodeNotActive || code == auction.RuntimeCodeLotFrozen {
				code = string(apperr.CodeProjectionPending)
				cause = apperr.ErrRuntimeProjectionGap
			}
			return auction.RuntimeBidResult{}, &auction.RuntimeBidRejectError{
				Code: code, CurrentAmount: rejection.CurrentPrice, CurrentCurrency: req.GetAmount().GetCurrency(),
				MinIncrementAmount: rejection.MinIncrement, NextBidAmount: rejection.MinimumBid,
				LotVersion: rejection.LotVersion, EndsAtUnixMs: rejection.EndsAtUnixMs, Cause: cause,
			}
		}
		return auction.RuntimeBidResult{}, err
	}
	state := fact.GetStateAfter()
	accepted := fact.GetAcceptedBid()
	if state == nil || accepted == nil {
		return auction.RuntimeBidResult{}, errors.New("accepted runtime bid fact is incomplete")
	}
	updatedLot := auction.LotFromRuntimeFact(lot, fact)
	bid := &v1.Bid{
		Id: accepted.GetBidId(), LotId: fact.GetLotId(), UserId: accepted.GetUserId(), Nickname: accepted.GetNickname(),
		AvatarUrl: accepted.GetAvatarUrl(), Amount: &v1.Money{Amount: accepted.GetAmountFen(), Currency: state.GetCurrency()},
		CreatedAtUnixMs: accepted.GetAcceptedAtUnixMs(),
	}
	ranking := auction.RankingFromRuntimeFact(fact)
	extendCountBefore := state.GetExtendCount()
	if state.GetStatus() == v1.LotStatus_LOT_STATUS_EXTENDED && extendCountBefore > 0 {
		extendCountBefore--
	}
	return auction.RuntimeBidResult{
		Lot: updatedLot, Bid: bid, Ranking: ranking, ExtendCountBefore: extendCountBefore,
		RuntimeEventID: fact.GetEventId(), PreviousLotVersion: fact.GetPrevLotVersion(), LotVersion: fact.GetLotVersion(),
		OrderID: state.GetOrderId(), Replayed: replayed,
	}, nil
}

// ExecuteStartLot serializes draft edits and runtime creation on the MySQL lot
// row, then atomically creates the Redis aggregate and its outbox fact. Holding
// the row lock across the short Redis command prevents a stale configuration
// snapshot from starting while a draft patch commits concurrently.
func (s *Store) ExecuteStartLot(ctx context.Context, requested *v1.Lot, traceID string) (auction.RuntimeStartResult, error) {
	if s == nil || s.db == nil || s.redis == nil || requested == nil {
		return auction.RuntimeStartResult{}, errors.New("runtime start requires initialized MySQL, Redis, and lot")
	}
	lotID := strings.TrimSpace(requested.GetId())
	mainAccountID := strings.TrimSpace(requested.GetMainAccountId())
	if lotID == "" || mainAccountID == "" {
		return auction.RuntimeStartResult{}, fmt.Errorf("%w: lot id and main account id are required", apperr.ErrInvalidArgument)
	}
	if err := s.checkRuntimeAdmission(ctx); err != nil {
		return auction.RuntimeStartResult{}, err
	}

	var result auction.RuntimeStartResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model AuctionLotModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", lotID).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return fmt.Errorf("lock lot for runtime start: %w", err)
		}
		if model.MainAccountID != mainAccountID {
			return apperr.ErrPermissionDenied
		}
		lockedLot, err := modelToLot(&model)
		if err != nil {
			return fmt.Errorf("decode locked lot for runtime start: %w", err)
		}
		fact, err := s.executeStartLotRedis(ctx, lockedLot, traceID)
		if err != nil {
			return err
		}
		result = auction.RuntimeStartResult{
			SourceLot: proto.Clone(lockedLot).(*v1.Lot),
			Fact:      fact,
		}
		return nil
	})
	if err != nil {
		return auction.RuntimeStartResult{}, err
	}
	return result, nil
}

func (s *Store) executeStartLotRedis(ctx context.Context, lot *v1.Lot, traceID string) (*v1.RuntimeFactV1, error) {
	if s == nil || s.redis == nil || lot == nil {
		return nil, errors.New("runtime start requires an initialized Redis store and lot")
	}
	config, err := runtimeConfigFromLot(lot)
	if err != nil {
		return nil, err
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	traceID = runtimeTraceID(traceID, eventID)
	capPrice := ""
	if config.CapPriceFen != nil {
		capPrice = strconv.FormatInt(*config.CapPriceFen, 10)
	}
	shard := s.runtimeOutboxShard(lot.GetId())
	keys := []string{
		runtimeStateKey(lot.GetId()), runtimeRankingKey(lot.GetId()), runtimeRankMetaKey(lot.GetId()),
		runtimeParticipantsKey(lot.GetId()), runtimeRecentKey(lot.GetId()), runtimeIdempotencyHashKey(lot.GetId()),
		runtimeExpiringKey(), runtimeOutboxPendingKey(shard), runtimeRoomActiveLotKey(lot.GetRoomId()), runtimeFrozenLotKey(lot.GetId()),
		runtimeRoomDisplayLotKey(lot.GetRoomId()),
	}
	return s.executeRuntimeCommand(ctx, runtimeStartLotScriptV1, keys, []any{
		eventID, traceID, lot.GetId(), lot.GetRoomId(), lot.GetMainAccountId(), lot.GetTitle(), lot.GetImageUrl(),
		strconv.FormatInt(config.ConfigVersion, 10), strconv.Itoa(int(lot.GetStatus())), strconv.FormatInt(lot.GetVersion(), 10),
		config.Currency, strconv.FormatInt(config.StartPriceFen, 10), strconv.FormatInt(config.MinIncrementFen, 10), capPrice,
		strconv.FormatInt(config.DurationMs, 10), strconv.FormatInt(config.AntiSnipeWindowMs, 10), strconv.FormatInt(config.AntiSnipeExtendMs, 10),
		strconv.FormatInt(int64(config.MaxExtendCount), 10), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT)), strconv.FormatUint(uint64(eventcontract.RuntimeSchemaVersionV1), 10),
		strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
	}, lot.GetId())
}

// ExecutePlaceBid atomically adjudicates a bid and appends the exact resulting fact to the Redis outbox.
func (s *Store) ExecutePlaceBid(ctx context.Context, lotID string, req *v1.PlaceBidRequest, bidderID, nickname, avatarURL, bidID, orderID, traceID string) (*v1.RuntimeFactV1, bool, error) {
	if s == nil || s.redis == nil || strings.TrimSpace(lotID) == "" || req == nil || req.GetAmount() == nil {
		return nil, false, errors.New("runtime bid requires an initialized store, lot, request, and amount")
	}
	if err := s.checkRuntimeAdmission(ctx); err != nil {
		return nil, false, err
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, false, err
	}
	traceID = runtimeTraceID(traceID, eventID)
	shard := s.runtimeOutboxShard(lotID)
	keys := []string{
		runtimeStateKey(lotID), runtimeRankingKey(lotID), runtimeRankMetaKey(lotID), runtimeParticipantsKey(lotID),
		runtimeRecentKey(lotID), runtimeIdempotencyHashKey(lotID), runtimeExpiringKey(), runtimeOutboxPendingKey(shard),
		"", runtimeFrozenLotKey(lotID), "", // room pointers are filled from the immutable Redis state below.
	}
	roomID, err := s.runtimeRoomIdentity(ctx, lotID)
	if err != nil {
		return nil, false, err
	}
	keys[8] = runtimeRoomActiveLotKey(roomID)
	keys[10] = runtimeRoomDisplayLotKey(roomID)
	reply, fact, err := s.executeRuntimeCommandReply(ctx, runtimePlaceBidScriptV1, keys, []any{
		eventID, traceID, bidID, bidderID, nickname, auction.MaskBuyerNickname(nickname), avatarURL,
		strconv.FormatInt(req.GetAmount().GetAmount(), 10), req.GetAmount().GetCurrency(),
		runtimeIdempotencyField(bidderID, req.GetIdempotencyKey()), req.GetIdempotencyKey(), orderID,
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)), strconv.FormatUint(uint64(eventcontract.RuntimeSchemaVersionV1), 10),
		strconv.Itoa(eventcontract.MaxRuntimeFactBytes), strconv.FormatInt(auction.RealtimeRankingLimit(), 10), strconv.FormatInt(runtimeRecentLimit, 10),
		strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), strconv.FormatInt(s.outboxPendingLimit, 10),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
	}, lotID)
	if err != nil {
		return nil, false, err
	}
	return fact, reply.Replayed, nil
}

// ExecuteCancelLot atomically cancels any non-terminal runtime lot.
func (s *Store) ExecuteCancelLot(ctx context.Context, lotID, reason, operatorID, traceID string) (*v1.RuntimeFactV1, error) {
	if s == nil || s.redis == nil || strings.TrimSpace(lotID) == "" {
		return nil, errors.New("runtime cancel requires an initialized store and lot ID")
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	traceID = runtimeTraceID(traceID, eventID)
	roomID, err := s.runtimeRoomIdentity(ctx, lotID)
	if err != nil {
		return nil, err
	}
	shard := s.runtimeOutboxShard(lotID)
	keys := []string{
		runtimeStateKey(lotID), runtimeRankingKey(lotID), runtimeRankMetaKey(lotID), runtimeParticipantsKey(lotID),
		runtimeRecentKey(lotID), runtimeIdempotencyHashKey(lotID), runtimeExpiringKey(), runtimeOutboxPendingKey(shard),
		runtimeRoomActiveLotKey(roomID), runtimeFrozenLotKey(lotID), runtimeRoomDisplayLotKey(roomID),
	}
	return s.executeRuntimeCommand(ctx, runtimeCancelLotScriptV1, keys, []any{
		eventID, traceID, reason, operatorID, strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT)), strconv.FormatUint(uint64(eventcontract.RuntimeSchemaVersionV1), 10),
		strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
		strconv.FormatInt(auction.RealtimeRankingLimit(), 10),
	}, lotID)
}

// ExecuteCloseIfExpired atomically settles or fails a due runtime lot.
func (s *Store) ExecuteCloseIfExpired(ctx context.Context, lotID, orderID, traceID string) (*v1.RuntimeFactV1, error) {
	if s == nil || s.redis == nil || strings.TrimSpace(lotID) == "" {
		return nil, errors.New("runtime close requires an initialized store and lot ID")
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	traceID = runtimeTraceID(traceID, eventID)
	roomID, err := s.runtimeRoomIdentity(ctx, lotID)
	if err != nil {
		return nil, err
	}
	shard := s.runtimeOutboxShard(lotID)
	keys := []string{
		runtimeStateKey(lotID), runtimeRankingKey(lotID), runtimeRankMetaKey(lotID), runtimeParticipantsKey(lotID),
		runtimeRecentKey(lotID), runtimeIdempotencyHashKey(lotID), runtimeExpiringKey(), runtimeOutboxPendingKey(shard),
		runtimeRoomActiveLotKey(roomID), runtimeFrozenLotKey(lotID), runtimeRoomDisplayLotKey(roomID),
	}
	return s.executeRuntimeCommand(ctx, runtimeCloseIfExpiredScriptV1, keys, []any{
		eventID, traceID, orderID, strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED)), strconv.FormatUint(uint64(eventcontract.RuntimeSchemaVersionV1), 10),
		strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
		strconv.FormatInt(auction.RealtimeRankingLimit(), 10), auction.RuntimeExpiredNoBidReason,
	}, lotID)
}

// ExecuteSyncLotConfig atomically versions an allowed runtime configuration change.
func (s *Store) ExecuteSyncLotConfig(ctx context.Context, lot *v1.Lot, expectedConfigVersion int64, traceID string) (*v1.RuntimeFactV1, error) {
	if s == nil || s.redis == nil || lot == nil {
		return nil, errors.New("runtime config sync requires an initialized store and lot")
	}
	if err := s.checkRuntimeAdmission(ctx); err != nil {
		return nil, err
	}
	config, err := runtimeConfigFromLot(lot)
	if err != nil {
		return nil, err
	}
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		return nil, err
	}
	traceID = runtimeTraceID(traceID, eventID)
	capPrice := ""
	if config.CapPriceFen != nil {
		capPrice = strconv.FormatInt(*config.CapPriceFen, 10)
	}
	shard := s.runtimeOutboxShard(lot.GetId())
	keys := []string{
		runtimeStateKey(lot.GetId()), runtimeRankingKey(lot.GetId()), runtimeRankMetaKey(lot.GetId()),
		runtimeParticipantsKey(lot.GetId()), runtimeOutboxPendingKey(shard), runtimeFrozenLotKey(lot.GetId()),
	}
	return s.executeRuntimeCommand(ctx, runtimeSyncLotConfigScriptV1, keys, []any{
		eventID, traceID, strconv.FormatInt(expectedConfigVersion, 10), strconv.FormatInt(config.ConfigVersion, 10),
		config.LotID, config.RoomID, config.MainAccountID, config.Title, config.ImageURL, config.Currency,
		strconv.FormatInt(config.StartPriceFen, 10), strconv.FormatInt(config.MinIncrementFen, 10), capPrice,
		strconv.FormatInt(config.DurationMs, 10), strconv.FormatInt(config.AntiSnipeWindowMs, 10), strconv.FormatInt(config.AntiSnipeExtendMs, 10),
		strconv.FormatInt(int64(config.MaxExtendCount), 10), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG)),
		strconv.FormatUint(uint64(eventcontract.RuntimeSchemaVersionV1), 10), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
		strconv.FormatInt(auction.RealtimeRankingLimit(), 10),
	}, lot.GetId())
}

func (s *Store) executeRuntimeCommand(ctx context.Context, script *redis.Script, keys []string, args []any, lotID string) (*v1.RuntimeFactV1, error) {
	_, fact, err := s.executeRuntimeCommandReply(ctx, script, keys, args, lotID)
	return fact, err
}

func (s *Store) executeRuntimeCommandReply(ctx context.Context, script *redis.Script, keys []string, args []any, lotID string) (runtimeCommandReplyV1, *v1.RuntimeFactV1, error) {
	if script == nil || s == nil || s.redis == nil {
		return runtimeCommandReplyV1{}, nil, errors.New("runtime command executor is not initialized")
	}
	if s.runtimeGenerationGuard != nil {
		if _, err := s.runtimeGenerationGuard.AllowWrite(); err != nil {
			return runtimeCommandReplyV1{}, nil, fmt.Errorf("runtime command is frozen: %w", err)
		}
	}
	connection := s.redis.Conn()
	defer func() { _ = connection.Close() }()
	raw, err := script.Run(ctx, connection, keys, args...).Text()
	if err != nil {
		return runtimeCommandReplyV1{}, nil, fmt.Errorf("execute runtime Lua command: %w", err)
	}
	var reply runtimeCommandReplyV1
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		return runtimeCommandReplyV1{}, nil, fmt.Errorf("decode runtime Lua reply: %w", err)
	}
	if !reply.OK {
		code := strings.TrimSpace(reply.Code)
		if code == "" {
			code = strings.TrimSpace(reply.Message)
		}
		return reply, nil, &auction.RuntimeDecisionError{
			Code: code, EndsAtUnixMs: reply.EndsAtUnixMs, CurrentPrice: reply.CurrentPriceFen,
			MinIncrement: reply.MinIncrementFen, MinimumBid: reply.MinimumBidFen, LotVersion: reply.LotVersion,
		}
	}
	if strings.TrimSpace(reply.FactJSON) == "" || strings.TrimSpace(reply.EventID) == "" {
		return reply, nil, errors.New("runtime Lua success reply omitted its fact")
	}
	fact, err := eventcontract.DecodeRuntimeOutboxItem(reply.EventID + "\n" + reply.FactJSON)
	if err != nil {
		return reply, nil, fmt.Errorf("validate runtime Lua fact: %w", err)
	}
	if fact.GetLotId() != lotID || fact.GetEventId() != reply.EventID || fact.GetLotVersion() != reply.LotVersion || fact.GetOccurredAtUnixMs() != reply.OccurredAtUnixMs {
		return reply, nil, errors.New("runtime Lua reply identity does not match its fact")
	}
	waitStarted := time.Now()
	replicas, waitErr := connection.Wait(ctx, runtimeRequiredReplicas, runtimeReplicaWaitTimeout).Result()
	confirmed := waitErr == nil && replicas >= runtimeRequiredReplicas
	observability.RecordRuntimeReplicaWait(confirmed, time.Since(waitStarted))
	if !confirmed {
		slog.Warn("runtime command committed without requested replica acknowledgement",
			"lot_id", lotID, "event_id", fact.GetEventId(), "replicas", replicas, "error", waitErr,
		)
		markCtx, cancelMark := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
		markErr := s.redis.SAdd(markCtx, runtimePriorityReconcileKey(), lotID).Err()
		cancelMark()
		if markErr != nil {
			slog.Error("mark runtime lot for priority reconciliation failed", "lot_id", lotID, "error", markErr)
		}
	}
	return reply, fact, nil
}

func runtimeConfigFromLot(lot *v1.Lot) (auction.RuntimeConfigSnapshot, error) {
	if lot == nil || lot.GetRule() == nil || lot.GetRule().GetStartPrice() == nil || lot.GetRule().GetMinIncrement() == nil {
		return auction.RuntimeConfigSnapshot{}, errors.New("lot runtime configuration is incomplete")
	}
	rule := lot.GetRule()
	if rule.GetDurationSeconds() <= 0 || rule.GetAntiSnipeWindowSeconds() < 0 || rule.GetAntiSnipeExtendSeconds() < 0 {
		return auction.RuntimeConfigSnapshot{}, errors.New("lot runtime duration configuration is invalid")
	}
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
	return config, nil
}

func runtimeTraceID(traceID, eventID string) string {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return eventID
	}
	return traceID
}

func (s *Store) runtimeRoomIdentity(ctx context.Context, lotID string) (string, error) {
	roomID, err := s.redis.HGet(ctx, runtimeStateKey(lotID), "room_id").Result()
	if errors.Is(err, redis.Nil) {
		return "", &auction.RuntimeDecisionError{Code: auction.RuntimeCodeStateMissing}
	}
	if err != nil {
		return "", fmt.Errorf("read runtime room identity: %w", err)
	}
	if strings.TrimSpace(roomID) == "" {
		return "", errors.New("runtime room identity is empty")
	}
	return roomID, nil
}

func runtimePriorityReconcileKey() string {
	return "auction:runtime:reconcile:priority"
}
