package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const obsoleteStreamAppendCommand = "X" + "ADD"

func TestRuntimeStartLotLuaFollowsAtomicScriptRules(t *testing.T) {
	assertRuntimeLuaPhases(t, runtimeStartLotLua)
	for _, required := range []string{
		"redis.call('TIME')",
		"redis.call('TYPE', expiring_key).ok",
		"redis.call('TYPE', outbox_key).ok",
		"redis.call('LPUSH', outbox_key, outbox_item)",
		"redis.call('ZADD', expiring_key, ends_at_unix_ms, lot_id)",
		"event_id .. '\\n' .. fact_payload",
		"9007199254740991",
	} {
		if !strings.Contains(runtimeStartLotLua, required) {
			t.Fatalf("start_lot.lua is missing %q", required)
		}
	}
	if strings.Contains(runtimeStartLotLua, obsoleteStreamAppendCommand) {
		t.Fatal("start_lot.lua contains an obsolete append command")
	}
}

func TestRuntimeCancelLotLuaFollowsAtomicScriptRules(t *testing.T) {
	assertRuntimeLuaPhases(t, runtimeCancelLotLuaV1)
	for _, required := range []string{
		"redis.call('TIME')",
		"redis.call('TYPE', expiring_key).ok",
		"redis.call('TYPE', outbox_key).ok",
		"redis.call('LPUSH', outbox_key, outbox_item)",
		"redis.call('ZREM', expiring_key, lot_id)",
		"redis.call('EXPIRE', state_key, terminal_ttl_seconds)",
	} {
		if !strings.Contains(runtimeCancelLotLuaV1, required) {
			t.Fatalf("cancel_lot.lua is missing %q", required)
		}
	}
	if strings.Contains(runtimeCancelLotLuaV1, obsoleteStreamAppendCommand) {
		t.Fatal("cancel_lot.lua contains an obsolete append command")
	}
}

func TestRuntimeCloseIfExpiredLuaFollowsAtomicScriptRules(t *testing.T) {
	assertRuntimeLuaPhases(t, runtimeCloseIfExpiredLuaV1)
	for _, required := range []string{
		"redis.call('TIME')",
		"redis.call('TYPE', expiring_key).ok",
		"redis.call('TYPE', outbox_key).ok",
		"redis.call('ZREM', expiring_key, lot_id)",
		"redis.call('LPUSH', outbox_key, outbox_item)",
		"return reject('NOT_EXPIRED', ends_at_unix_ms)",
	} {
		if !strings.Contains(runtimeCloseIfExpiredLuaV1, required) {
			t.Fatalf("close_if_expired.lua is missing %q", required)
		}
	}
	if strings.Contains(runtimeCloseIfExpiredLuaV1, obsoleteStreamAppendCommand) {
		t.Fatal("close_if_expired.lua contains an obsolete append command")
	}
}

func TestRuntimeSyncLotConfigLuaFollowsAtomicScriptRules(t *testing.T) {
	assertRuntimeLuaPhases(t, runtimeSyncLotConfigLuaV1)
	for _, required := range []string{
		"redis.call('TIME')",
		"redis.call('TYPE', outbox_key).ok",
		"redis.call('LPUSH', outbox_key, outbox_item)",
		"'config_version', next_config_version",
		"next_max_extend_count < extend_count",
		"next_cap_price_fen <= current_price_fen",
	} {
		if !strings.Contains(runtimeSyncLotConfigLuaV1, required) {
			t.Fatalf("sync_lot_config.lua is missing %q", required)
		}
	}
	if strings.Contains(runtimeSyncLotConfigLuaV1, obsoleteStreamAppendCommand) {
		t.Fatal("sync_lot_config.lua contains an obsolete append command")
	}
}

func TestRuntimePlaceBidLuaFollowsAtomicScriptRules(t *testing.T) {
	assertRuntimeLuaPhases(t, runtimePlaceBidLuaV1)
	for _, required := range []string{
		"redis.call('TIME')",
		"redis.call('HLEN', rankmeta_key)",
		"redis.call('LLEN', recent_key)",
		"redis.call('ZSCORE', expiring_key",
		"redis.call('HSET', idempotency_key, idempotency_field, response_payload)",
		"redis.call('LPUSH', outbox_key, outbox_item)",
	} {
		if !strings.Contains(runtimePlaceBidLuaV1, required) {
			t.Fatalf("place_bid.lua is missing %q", required)
		}
	}
	if strings.Contains(runtimePlaceBidLuaV1, obsoleteStreamAppendCommand) {
		t.Fatal("place_bid.lua contains an obsolete append command")
	}
}

func TestRuntimeStateToLotReadsCanonicalLuaHashFields(t *testing.T) {
	base := &v1.Lot{
		Id: "lot-1", RoomId: "room-1", Title: "投影前标题", Status: v1.LotStatus_LOT_STATUS_READY,
		CurrentPrice:        &v1.Money{Amount: 10_000, Currency: "CNY"},
		PresentationVersion: 5,
		DuelState:           &v1.DuelState{Active: true, UserAId: "buyer-1", UserBId: "buyer-2", StartedAtUnixMs: 123},
		Rule: &v1.BidRule{
			StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"},
		},
	}
	lot := runtimeStateToLot(base, map[string]string{
		"lot_id": "lot-1", "room_id": "room-1", "main_account_id": "main-1", "title": "Redis 权威标题",
		"image_url": "https://example.com/runtime.png", "config_version": "3", "currency": "CNY",
		"start_price_fen": "10000", "min_increment_fen": "500", "cap_price_fen": "20000", "duration_ms": "60000",
		"anti_snipe_window_ms": "10000", "anti_snipe_extend_ms": "15000", "max_extend_count": "4",
		"status": strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)), "version": "8", "current_price_fen": "12500",
		"leading_user_id": "buyer-1", "leading_nickname": "买家甲", "ends_at_unix_ms": "90000",
		"bid_count": "3", "participant_count": "2", "extend_count": "2",
	})

	if lot.GetStatus() != v1.LotStatus_LOT_STATUS_EXTENDED || lot.GetVersion() != 8 || lot.GetCurrentPrice().GetAmount() != 12_500 {
		t.Fatalf("runtime state did not replace projected lot state: %+v", lot)
	}
	if lot.GetTitle() != "Redis 权威标题" || lot.GetConfigVersion() != 3 || lot.GetLeadingUserId() != "buyer-1" {
		t.Fatalf("runtime identity/config fields missing: %+v", lot)
	}
	if lot.GetRule().GetMinIncrement().GetAmount() != 500 || lot.GetRule().GetCapPrice().GetAmount() != 20_000 || lot.GetRule().GetDurationSeconds() != 60 || lot.GetRule().GetAntiSnipeExtendSeconds() != 15 {
		t.Fatalf("runtime rule fields missing: %+v", lot.GetRule())
	}
	if lot.GetPresentationVersion() != 5 || !lot.GetDuelState().GetActive() || lot.GetDuelState().GetUserBId() != "buyer-2" ||
		lot.GetDuelState().GetEndsAtUnixMs() != 90_000 || lot.GetDuelState().GetExtendCount() != 2 || lot.GetDuelState().GetMaxExtendCount() != 4 {
		t.Fatalf("runtime state did not preserve presentation duel: %+v", lot.GetDuelState())
	}
}

