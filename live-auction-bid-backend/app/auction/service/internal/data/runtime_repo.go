package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

const (
	runtimeRecentLimit = int64(20)
)

type runtimeBidJSON struct {
	ID              string `json:"id"`
	LotID           string `json:"lot_id"`
	UserID          string `json:"user_id"`
	Nickname        string `json:"nickname"`
	AvatarURL       string `json:"avatar_url"`
	Amount          int64  `json:"amount"`
	Currency        string `json:"currency"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
}

type runtimeRankMetaJSON struct {
	UserID      string `json:"user_id"`
	Nickname    string `json:"nickname"`
	AvatarURL   string `json:"avatar_url"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	BidAtUnixMs int64  `json:"bid_at_unix_ms"`
	BidID       string `json:"bid_id"`
}

func (s *Store) SnapshotRuntime(ctx context.Context, current *v1.Lot) (*v1.RoomSnapshot, error) {
	if current == nil {
		return nil, fmt.Errorf("%w: current lot is required", apperr.ErrInvalidArgument)
	}
	lot, err := s.loadRuntimeLot(ctx, current)
	if err != nil {
		return nil, err
	}
	ranking, err := s.RankingRuntime(ctx, current.Id, auction.RealtimeRankingLimit())
	if err != nil {
		return nil, err
	}
	recent, err := s.recentRuntimeBids(ctx, current.Id, runtimeRecentLimit)
	if err != nil {
		return nil, err
	}
	return &v1.RoomSnapshot{
		RoomId:           current.RoomId,
		CurrentLot:       lot,
		Ranking:          ranking,
		RecentBids:       recent,
		PlaybookStage:    lot.PlaybookStage,
		ServerTimeUnixMs: time.Now().UnixMilli(),
	}, nil
}

func (s *Store) ActiveRuntimeLotID(ctx context.Context, roomID string) (string, bool, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return "", false, fmt.Errorf("%w: room id is required", apperr.ErrInvalidArgument)
	}
	lotID, err := s.redis.Get(ctx, runtimeRoomActiveLotKey(roomID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read runtime active lot: %w", err)
	}
	lotID = strings.TrimSpace(lotID)
	return lotID, lotID != "", nil
}

func (s *Store) DisplayedRuntimeLotID(ctx context.Context, roomID string) (string, bool, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return "", false, fmt.Errorf("%w: room id is required", apperr.ErrInvalidArgument)
	}
	lotID, err := s.redis.Get(ctx, runtimeRoomDisplayLotKey(roomID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read runtime displayed lot: %w", err)
	}
	lotID = strings.TrimSpace(lotID)
	return lotID, lotID != "", nil
}

func (s *Store) RankingRuntime(ctx context.Context, lotID string, limit int64) ([]*v1.RankingItem, error) {
	if lotID == "" {
		return nil, fmt.Errorf("%w: lot id is required", apperr.ErrInvalidArgument)
	}
	stop := int64(-1)
	if limit > 0 {
		stop = limit - 1
	}
	rows, err := s.redis.ZRevRangeWithScores(ctx, runtimeRankingKey(lotID), 0, stop).Result()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []*v1.RankingItem{}, nil
	}
	userIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, fmt.Sprint(row.Member))
	}
	metaValues, err := s.redis.HMGet(ctx, runtimeRankMetaKey(lotID), userIDs...).Result()
	if err != nil {
		return nil, err
	}
	ranking := make([]*v1.RankingItem, 0, len(rows))
	for i, rawMeta := range metaValues {
		meta := runtimeRankMetaJSON{UserID: userIDs[i], Amount: int64(rows[i].Score)}
		if text, ok := rawMeta.(string); ok && text != "" {
			_ = json.Unmarshal([]byte(text), &meta)
		}
		if meta.AvatarURL == "" {
			meta.AvatarURL = userbiz.AvatarURLForUserID(meta.UserID)
		}
		ranking = append(ranking, &v1.RankingItem{
			Rank:        int32(i + 1),
			UserId:      meta.UserID,
			Nickname:    meta.Nickname,
			AvatarUrl:   meta.AvatarURL,
			Amount:      &v1.Money{Amount: meta.Amount, Currency: meta.Currency},
			BidAtUnixMs: meta.BidAtUnixMs,
		})
	}
	return ranking, nil
}

