package data

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestRuntimeLuaMatchesGoModelAcrossCommandSequences(t *testing.T) {
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if redisAddress == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	for seed := int64(1); seed <= 32; seed++ {
		t.Run(fmt.Sprintf("seed_%02d", seed), func(t *testing.T) {
			random := rand.New(rand.NewSource(seed))
			runRuntimeCommandSequence(t, ctx, client, random, seed)
		})
	}
}

func isTerminalLotStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

func runRuntimeCommandSequence(t *testing.T, ctx context.Context, client *redis.Client, random *rand.Rand, seed int64) {
	t.Helper()
	startEventID := mustRuntimeEventID(t)
	lotID := fmt.Sprintf("lua-property-%d-%s", seed, startEventID)
	roomID := fmt.Sprintf("lua-property-room-%d-%s", seed, startEventID)
	shard := 100_000 + int(seed)
	keys := runtimePropertyKeys(lotID, roomID, shard)
	cleanupRuntimePropertyKeys(t, client, keys, lotID)

	startPrice := int64(10_000 + random.Intn(20_000))
	minIncrement := int64(1+random.Intn(20)) * 100
	config := auction.RuntimeConfigSnapshot{
		LotID: lotID, RoomID: roomID, MainAccountID: fmt.Sprintf("main-%d", seed),
		Title: fmt.Sprintf("property lot %d", seed), ImageURL: "https://example.test/property.png",
		ConfigVersion: 1, Currency: "CNY", StartPriceFen: startPrice, MinIncrementFen: minIncrement,
		DurationMs: 300_000, AntiSnipeWindowMs: 10_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
	}
	startArgs := []any{
		startEventID, "trace-property-start", config.LotID, config.RoomID, config.MainAccountID, config.Title, config.ImageURL,
		"1", strconv.Itoa(int(v1.LotStatus_LOT_STATUS_DRAFT)), "0", config.Currency,
		strconv.FormatInt(config.StartPriceFen, 10), strconv.FormatInt(config.MinIncrementFen, 10), "",
		strconv.FormatInt(config.DurationMs, 10), strconv.FormatInt(config.AntiSnipeWindowMs, 10),
		strconv.FormatInt(config.AntiSnipeExtendMs, 10), strconv.Itoa(int(config.MaxExtendCount)),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
	}
	startFact := runRuntimePropertyScript(t, ctx, client, runtimeStartLotScriptV1, keys, startArgs)
	startDecision, err := auction.DecideRuntimeStartLot(auction.RuntimeStartLotCommand{
		Meta: auction.RuntimeCommandMeta{EventID: startEventID, TraceID: "trace-property-start"}, Config: config,
		PreviousStatus: v1.LotStatus_LOT_STATUS_DRAFT, PreviousLotVersion: 0, NowUnixMs: startFact.GetOccurredAtUnixMs(),
	})
	if err != nil {
		t.Fatalf("Go start decision: %v", err)
	}
	assertRuntimePropertyFact(t, startDecision.Fact, startFact)
	state := startDecision.State
	writtenFacts := int64(1)

	if random.Intn(2) == 0 {
		nextConfig := state.Config
		nextConfig.ConfigVersion++
		nextConfig.MinIncrementFen += int64(1+random.Intn(5)) * 100
		syncEventID := mustRuntimeEventID(t)
		syncArgs := []any{
			syncEventID, "trace-property-sync", strconv.FormatInt(state.Config.ConfigVersion, 10), strconv.FormatInt(nextConfig.ConfigVersion, 10),
			nextConfig.LotID, nextConfig.RoomID, nextConfig.MainAccountID, nextConfig.Title, nextConfig.ImageURL,
			nextConfig.Currency, strconv.FormatInt(nextConfig.StartPriceFen, 10), strconv.FormatInt(nextConfig.MinIncrementFen, 10), "",
			strconv.FormatInt(nextConfig.DurationMs, 10), strconv.FormatInt(nextConfig.AntiSnipeWindowMs, 10),
			strconv.FormatInt(nextConfig.AntiSnipeExtendMs, 10), strconv.Itoa(int(nextConfig.MaxExtendCount)),
			strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20",
		}
		syncKeys := []string{keys[0], keys[1], keys[2], keys[3], keys[7], keys[9]}
		syncFact := runRuntimePropertyScript(t, ctx, client, runtimeSyncLotConfigScriptV1, syncKeys, syncArgs)
		syncDecision, decisionErr := auction.DecideRuntimeSyncLotConfig(state, auction.RuntimeSyncLotConfigCommand{
			Meta:                  auction.RuntimeCommandMeta{EventID: syncEventID, TraceID: "trace-property-sync"},
			ExpectedConfigVersion: state.Config.ConfigVersion, NextConfig: nextConfig, NowUnixMs: syncFact.GetOccurredAtUnixMs(),
		})
		if decisionErr != nil {
			t.Fatalf("Go sync decision: %v", decisionErr)
		}
		assertRuntimePropertyFact(t, syncDecision.Fact, syncFact)
		state = syncDecision.State
		writtenFacts++
	}

	bidCount := 1 + random.Intn(6)
	for bidIndex := 0; bidIndex < bidCount; bidIndex++ {
		previousVersion := state.Version
		amount := state.CurrentPriceFen + state.Config.MinIncrementFen + int64(random.Intn(4))*state.Config.MinIncrementFen
		userID := fmt.Sprintf("buyer-%d", bidIndex%4)
		nickname := fmt.Sprintf("买家%d", random.Intn(100))
		eventID := mustRuntimeEventID(t)
		businessKey := fmt.Sprintf("property-%d-%d", seed, bidIndex)
		bidID := fmt.Sprintf("bid-%d-%d", seed, bidIndex)
		placeArgs := []any{
			eventID, "trace-property-bid", bidID, userID, nickname, auction.MaskBuyerNickname(nickname), "",
			strconv.FormatInt(amount, 10), state.Config.Currency, runtimeIdempotencyField(userID, businessKey), businessKey,
			fmt.Sprintf("order-%d", seed), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)),
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)),
			strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
			"20", "20", strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), "1000",
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
		}
		placeFact := runRuntimePropertyScript(t, ctx, client, runtimePlaceBidScriptV1, keys, placeArgs)
		placeDecision, decisionErr := auction.DecideRuntimePlaceBid(state, auction.RuntimePlaceBidCommand{
			Meta:  auction.RuntimeCommandMeta{EventID: eventID, TraceID: "trace-property-bid", IdempotencyKey: businessKey},
			BidID: bidID, UserID: userID, Nickname: nickname, AmountFen: amount, Currency: state.Config.Currency,
			OrderID: fmt.Sprintf("order-%d", seed), RankingLimit: 20, NowUnixMs: placeFact.GetOccurredAtUnixMs(),
		})
		if decisionErr != nil {
			t.Fatalf("Go bid %d decision: %v", bidIndex, decisionErr)
		}
		assertRuntimePropertyFact(t, placeDecision.Fact, placeFact)
		if placeFact.GetPrevLotVersion() != previousVersion || placeFact.GetLotVersion() != previousVersion+1 {
			t.Fatalf("bid %d version chain = %d->%d, want %d->%d", bidIndex, placeFact.GetPrevLotVersion(), placeFact.GetLotVersion(), previousVersion, previousVersion+1)
		}
		state = placeDecision.State
		writtenFacts++
	}

	if random.Intn(2) == 0 {
		eventID := mustRuntimeEventID(t)
		cancelArgs := []any{
			eventID, "trace-property-cancel", "property cancellation", "operator-property",
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)),
			strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)),
			strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20",
		}
		cancelFact := runRuntimePropertyScript(t, ctx, client, runtimeCancelLotScriptV1, keys, cancelArgs)
		cancelDecision, decisionErr := auction.DecideRuntimeCancelLot(state, auction.RuntimeCancelLotCommand{
			Meta:   auction.RuntimeCommandMeta{EventID: eventID, TraceID: "trace-property-cancel"},
			Reason: "property cancellation", OperatorID: "operator-property", NowUnixMs: cancelFact.GetOccurredAtUnixMs(),
		})
		if decisionErr != nil {
			t.Fatalf("Go cancel decision: %v", decisionErr)
		}
		assertRuntimePropertyFact(t, cancelDecision.Fact, cancelFact)
		state = cancelDecision.State
	} else {
		state.EndsAtUnixMs = 1
		if err := client.HSet(ctx, keys[0], "ends_at_unix_ms", state.EndsAtUnixMs).Err(); err != nil {
			t.Fatalf("force expired deadline: %v", err)
		}
		eventID := mustRuntimeEventID(t)
		orderID := fmt.Sprintf("order-%d", seed)
		closeArgs := []any{
			eventID, "trace-property-close", orderID, strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)),
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
			strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)),
			strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
			"20", auction.RuntimeExpiredNoBidReason,
		}
		closeFact := runRuntimePropertyScript(t, ctx, client, runtimeCloseIfExpiredScriptV1, keys, closeArgs)
		closeDecision, decisionErr := auction.DecideRuntimeCloseIfExpired(state, auction.RuntimeCloseIfExpiredCommand{
			Meta:    auction.RuntimeCommandMeta{EventID: eventID, TraceID: "trace-property-close"},
			OrderID: orderID, NowUnixMs: closeFact.GetOccurredAtUnixMs(),
		})
		if decisionErr != nil {
			t.Fatalf("Go close decision: %v", decisionErr)
		}
		assertRuntimePropertyFact(t, closeDecision.Fact, closeFact)
		state = closeDecision.State
	}
	writtenFacts++

	if !isTerminalLotStatus(state.Status) {
		t.Fatalf("sequence ended in non-terminal status %s", state.Status)
	}
	if length, err := client.LLen(ctx, keys[7]).Result(); err != nil || length != writtenFacts {
		t.Fatalf("outbox length=%d want=%d error=%v", length, writtenFacts, err)
	}
	if storedVersion, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || storedVersion != state.Version {
		t.Fatalf("stored version=%d want=%d error=%v", storedVersion, state.Version, err)
	}
	if _, err := client.Get(ctx, keys[8]).Result(); err != redis.Nil {
		t.Fatalf("terminal sequence retained room active pointer: %v", err)
	}
}