func TestRuntimeStartLotLuaMatchesGoModel(t *testing.T) {
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if redisAddress == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	lotID := "lua-start-" + eventID
	roomID := "lua-room-" + eventID
	shard := int(time.Now().UnixNano() & 0x3fffffff)
	keys := []string{
		runtimeStateKey(lotID),
		runtimeRankingKey(lotID),
		runtimeRankMetaKey(lotID),
		runtimeParticipantsKey(lotID),
		runtimeRecentKey(lotID),
		runtimeIdempotencyHashKey(lotID),
		runtimeExpiringKey(),
		runtimeOutboxPendingKey(shard),
		runtimeRoomActiveLotKey(roomID),
		runtimeFrozenLotKey(lotID),
		runtimeRoomDisplayLotKey(roomID),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, keys[0], keys[1], keys[2], keys[3], keys[4], keys[5], keys[7], keys[8], keys[9], keys[10]).Err()
		_ = client.ZRem(cleanupCtx, keys[6], lotID).Err()
	})

	config := auction.RuntimeConfigSnapshot{
		LotID:             lotID,
		RoomID:            roomID,
		MainAccountID:     "main-1",
		Title:             "Lua 对拍拍品",
		ImageURL:          "https://example.com/lua-start.png",
		ConfigVersion:     3,
		Currency:          "CNY",
		StartPriceFen:     10_000,
		MinIncrementFen:   100,
		DurationMs:        60_000,
		AntiSnipeWindowMs: 10_000,
		AntiSnipeExtendMs: 30_000,
		MaxExtendCount:    3,
	}
	args := []any{
		eventID,
		"trace-start",
		config.LotID,
		config.RoomID,
		config.MainAccountID,
		config.Title,
		config.ImageURL,
		strconv.FormatInt(config.ConfigVersion, 10),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_QUEUED)),
		"4",
		config.Currency,
		strconv.FormatInt(config.StartPriceFen, 10),
		strconv.FormatInt(config.MinIncrementFen, 10),
		"",
		strconv.FormatInt(config.DurationMs, 10),
		strconv.FormatInt(config.AntiSnipeWindowMs, 10),
		strconv.FormatInt(config.AntiSnipeExtendMs, 10),
		strconv.Itoa(int(config.MaxExtendCount)),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)),
		strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
	}
	invalidArgs := append([]any(nil), args...)
	invalidArgs[0] = "not-a-uuid-v7"
	rejectedRaw, err := runtimeStartLotScriptV1.Run(ctx, client, keys, invalidArgs...).Text()
	if err != nil {
		t.Fatalf("run rejected start_lot.lua: %v", err)
	}
	var rejectedResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(rejectedRaw), &rejectedResponse); err != nil {
		t.Fatalf("decode rejected start response: %v", err)
	}
	if rejectedResponse.OK || rejectedResponse.Code != auction.RuntimeCodeInvalidArgument {
		t.Fatalf("invalid event id response=%s", rejectedRaw)
	}
	if exists, err := client.Exists(ctx, keys[0], keys[7], keys[8], keys[10]).Result(); err != nil || exists != 0 {
		t.Fatalf("rejected start left state/outbox/room keys: exists=%d error=%v", exists, err)
	}
	if _, err := client.ZScore(ctx, keys[6], lotID).Result(); err != redis.Nil {
		t.Fatalf("rejected start left expiring candidate: %v", err)
	}

	raw, err := runtimeStartLotScriptV1.Run(ctx, client, keys, args...).Text()
	if err != nil {
		t.Fatalf("run start_lot.lua: %v", err)
	}
	var response struct {
		OK               bool   `json:"ok"`
		Code             string `json:"code"`
		LotVersion       int64  `json:"lot_version"`
		OccurredAtUnixMs int64  `json:"occurred_at_unix_ms"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if !response.OK {
		t.Fatalf("start rejected: code=%s response=%s", response.Code, raw)
	}
	if displayed, err := client.Get(ctx, keys[10]).Result(); err != nil || displayed != lotID {
		t.Fatalf("started lot display pointer=%q error=%v", displayed, err)
	}

	item, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatalf("read outbox item: %v", err)
	}
	actualFact, err := eventcontract.DecodeRuntimeOutboxItem(item)
	if err != nil {
		t.Fatalf("decode outbox item: %v", err)
	}
	expected, err := auction.DecideRuntimeStartLot(auction.RuntimeStartLotCommand{
		Meta:               auction.RuntimeCommandMeta{EventID: eventID, TraceID: "trace-start"},
		Config:             config,
		PreviousStatus:     v1.LotStatus_LOT_STATUS_QUEUED,
		PreviousLotVersion: 4,
		NowUnixMs:          actualFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go start model: %v", err)
	}
	equal, err := eventcontract.RuntimeFactBinaryEqual(expected.Fact, actualFact)
	if err != nil {
		t.Fatalf("compare runtime facts: %v", err)
	}
	if !equal {
		t.Fatalf("Lua fact differs from Go model\nGo:  %v\nLua: %v", expected.Fact, actualFact)
	}
	assertRuntimeStateIdentity(t, ctx, client, keys[0], actualFact)

	state, err := client.HGetAll(ctx, keys[0]).Result()
	if err != nil {
		t.Fatalf("read runtime state: %v", err)
	}
	if state["version"] != "5" || state["status"] != strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)) || state["ends_at_unix_ms"] != strconv.FormatInt(actualFact.GetStateAfter().GetEndsAtUnixMs(), 10) {
		t.Fatalf("stored state mismatch: %+v", state)
	}
	if ttl, err := client.TTL(ctx, keys[0]).Result(); err != nil || ttl != -1 {
		t.Fatalf("active runtime state TTL=%s error=%v; active state must not expire", ttl, err)
	}
	if score, err := client.ZScore(ctx, keys[6], lotID).Result(); err != nil || int64(score) != actualFact.GetStateAfter().GetEndsAtUnixMs() {
		t.Fatalf("expiring score=%v error=%v", score, err)
	}
	if activeLot, err := client.Get(ctx, keys[8]).Result(); err != nil || activeLot != lotID {
		t.Fatalf("active lot=%q error=%v", activeLot, err)
	}

	nextConfig := config
	nextConfig.ConfigVersion++
	nextConfig.MinIncrementFen = 250
	nextConfig.DurationMs = 90_000
	nextConfig.AntiSnipeWindowMs = 60_000
	nextConfig.AntiSnipeExtendMs = 45_000
	nextConfig.MaxExtendCount = 4
	syncEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	syncKeys := []string{keys[0], keys[1], keys[2], keys[3], keys[7], keys[9]}
	syncArgs := []any{
		syncEventID, "trace-sync", strconv.FormatInt(config.ConfigVersion, 10), strconv.FormatInt(nextConfig.ConfigVersion, 10),
		nextConfig.LotID, nextConfig.RoomID, nextConfig.MainAccountID, nextConfig.Title, nextConfig.ImageURL,
		nextConfig.Currency, strconv.FormatInt(nextConfig.StartPriceFen, 10), strconv.FormatInt(nextConfig.MinIncrementFen, 10), "",
		strconv.FormatInt(nextConfig.DurationMs, 10), strconv.FormatInt(nextConfig.AntiSnipeWindowMs, 10),
		strconv.FormatInt(nextConfig.AntiSnipeExtendMs, 10), strconv.Itoa(int(nextConfig.MaxExtendCount)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20",
	}
	if err := client.Set(ctx, keys[9], "STATE_DIVERGED", 0).Err(); err != nil {
		t.Fatalf("set runtime lot fence: %v", err)
	}
	frozenRaw, err := runtimeSyncLotConfigScriptV1.Run(ctx, client, syncKeys, syncArgs...).Text()
	if err != nil {
		t.Fatalf("run fenced sync_lot_config.lua: %v", err)
	}
	var frozenResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(frozenRaw), &frozenResponse); err != nil || frozenResponse.OK || frozenResponse.Code != auction.RuntimeCodeLotFrozen {
		t.Fatalf("frozen lot response=%s error=%v", frozenRaw, err)
	}
	if err := client.Del(ctx, keys[9]).Err(); err != nil {
		t.Fatalf("clear runtime lot fence: %v", err)
	}
	conflictArgs := append([]any(nil), syncArgs...)
	conflictArgs[2] = "999"
	conflictRaw, err := runtimeSyncLotConfigScriptV1.Run(ctx, client, syncKeys, conflictArgs...).Text()
	if err != nil {
		t.Fatalf("run conflicting sync_lot_config.lua: %v", err)
	}
	var conflictResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(conflictRaw), &conflictResponse); err != nil || conflictResponse.OK || conflictResponse.Code != auction.RuntimeCodeConfigVersionConflict {
		t.Fatalf("config conflict response=%s error=%v", conflictRaw, err)
	}
	if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != expected.State.Version {
		t.Fatalf("config conflict changed lot version=%d error=%v", version, err)
	}
	if outboxLength, err := client.LLen(ctx, keys[7]).Result(); err != nil || outboxLength != 1 {
		t.Fatalf("config conflict changed outbox length=%d error=%v", outboxLength, err)
	}

	syncRaw, err := runtimeSyncLotConfigScriptV1.Run(ctx, client, syncKeys, syncArgs...).Text()
	if err != nil {
		t.Fatalf("run sync_lot_config.lua: %v", err)
	}
	var syncResponse struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(syncRaw), &syncResponse); err != nil || !syncResponse.OK {
		t.Fatalf("sync config response=%s error=%v", syncRaw, err)
	}
	syncItem, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatalf("read sync outbox item: %v", err)
	}
	actualSyncFact, err := eventcontract.DecodeRuntimeOutboxItem(syncItem)
	if err != nil {
		t.Fatalf("decode sync outbox item: %v", err)
	}
	expectedSync, err := auction.DecideRuntimeSyncLotConfig(expected.State, auction.RuntimeSyncLotConfigCommand{
		Meta: auction.RuntimeCommandMeta{EventID: syncEventID, TraceID: "trace-sync"}, ExpectedConfigVersion: config.ConfigVersion,
		NextConfig: nextConfig, NowUnixMs: actualSyncFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go sync config model: %v", err)
	}
	equal, err = eventcontract.RuntimeFactBinaryEqual(expectedSync.Fact, actualSyncFact)
	if err != nil {
		t.Fatalf("compare sync config facts: %v", err)
	}
	if !equal {
		t.Fatalf("sync Lua fact differs from Go model\nGo:  %v\nLua: %v", expectedSync.Fact, actualSyncFact)
	}
	assertRuntimeStateIdentity(t, ctx, client, keys[0], actualSyncFact)
	unchangedFields, err := client.HMGet(ctx, keys[0], "status", "current_price_fen", "ends_at_unix_ms", "leading_user_id").Result()
	if err != nil {
		t.Fatalf("read bidding state after config sync: %v", err)
	}
	if fmt.Sprint(unchangedFields[0]) != strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)) || fmt.Sprint(unchangedFields[1]) != strconv.FormatInt(config.StartPriceFen, 10) || fmt.Sprint(unchangedFields[2]) != strconv.FormatInt(expected.State.EndsAtUnixMs, 10) || fmt.Sprint(unchangedFields[3]) != "" {
		t.Fatalf("sync config changed bidding state: %+v", unchangedFields)
	}

	placeCurrent := expectedSync.State
	placeCurrent.EndsAtUnixMs = time.Now().Add(30 * time.Second).UnixMilli()
	if err := client.HSet(ctx, keys[0], "ends_at_unix_ms", placeCurrent.EndsAtUnixMs).Err(); err != nil {
		t.Fatalf("inject anti-snipe deadline: %v", err)
	}
	if err := client.ZAdd(ctx, keys[6], redis.Z{Score: float64(placeCurrent.EndsAtUnixMs), Member: lotID}).Err(); err != nil {
		t.Fatalf("inject anti-snipe candidate: %v", err)
	}
	placeEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	placeBusinessKey := "idem-place-1"
	placeIdempotencyField := runtimeIdempotencyField("bidder-1", placeBusinessKey)
	placeArgs := []any{
		placeEventID, "trace-place", "bid-1", "bidder-1", "出价用户", auction.MaskBuyerNickname("出价用户"),
		"https://example.com/bidder.png", "10250", "CNY", placeIdempotencyField, placeBusinessKey, "order-unused",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
		"20", "20", strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), "1000",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
	}
	lowArgs := append([]any(nil), placeArgs...)
	lowArgs[0], err = eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	lowArgs[2] = "bid-low"
	lowArgs[7] = "10000"
	lowRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, lowArgs...).Text()
	if err != nil {
		t.Fatalf("run rejected place_bid.lua: %v", err)
	}
	var lowResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(lowRaw), &lowResponse); err != nil || lowResponse.OK || lowResponse.Code != auction.RuntimeCodeBidTooLow {
		t.Fatalf("low bid response=%s error=%v", lowRaw, err)
	}
	if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != expectedSync.State.Version {
		t.Fatalf("rejected bid changed version=%d error=%v", version, err)
	}
	if outboxLength, err := client.LLen(ctx, keys[7]).Result(); err != nil || outboxLength != 2 {
		t.Fatalf("rejected bid changed outbox length=%d error=%v", outboxLength, err)
	}

	placeRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, placeArgs...).Text()
	if err != nil {
		t.Fatalf("run place_bid.lua: %v", err)
	}
	var placeResponse struct {
		OK       bool `json:"ok"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal([]byte(placeRaw), &placeResponse); err != nil || !placeResponse.OK || placeResponse.Replayed {
		t.Fatalf("place bid response=%s error=%v", placeRaw, err)
	}
	placeItem, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatalf("read place outbox item: %v", err)
	}
	actualPlaceFact, err := eventcontract.DecodeRuntimeOutboxItem(placeItem)
	if err != nil {
		t.Fatalf("decode place outbox item: %v", err)
	}
	expectedPlace, err := auction.DecideRuntimePlaceBid(placeCurrent, auction.RuntimePlaceBidCommand{
		Meta:  auction.RuntimeCommandMeta{EventID: placeEventID, TraceID: "trace-place", IdempotencyKey: placeBusinessKey},
		BidID: "bid-1", UserID: "bidder-1", Nickname: "出价用户", AvatarURL: "https://example.com/bidder.png",
		AmountFen: 10_250, Currency: "CNY", OrderID: "order-unused", RankingLimit: 20,
		NowUnixMs: actualPlaceFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go place model: %v", err)
	}
	equal, err = eventcontract.RuntimeFactBinaryEqual(expectedPlace.Fact, actualPlaceFact)
	if err != nil {
		t.Fatalf("compare place facts: %v", err)
	}
	if !equal {
		t.Fatalf("place Lua fact differs from Go model\nGo:  %v\nLua: %v", expectedPlace.Fact, actualPlaceFact)
	}
	assertRuntimeStateIdentity(t, ctx, client, keys[0], actualPlaceFact)
	if score, err := client.ZScore(ctx, keys[6], lotID).Result(); err != nil || int64(score) != expectedPlace.State.EndsAtUnixMs {
		t.Fatalf("anti-snipe expiring score=%v want=%d error=%v", score, expectedPlace.State.EndsAtUnixMs, err)
	}
	if count, err := client.HLen(ctx, keys[5]).Result(); err != nil || count != 1 {
		t.Fatalf("idempotency hash count=%d error=%v", count, err)
	}

	replayArgs := append([]any(nil), placeArgs...)
	replayArgs[0], err = eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	replayArgs[2] = "bid-retry-must-not-win"
	replayArgs[7] = "999999"
	replayRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, replayArgs...).Text()
	if err != nil {
		t.Fatalf("run idempotent bid replay: %v", err)
	}
	var replayResponse struct {
		OK       bool `json:"ok"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal([]byte(replayRaw), &replayResponse); err != nil || !replayResponse.OK || !replayResponse.Replayed {
		t.Fatalf("bid replay response=%s error=%v", replayRaw, err)
	}
	if outboxLength, err := client.LLen(ctx, keys[7]).Result(); err != nil || outboxLength != 3 {
		t.Fatalf("bid replay duplicated outbox: length=%d error=%v", outboxLength, err)
	}
	if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != expectedPlace.State.Version {
		t.Fatalf("bid replay changed version=%d error=%v", version, err)
	}

	if err := client.HSet(ctx, keys[2], "bidder-1", "{malformed-json").Err(); err != nil {
		t.Fatalf("inject malformed rank metadata: %v", err)
	}
	corruptArgs := append([]any(nil), placeArgs...)
	corruptArgs[0], err = eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	corruptArgs[2] = "bid-after-corruption"
	corruptArgs[3] = "bidder-2"
	corruptArgs[4] = "第二用户"
	corruptArgs[5] = auction.MaskBuyerNickname("第二用户")
	corruptArgs[7] = "10500"
	corruptArgs[9] = runtimeIdempotencyField("bidder-2", "idem-corrupt")
	corruptArgs[10] = "idem-corrupt"
	corruptRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, corruptArgs...).Text()
	if err != nil {
		t.Fatalf("run bid with corrupt ranking: %v", err)
	}
	var corruptResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(corruptRaw), &corruptResponse); err != nil || corruptResponse.OK || corruptResponse.Code != "RUNTIME_STATE_CORRUPT" {
		t.Fatalf("corrupt ranking response=%s error=%v", corruptRaw, err)
	}
	if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != expectedPlace.State.Version {
		t.Fatalf("corrupt ranking changed version=%d error=%v", version, err)
	}
	if price, err := client.HGet(ctx, keys[0], "current_price_fen").Int64(); err != nil || price != expectedPlace.State.CurrentPriceFen {
		t.Fatalf("corrupt ranking changed price=%d error=%v", price, err)
	}
	if outboxLength, err := client.LLen(ctx, keys[7]).Result(); err != nil || outboxLength != 3 {
		t.Fatalf("corrupt ranking changed outbox length=%d error=%v", outboxLength, err)
	}
	repairedMeta, err := json.Marshal(map[string]any{
		"user_id": "bidder-1", "nickname": "出价用户", "masked_nickname": auction.MaskBuyerNickname("出价用户"),
		"avatar_url": "https://example.com/bidder.png", "amount": int64(10_250), "amount_fen": int64(10_250),
		"currency": "CNY", "bid_at_unix_ms": actualPlaceFact.GetOccurredAtUnixMs(), "bid_id": "bid-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, keys[2], "bidder-1", repairedMeta).Err(); err != nil {
		t.Fatalf("repair rank metadata: %v", err)
	}

	cancelEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	cancelRaw, err := runtimeCancelLotScriptV1.Run(ctx, client, keys,
		cancelEventID,
		"trace-cancel",
		"  operator cancelled  ",
		"operator-1",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)),
		strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
		strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
		"20",
	).Text()
	if err != nil {
		t.Fatalf("run cancel_lot.lua: %v", err)
	}
	var cancelResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(cancelRaw), &cancelResponse); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if !cancelResponse.OK {
		t.Fatalf("cancel rejected: code=%s response=%s", cancelResponse.Code, cancelRaw)
	}
	cancelItem, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatalf("read cancel outbox item: %v", err)
	}
	actualCancelFact, err := eventcontract.DecodeRuntimeOutboxItem(cancelItem)
	if err != nil {
		t.Fatalf("decode cancel outbox item: %v", err)
	}
	expectedCancel, err := auction.DecideRuntimeCancelLot(expectedPlace.State, auction.RuntimeCancelLotCommand{
		Meta:       auction.RuntimeCommandMeta{EventID: cancelEventID, TraceID: "trace-cancel"},
		Reason:     "  operator cancelled  ",
		OperatorID: "operator-1",
		NowUnixMs:  actualCancelFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go cancel model: %v", err)
	}
	equal, err = eventcontract.RuntimeFactBinaryEqual(expectedCancel.Fact, actualCancelFact)
	if err != nil {
		t.Fatalf("compare cancel runtime facts: %v", err)
	}
	if !equal {
		t.Fatalf("cancel Lua fact differs from Go model\nGo:  %v\nLua: %v", expectedCancel.Fact, actualCancelFact)
	}
	assertRuntimeStateIdentity(t, ctx, client, keys[0], actualCancelFact)
	if status, err := client.HGet(ctx, keys[0], "status").Int64(); err != nil || status != int64(v1.LotStatus_LOT_STATUS_CANCELLED) {
		t.Fatalf("cancelled state status=%d error=%v", status, err)
	}
	if ttl, err := client.TTL(ctx, keys[0]).Result(); err != nil || ttl <= runtimeTerminalRetention-time.Minute || ttl > runtimeTerminalRetention {
		t.Fatalf("terminal runtime state TTL=%s error=%v", ttl, err)
	}
	if _, err := client.ZScore(ctx, keys[6], lotID).Result(); err != redis.Nil {
		t.Fatalf("cancelled lot remains in expiring zset: %v", err)
	}
	if _, err := client.Get(ctx, keys[8]).Result(); err != redis.Nil {
		t.Fatalf("cancelled lot retained room active key: %v", err)
	}
	if displayed, err := client.Get(ctx, keys[10]).Result(); err != nil || displayed != lotID {
		t.Fatalf("cancelled lot display pointer=%q error=%v", displayed, err)
	}
	afterCancelArgs := append([]any(nil), placeArgs...)
	afterCancelArgs[0], err = eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	afterCancelArgs[2] = "bid-after-cancel"
	afterCancelArgs[3] = "bidder-2"
	afterCancelArgs[4] = "取消后买家"
	afterCancelArgs[5] = auction.MaskBuyerNickname("取消后买家")
	afterCancelArgs[9] = runtimeIdempotencyField("bidder-2", "after-cancel")
	afterCancelArgs[10] = "after-cancel"
	afterCancelRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, afterCancelArgs...).Text()
	if err != nil {
		t.Fatalf("run bid after cancel: %v", err)
	}
	var afterCancelResponse struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(afterCancelRaw), &afterCancelResponse); err != nil || afterCancelResponse.OK || afterCancelResponse.Code != auction.RuntimeCodeLotCancelled {
		t.Fatalf("bid after cancel response=%s error=%v", afterCancelRaw, err)
	}
	if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != expectedCancel.State.Version {
		t.Fatalf("bid after cancel changed version=%d error=%v", version, err)
	}
}

func TestRuntimeCloseIfExpiredLuaRechecksDeadlineAndMatchesGoModel(t *testing.T) {
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if redisAddress == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	lotID := "lua-close-" + eventID
	roomID := "lua-close-room-" + eventID
	shard := int(time.Now().UnixNano() & 0x3fffffff)
	keys := []string{
		runtimeStateKey(lotID), runtimeRankingKey(lotID), runtimeRankMetaKey(lotID),
		runtimeParticipantsKey(lotID), runtimeRecentKey(lotID), runtimeIdempotencyHashKey(lotID),
		runtimeExpiringKey(), runtimeOutboxPendingKey(shard), runtimeRoomActiveLotKey(roomID), runtimeFrozenLotKey(lotID), runtimeRoomDisplayLotKey(roomID),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, keys[0], keys[1], keys[2], keys[3], keys[4], keys[5], keys[7], keys[8], keys[9], keys[10]).Err()
		_ = client.ZRem(cleanupCtx, keys[6], lotID).Err()
	})

	config := auction.RuntimeConfigSnapshot{
		LotID: lotID, RoomID: roomID, MainAccountID: "main-close", Title: "延时落槌测试",
		ImageURL: "https://example.com/close.png", ConfigVersion: 2, Currency: "CNY",
		StartPriceFen: 10_000, MinIncrementFen: 100, DurationMs: 60_000,
		AntiSnipeWindowMs: 10_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
	}
	futureEndsAt := time.Now().Add(30 * time.Second).UnixMilli()
	state := auction.RuntimeState{
		Config: config, Status: v1.LotStatus_LOT_STATUS_EXTENDED, Version: 7,
		CurrentPriceFen: 12_000, LeadingUserID: "user-1", LeadingNickname: "甲用户",
		StartedAtUnixMs: futureEndsAt - 60_000, EndsAtUnixMs: futureEndsAt,
		BidCount: 4, ParticipantIDs: map[string]struct{}{"user-1": {}}, ExtendCount: 1,
		TopRanking: []auction.RuntimeRankingEntry{{
			UserID: "user-1", MaskedNickname: "甲***", AmountFen: 12_000, BidAtUnixMs: futureEndsAt - 1_000,
		}},
	}
	stateValues := map[string]any{
		"lot_id": lotID, "room_id": roomID, "main_account_id": config.MainAccountID,
		"title": config.Title, "image_url": config.ImageURL, "config_version": config.ConfigVersion,
		"currency": config.Currency, "start_price_fen": config.StartPriceFen,
		"min_increment_fen": config.MinIncrementFen, "cap_price_fen": "",
		"duration_ms": config.DurationMs, "anti_snipe_window_ms": config.AntiSnipeWindowMs,
		"anti_snipe_extend_ms": config.AntiSnipeExtendMs, "max_extend_count": config.MaxExtendCount,
		"status": int32(state.Status), "version": state.Version, "current_price_fen": state.CurrentPriceFen,
		"leading_user_id": state.LeadingUserID, "leading_nickname": state.LeadingNickname,
		"winner_user_id": "", "winner_nickname": "", "final_price_fen": 0,
		"started_at_unix_ms": state.StartedAtUnixMs, "ends_at_unix_ms": state.EndsAtUnixMs,
		"settled_at_unix_ms": 0, "cancelled_at_unix_ms": 0, "cancel_reason": "",
		"bid_count": state.BidCount, "participant_count": 1, "extend_count": state.ExtendCount,
		"order_id": "",
	}
	metaPayload, err := json.Marshal(map[string]any{
		"masked_nickname": "甲***", "avatar_url": "", "bid_at_unix_ms": state.TopRanking[0].BidAtUnixMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	pipe := client.TxPipeline()
	pipe.HSet(ctx, keys[0], stateValues)
	pipe.ZAdd(ctx, keys[1], redis.Z{Score: 12_000, Member: "user-1"})
	pipe.HSet(ctx, keys[2], "user-1", metaPayload)
	pipe.SAdd(ctx, keys[3], "user-1")
	pipe.ZAdd(ctx, keys[6], redis.Z{Score: float64(futureEndsAt), Member: lotID})
	pipe.Set(ctx, keys[8], lotID, 0)
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("seed close runtime: %v", err)
	}

	closeArgs := func(closeEventID string) []any {
		return []any{
			closeEventID, "trace-close", "order-close", strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)),
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
			strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)),
			strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
			strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20", auction.RuntimeExpiredNoBidReason,
		}
	}
	notExpiredRaw, err := runtimeCloseIfExpiredScriptV1.Run(ctx, client, keys, closeArgs(eventID)...).Text()
	if err != nil {
		t.Fatalf("run not-expired close: %v", err)
	}
	var notExpired struct {
		OK           bool   `json:"ok"`
		Code         string `json:"code"`
		EndsAtUnixMs int64  `json:"ends_at_unix_ms"`
	}
	if err := json.Unmarshal([]byte(notExpiredRaw), &notExpired); err != nil {
		t.Fatalf("decode not-expired close: %v", err)
	}
	if notExpired.OK || notExpired.Code != auction.RuntimeCodeNotExpired || notExpired.EndsAtUnixMs != futureEndsAt {
		t.Fatalf("not-expired response=%s", notExpiredRaw)
	}
	if outboxLength, err := client.LLen(ctx, keys[7]).Result(); err != nil || outboxLength != 0 {
		t.Fatalf("not-expired close wrote outbox: length=%d error=%v", outboxLength, err)
	}
	if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != state.Version {
		t.Fatalf("not-expired close changed state version=%d error=%v", version, err)
	}
	if score, err := client.ZScore(ctx, keys[6], lotID).Result(); err != nil || int64(score) != futureEndsAt {
		t.Fatalf("not-expired candidate was not rescheduled: score=%v error=%v", score, err)
	}

	state.EndsAtUnixMs = 1
	if err := client.HSet(ctx, keys[0], "ends_at_unix_ms", state.EndsAtUnixMs).Err(); err != nil {
		t.Fatalf("inject expired deadline: %v", err)
	}
	closeEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	closedRaw, err := runtimeCloseIfExpiredScriptV1.Run(ctx, client, keys, closeArgs(closeEventID)...).Text()
	if err != nil {
		t.Fatalf("run expired close: %v", err)
	}
	var closedResponse struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(closedRaw), &closedResponse); err != nil || !closedResponse.OK {
		t.Fatalf("expired close response=%s error=%v", closedRaw, err)
	}
	item, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatalf("read close outbox: %v", err)
	}
	actualFact, err := eventcontract.DecodeRuntimeOutboxItem(item)
	if err != nil {
		t.Fatalf("decode close outbox: %v", err)
	}
	expected, err := auction.DecideRuntimeCloseIfExpired(state, auction.RuntimeCloseIfExpiredCommand{
		Meta:    auction.RuntimeCommandMeta{EventID: closeEventID, TraceID: "trace-close"},
		OrderID: "order-close", NowUnixMs: actualFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go close model: %v", err)
	}
	equal, err := eventcontract.RuntimeFactBinaryEqual(expected.Fact, actualFact)
	if err != nil {
		t.Fatalf("compare close facts: %v", err)
	}
	if !equal {
		t.Fatalf("close Lua fact differs from Go model\nGo:  %v\nLua: %v", expected.Fact, actualFact)
	}
	assertRuntimeStateIdentity(t, ctx, client, keys[0], actualFact)
	if _, err := client.ZScore(ctx, keys[6], lotID).Result(); err != redis.Nil {
		t.Fatalf("closed lot remains in expiring zset: %v", err)
	}
	if _, err := client.Get(ctx, keys[8]).Result(); err != redis.Nil {
		t.Fatalf("closed lot retained room active key: %v", err)
	}
	if displayed, err := client.Get(ctx, keys[10]).Result(); err != nil || displayed != lotID {
		t.Fatalf("closed lot display pointer=%q error=%v", displayed, err)
	}
}

func TestRuntimePlaceBidLuaAtCapSettlesExactlyOnce(t *testing.T) {
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if redisAddress == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	startEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	lotID := "lua-cap-" + startEventID
	roomID := "lua-cap-room-" + startEventID
	shard := int(time.Now().UnixNano() & 0x3fffffff)
	keys := []string{
		runtimeStateKey(lotID), runtimeRankingKey(lotID), runtimeRankMetaKey(lotID),
		runtimeParticipantsKey(lotID), runtimeRecentKey(lotID), runtimeIdempotencyHashKey(lotID),
		runtimeExpiringKey(), runtimeOutboxPendingKey(shard), runtimeRoomActiveLotKey(roomID), runtimeFrozenLotKey(lotID), runtimeRoomDisplayLotKey(roomID),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, keys[0], keys[1], keys[2], keys[3], keys[4], keys[5], keys[7], keys[8], keys[9], keys[10]).Err()
		_ = client.ZRem(cleanupCtx, keys[6], lotID).Err()
	})

	capPrice := int64(10_500)
	config := auction.RuntimeConfigSnapshot{
		LotID: lotID, RoomID: roomID, MainAccountID: "main-cap", Title: "封顶成交测试",
		ImageURL: "https://example.com/cap.png", ConfigVersion: 1, Currency: "CNY",
		StartPriceFen: 10_000, MinIncrementFen: 100, CapPriceFen: &capPrice, DurationMs: 60_000,
		AntiSnipeWindowMs: 10_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
	}
	startArgs := []any{
		startEventID, "trace-cap-start", lotID, roomID, config.MainAccountID, config.Title, config.ImageURL,
		"1", strconv.Itoa(int(v1.LotStatus_LOT_STATUS_DRAFT)), "0", "CNY", "10000", "100",
		strconv.FormatInt(capPrice, 10), "60000", "10000", "30000", "3",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
	}
	if raw, err := runtimeStartLotScriptV1.Run(ctx, client, keys, startArgs...).Text(); err != nil {
		t.Fatalf("start capped lot: %v", err)
	} else {
		var response struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal([]byte(raw), &response); err != nil || !response.OK {
			t.Fatalf("start capped lot response=%s error=%v", raw, err)
		}
	}
	startItem, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatal(err)
	}
	startFact, err := eventcontract.DecodeRuntimeOutboxItem(startItem)
	if err != nil {
		t.Fatal(err)
	}
	startDecision, err := auction.DecideRuntimeStartLot(auction.RuntimeStartLotCommand{
		Meta: auction.RuntimeCommandMeta{EventID: startEventID, TraceID: "trace-cap-start"}, Config: config,
		PreviousStatus: v1.LotStatus_LOT_STATUS_DRAFT, PreviousLotVersion: 0, NowUnixMs: startFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatal(err)
	}

	placeEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	businessKey := "idem-cap"
	placeArgs := []any{
		placeEventID, "trace-cap-place", "bid-cap", "buyer-cap", "封顶买家", auction.MaskBuyerNickname("封顶买家"), "",
		strconv.FormatInt(capPrice, 10), "CNY", runtimeIdempotencyField("buyer-cap", businessKey), businessKey, "order-cap",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
		"20", "20", strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), "1000",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
	}
	placeRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, placeArgs...).Text()
	if err != nil {
		t.Fatalf("place cap bid: %v", err)
	}
	var placeResponse struct {
		OK       bool `json:"ok"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal([]byte(placeRaw), &placeResponse); err != nil || !placeResponse.OK || placeResponse.Replayed {
		t.Fatalf("cap bid response=%s error=%v", placeRaw, err)
	}
	placeItem, err := client.LIndex(ctx, keys[7], 0).Result()
	if err != nil {
		t.Fatal(err)
	}
	actualFact, err := eventcontract.DecodeRuntimeOutboxItem(placeItem)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := auction.DecideRuntimePlaceBid(startDecision.State, auction.RuntimePlaceBidCommand{
		Meta:  auction.RuntimeCommandMeta{EventID: placeEventID, TraceID: "trace-cap-place", IdempotencyKey: businessKey},
		BidID: "bid-cap", UserID: "buyer-cap", Nickname: "封顶买家", AmountFen: capPrice, Currency: "CNY",
		OrderID: "order-cap", RankingLimit: 20, NowUnixMs: actualFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go cap model: %v", err)
	}
	equal, err := eventcontract.RuntimeFactBinaryEqual(expected.Fact, actualFact)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatalf("cap Lua fact differs from Go model\nGo:  %v\nLua: %v", expected.Fact, actualFact)
	}
	if actualFact.GetOrderDraft().GetOrderId() != "order-cap" || actualFact.GetStateAfter().GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED {
		t.Fatalf("cap settlement fact mismatch: %+v", actualFact)
	}
	if _, err := client.Get(ctx, keys[8]).Result(); err != redis.Nil {
		t.Fatalf("cap settlement retained room active key: %v", err)
	}
	if displayed, err := client.Get(ctx, keys[10]).Result(); err != nil || displayed != lotID {
		t.Fatalf("cap settlement display pointer=%q error=%v", displayed, err)
	}
	if _, err := client.ZScore(ctx, keys[6], lotID).Result(); err != redis.Nil {
		t.Fatalf("cap settlement retained expiring candidate: %v", err)
	}
	for _, key := range []string{keys[0], keys[1], keys[2], keys[3], keys[4], keys[5]} {
		if ttl, err := client.TTL(ctx, key).Result(); err != nil || ttl <= runtimeTerminalRetention-time.Minute || ttl > runtimeTerminalRetention {
			t.Fatalf("terminal key %s TTL=%s error=%v", key, ttl, err)
		}
	}

	retryArgs := append([]any(nil), placeArgs...)
	retryArgs[0], err = eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	retryRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, retryArgs...).Text()
	if err != nil {
		t.Fatalf("retry cap bid: %v", err)
	}
	var retryResponse struct {
		OK       bool `json:"ok"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal([]byte(retryRaw), &retryResponse); err != nil || !retryResponse.OK || !retryResponse.Replayed {
		t.Fatalf("cap retry response=%s error=%v", retryRaw, err)
	}
	if length, err := client.LLen(ctx, keys[7]).Result(); err != nil || length != 2 {
		t.Fatalf("cap retry duplicated outbox: length=%d error=%v", length, err)
	}
}

func assertRuntimeLuaPhases(t *testing.T, source string) {
	t.Helper()
	readMarker := strings.Index(source, "-- PHASE: READ")
	validateMarker := strings.Index(source, "-- PHASE: VALIDATE_AND_SERIALIZE")
	writeMarker := strings.Index(source, "-- PHASE: WRITE")
	if readMarker < 0 || validateMarker <= readMarker || writeMarker <= validateMarker {
		t.Fatalf("Lua phases are missing or out of order: read=%d validate=%d write=%d", readMarker, validateMarker, writeMarker)
	}
	writePhase := source[writeMarker:]
	validatePhase := source[validateMarker:writeMarker]
	for _, forbidden := range []string{
		"redis.call('DEL'", "redis.call('SET'", "redis.call('HSET'", "redis.call('LPUSH'", "redis.call('ZADD'", "redis.call('ZREM'", "redis.call('EXPIRE'", "redis.call('PERSIST'",
	} {
		if strings.Contains(validatePhase, forbidden) {
			t.Fatalf("runtime Lua validation phase contains mutation %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"cjson.decode",
		"cjson.encode",
		"tonumber(",
		"redis.call('GET'",
		"redis.call('HGET'",
		"redis.call('HMGET'",
		"redis.call('ZRANGE'",
		"redis.call('ZREVRANGE'",
		"redis.call('SCARD'",
		"redis.call('SISMEMBER'",
		"redis.call('TIME'",
	} {
		if strings.Contains(writePhase, forbidden) {
			t.Fatalf("Lua write phase contains fallible operation %q", forbidden)
		}
	}
	if strings.Index(source, "local fact_payload = cjson.encode") > writeMarker {
		t.Fatal("runtime fact must be serialized before the write phase")
	}
	for _, required := range []string{
		"local state_after_payload = cjson.encode(state_after)",
		"'last_event_id', event_id",
		"'state_after_json', state_after_payload",
		"redis.call('EXISTS', frozen_lot_key)",
		"return reject('LOT_FROZEN')",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("runtime Lua is missing reconciliation identity %q", required)
		}
	}
	if len(source) == 0 {
		t.Fatal("Lua source is empty")
	}
}

func assertRuntimeStateIdentity(t *testing.T, ctx context.Context, client *redis.Client, stateKey string, fact *v1.RuntimeFactV1) {
	t.Helper()
	values, err := client.HMGet(ctx, stateKey, "version", "last_event_id", "state_after_json").Result()
	if err != nil {
		t.Fatalf("read runtime reconciliation identity: %v", err)
	}
	if fmt.Sprint(values[0]) != strconv.FormatInt(fact.GetLotVersion(), 10) {
		t.Fatalf("runtime identity version=%v want=%d", values[0], fact.GetLotVersion())
	}
	if fmt.Sprint(values[1]) != fact.GetEventId() {
		t.Fatalf("runtime identity event_id=%v want=%s", values[1], fact.GetEventId())
	}
	state := new(v1.LotRuntimeStateV1)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(fmt.Sprint(values[2])), state); err != nil {
		t.Fatalf("decode runtime state_after_json: %v", err)
	}
	if !proto.Equal(state, fact.GetStateAfter()) {
		t.Fatalf("runtime state_after_json differs from emitted fact\nstored: %v\nfact:   %v", state, fact.GetStateAfter())
	}
	storedHash, err := eventcontract.CanonicalStateHash(state)
	if err != nil {
		t.Fatalf("hash stored runtime state: %v", err)
	}
	factHash, err := eventcontract.CanonicalStateHash(fact.GetStateAfter())
	if err != nil {
		t.Fatalf("hash emitted runtime state: %v", err)
	}
	if storedHash != factHash {
		t.Fatalf("runtime canonical hash=%s want=%s", storedHash, factHash)
	}
}