func (s *Store) loadRuntimeLot(ctx context.Context, base *v1.Lot) (*v1.Lot, error) {
	values, err := s.redis.HGetAll(ctx, runtimeStateKey(base.Id)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: lot runtime state is missing", apperr.ErrInvalidArgument)
	}
	return runtimeStateToLot(base, values), nil
}

func (s *Store) recentRuntimeBids(ctx context.Context, lotID string, limit int64) ([]*v1.Bid, error) {
	if limit <= 0 {
		limit = runtimeRecentLimit
	}
	rows, err := s.redis.LRange(ctx, runtimeRecentKey(lotID), 0, limit-1).Result()
	if err != nil {
		return nil, err
	}
	bids := make([]*v1.Bid, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		var payload runtimeBidJSON
		if err := json.Unmarshal([]byte(rows[i]), &payload); err != nil {
			return nil, err
		}
		bids = append(bids, runtimeJSONToBid(payload))
	}
	return bids, nil
}

func runtimeStateToLot(base *v1.Lot, values map[string]string) *v1.Lot {
	lot := proto.Clone(base).(*v1.Lot)
	lot.Id = firstNonEmpty(values["lot_id"], lot.Id)
	lot.MainAccountId = firstNonEmpty(values["main_account_id"], lot.MainAccountId)
	lot.RoomId = firstNonEmpty(values["room_id"], lot.RoomId)
	lot.Title = firstNonEmpty(values["title"], lot.Title)
	lot.ImageUrl = firstNonEmpty(values["image_url"], lot.ImageUrl)
	if value, exists := runtimeStateValue(values, "config_version"); exists {
		lot.ConfigVersion = parseInt64(value)
	}
	if value, exists := runtimeStateValue(values, "status"); exists {
		lot.Status = v1.LotStatus(parseInt32(value))
	}
	currency := firstNonEmpty(values["currency"], values["current_currency"], lot.GetCurrentPrice().GetCurrency(), lot.GetRule().GetStartPrice().GetCurrency())
	currentAmount, _ := runtimeStateInt64(values, "current_price_fen", "current_amount")
	lot.CurrentPrice = &v1.Money{Amount: currentAmount, Currency: currency}
	lot.LeadingUserId = values["leading_user_id"]
	lot.LeadingNickname = values["leading_nickname"]
	lot.StartedAtUnixMs = parseInt64(values["started_at_unix_ms"])
	lot.EndsAtUnixMs = parseInt64(values["ends_at_unix_ms"])
	lot.SettledAtUnixMs = parseInt64(values["settled_at_unix_ms"])
	lot.CancelReason = values["cancel_reason"]
	lot.CancelledAtUnixMs = parseInt64(values["cancelled_at_unix_ms"])
	lot.WinnerUserId = values["winner_user_id"]
	lot.WinnerNickname = values["winner_nickname"]
	finalAmount, _ := runtimeStateInt64(values, "final_price_fen", "final_amount")
	lot.FinalPrice = &v1.Money{Amount: finalAmount, Currency: firstNonEmpty(values["final_currency"], currency)}
	lot.Version = parseInt64(values["version"])
	if value, exists := runtimeStateValue(values, "playbook_stage"); exists {
		lot.PlaybookStage = v1.PlaybookStage(parseInt32(value))
	}
	lot.Stats = &v1.LotStats{
		ParticipantCount: parseInt64(values["participant_count"]),
		BidCount:         parseInt64(values["bid_count"]),
	}
	lot.DuelState = auction.MergeRuntimeDuelState(
		lot.GetDuelState(), lot.GetId(), lot.GetEndsAtUnixMs(),
		parseInt32(firstNonEmpty(values["extend_count"], values["duel_extend_count"])),
		parseInt32(values["max_extend_count"]),
		runtimeTerminalStatus(lot.GetStatus()),
	)
	rule := lot.GetRule()
	if rule == nil {
		rule = &v1.BidRule{}
	} else {
		rule = proto.Clone(rule).(*v1.BidRule)
	}
	if amount, exists := runtimeStateInt64(values, "start_price_fen"); exists {
		rule.StartPrice = &v1.Money{Amount: amount, Currency: currency}
	}
	if amount, exists := runtimeStateInt64(values, "min_increment_fen", "min_increment_amount"); exists {
		rule.MinIncrement = &v1.Money{Amount: amount, Currency: currency}
	}
	if value, exists := runtimeStateValue(values, "cap_price_fen"); exists {
		if strings.TrimSpace(value) == "" {
			rule.CapPrice = nil
		} else {
			rule.CapPrice = &v1.Money{Amount: parseInt64(value), Currency: currency}
		}
	}
	if durationMs, exists := runtimeStateInt64(values, "duration_ms"); exists {
		rule.DurationSeconds = int32(durationMs / 1000)
	}
	if windowMs, exists := runtimeStateInt64(values, "anti_snipe_window_ms"); exists {
		rule.AntiSnipeWindowSeconds = int32(windowMs / 1000)
	} else if seconds, exists := runtimeStateInt64(values, "anti_snipe_window_seconds"); exists {
		rule.AntiSnipeWindowSeconds = int32(seconds)
	}
	if extendMs, exists := runtimeStateInt64(values, "anti_snipe_extend_ms"); exists {
		rule.AntiSnipeExtendSeconds = int32(extendMs / 1000)
	} else if seconds, exists := runtimeStateInt64(values, "anti_snipe_extend_seconds"); exists {
		rule.AntiSnipeExtendSeconds = int32(seconds)
	}
	if value, exists := runtimeStateValue(values, "max_extend_count"); exists {
		rule.MaxExtendCount = parseInt32(value)
	}
	lot.Rule = rule
	return lot
}

func runtimeTerminalStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

func runtimeStateValue(values map[string]string, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			return value, true
		}
	}
	return "", false
}

func runtimeStateInt64(values map[string]string, keys ...string) (int64, bool) {
	value, exists := runtimeStateValue(values, keys...)
	if !exists {
		return 0, false
	}
	return parseInt64(value), true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func runtimeJSONToBid(payload runtimeBidJSON) *v1.Bid {
	avatarURL := payload.AvatarURL
	if avatarURL == "" {
		avatarURL = userbiz.AvatarURLForUserID(payload.UserID)
	}
	return &v1.Bid{
		Id:              payload.ID,
		LotId:           payload.LotID,
		UserId:          payload.UserID,
		Nickname:        payload.Nickname,
		AvatarUrl:       avatarURL,
		Amount:          &v1.Money{Amount: payload.Amount, Currency: payload.Currency},
		CreatedAtUnixMs: payload.CreatedAtUnixMs,
	}
}

func runtimeTag(lotID string) string {
	return "auction:lot:{" + lotID + "}"
}

func runtimeStateKey(lotID string) string {
	return runtimeTag(lotID) + ":state"
}

func runtimeRankingKey(lotID string) string {
	return runtimeTag(lotID) + ":ranking"
}

func runtimeRankMetaKey(lotID string) string {
	return runtimeTag(lotID) + ":rankmeta"
}

func runtimeParticipantsKey(lotID string) string {
	return runtimeTag(lotID) + ":participants"
}

func runtimeRecentKey(lotID string) string {
	return runtimeTag(lotID) + ":recent"
}

func (s *Store) runtimeOutboxShard(lotID string) int {
	count := s.outboxShards
	if count <= 0 {
		count = RuntimeOutboxShardCount
	}
	return runtimeOutboxShard(lotID, count)
}

func runtimeOutboxShard(lotID string, shardCount int) int {
	if shardCount <= 0 {
		shardCount = RuntimeOutboxShardCount
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(lotID))
	return int(h.Sum32() % uint32(shardCount))
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func parseInt32(value string) int32 {
	return int32(parseInt64(value))
}