func runRuntimePropertyScript(t *testing.T, ctx context.Context, client *redis.Client, script *redis.Script, keys []string, args []any) *v1.RuntimeFactV1 {
	t.Helper()
	raw, err := script.Run(ctx, client, keys, args...).Text()
	if err != nil {
		t.Fatalf("run runtime Lua: %v", err)
	}
	var response struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("decode runtime Lua response %q: %v", raw, err)
	}
	if !response.OK {
		t.Fatalf("runtime Lua rejected property command: code=%s response=%s", response.Code, raw)
	}
	item, err := client.LIndex(ctx, keys[runtimePropertyOutboxKeyIndex(keys)], 0).Result()
	if err != nil {
		t.Fatalf("read runtime outbox: %v", err)
	}
	fact, err := eventcontract.DecodeRuntimeOutboxItem(item)
	if err != nil {
		t.Fatalf("decode runtime outbox item: %v", err)
	}
	return fact
}

func runtimePropertyOutboxKeyIndex(keys []string) int {
	if len(keys) == 6 {
		return 4
	}
	return 7
}

func assertRuntimePropertyFact(t *testing.T, expected, actual *v1.RuntimeFactV1) {
	t.Helper()
	equal, err := eventcontract.RuntimeFactBinaryEqual(expected, actual)
	if err != nil {
		t.Fatalf("compare runtime facts: %v", err)
	}
	if !equal {
		t.Fatalf("Lua fact differs from Go model\nGo:  %v\nLua: %v", expected, actual)
	}
}

func mustRuntimeEventID(t *testing.T) string {
	t.Helper()
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatalf("create event ID: %v", err)
	}
	return eventID
}

func runtimePropertyKeys(lotID, roomID string, shard int) []string {
	return []string{
		runtimeStateKey(lotID), runtimeRankingKey(lotID), runtimeRankMetaKey(lotID), runtimeParticipantsKey(lotID),
		runtimeRecentKey(lotID), runtimeIdempotencyHashKey(lotID), runtimeExpiringKey(), runtimeOutboxPendingKey(shard),
		runtimeRoomActiveLotKey(roomID), runtimeFrozenLotKey(lotID), runtimeRoomDisplayLotKey(roomID),
	}
}

func cleanupRuntimePropertyKeys(t *testing.T, client *redis.Client, keys []string, lotID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Del(ctx, keys[0], keys[1], keys[2], keys[3], keys[4], keys[5], keys[7], keys[8], keys[9], keys[10]).Err()
		_ = client.ZRem(ctx, keys[6], lotID).Err()
	})
}
