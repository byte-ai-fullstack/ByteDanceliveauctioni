package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestRuntimeLuaLinearizesTailBidAgainstClose(t *testing.T) {
	ctx, client := runtimeConcurrencyRedis(t)
	for iteration := 0; iteration < 12; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			fixtureID := mustRuntimeEventID(t)
			lotID := fmt.Sprintf("tail-lot-%d-%s", iteration, fixtureID)
			roomID := fmt.Sprintf("tail-room-%d-%s", iteration, fixtureID)
			keys := runtimePropertyKeys(lotID, roomID, 300_000+iteration)
			cleanupRuntimePropertyKeys(t, client, keys, lotID)

			config := auction.RuntimeConfigSnapshot{
				LotID: lotID, RoomID: roomID, MainAccountID: "main-tail", Title: "tail bid race",
				ImageURL: "https://example.test/tail.png", ConfigVersion: 1, Currency: "CNY",
				StartPriceFen: 10_000, MinIncrementFen: 100, DurationMs: 80,
				AntiSnipeWindowMs: 1_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
			}
			startFact := runRuntimePropertyScript(t, ctx, client, runtimeStartLotScriptV1, keys, runtimeConcurrencyStartArgs(t, config))
			originalDeadline := startFact.GetStateAfter().GetEndsAtUnixMs()
			wait := time.Until(time.UnixMilli(originalDeadline).Add(-2 * time.Millisecond))
			if wait > 0 {
				time.Sleep(wait)
			}

			bidEventID := mustRuntimeEventID(t)
			businessKey := fmt.Sprintf("tail-race-%d", iteration)
			bidArgs := []any{
				bidEventID, "trace-tail-bid", fmt.Sprintf("tail-bid-%d", iteration), "tail-buyer", "尾声买家", auction.MaskBuyerNickname("尾声买家"), "",
				"10100", "CNY", runtimeIdempotencyField("tail-buyer", businessKey), businessKey, "order-tail",
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)),
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)),
				strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
				"20", "20", strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), "1000",
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
			}
			closeEventID := mustRuntimeEventID(t)
			closeArgs := []any{
				closeEventID, "trace-tail-close", "order-tail", strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)),
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED)),
				strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
				strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20", auction.RuntimeExpiredNoBidReason,
			}

			bidResult, closeResult := runRuntimeRace(ctx, client,
				runtimeScriptCall{script: runtimePlaceBidScriptV1, keys: keys, args: bidArgs},
				runtimeScriptCall{script: runtimeCloseIfExpiredScriptV1, keys: keys, args: closeArgs},
			)
			bidReply := decodeRuntimeRaceReply(t, "bid", bidResult)
			closeReply := decodeRuntimeRaceReply(t, "close", closeResult)
			if bidReply.OK && closeReply.OK {
				t.Fatalf("tail bid and close both committed: bid=%+v close=%+v", bidReply, closeReply)
			}
			if closeReply.OK && closeReply.OccurredAtUnixMs < originalDeadline {
				t.Fatalf("close committed at %d before original deadline %d", closeReply.OccurredAtUnixMs, originalDeadline)
			}

			version, err := client.HGet(ctx, keys[0], "version").Int64()
			if err != nil {
				t.Fatalf("read tail-race version: %v", err)
			}
			outboxLength, err := client.LLen(ctx, keys[7]).Result()
			if err != nil {
				t.Fatalf("read tail-race outbox: %v", err)
			}
			successes := int64(0)
			if bidReply.OK {
				successes++
				endsAt, endsErr := client.HGet(ctx, keys[0], "ends_at_unix_ms").Int64()
				if endsErr != nil || endsAt <= originalDeadline {
					t.Fatalf("accepted tail bid did not extend deadline: ends_at=%d original=%d error=%v", endsAt, originalDeadline, endsErr)
				}
			}
			if closeReply.OK {
				successes++
			}
			if version != 1+successes || outboxLength != 1+successes {
				t.Fatalf("tail race version/outbox=(%d,%d), want %d", version, outboxLength, 1+successes)
			}
		})
	}
}

func TestRuntimeLuaKeepsExtendedLotLiveAcrossOriginalDeadline(t *testing.T) {
	ctx, client := runtimeConcurrencyRedis(t)
	fixtureID := mustRuntimeEventID(t)
	lotID := "extended-tail-lot-" + fixtureID
	roomID := "extended-tail-room-" + fixtureID
	keys := runtimePropertyKeys(lotID, roomID, 310_000)
	cleanupRuntimePropertyKeys(t, client, keys, lotID)

	config := auction.RuntimeConfigSnapshot{
		LotID: lotID, RoomID: roomID, MainAccountID: "main-extended-tail", Title: "extended tail close race",
		ImageURL: "https://example.test/extended-tail.png", ConfigVersion: 1, Currency: "CNY",
		StartPriceFen: 10_000, MinIncrementFen: 100, DurationMs: 500,
		AntiSnipeWindowMs: 1_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
	}
	startFact := runRuntimePropertyScript(t, ctx, client, runtimeStartLotScriptV1, keys, runtimeConcurrencyStartArgs(t, config))
	originalDeadline := startFact.GetStateAfter().GetEndsAtUnixMs()
	redisNow, err := client.Time(ctx).Result()
	if err != nil {
		t.Fatalf("read Redis time before tail window: %v", err)
	}
	wait := time.Duration(originalDeadline-100-redisNow.UnixMilli()) * time.Millisecond
	if wait <= 0 {
		t.Fatalf("tail fixture left no setup margin: deadline=%d redis_now=%d", originalDeadline, redisNow.UnixMilli())
	}
	time.Sleep(wait)

	makeBidArgs := func(index int, eventID string) []any {
		userID := fmt.Sprintf("extended-tail-buyer-%d", index)
		nickname := fmt.Sprintf("尾拍买家%d", index)
		businessKey := fmt.Sprintf("extended-tail-bid-%d", index)
		return []any{
			eventID, "trace-extended-tail-bid", businessKey, userID, nickname, auction.MaskBuyerNickname(nickname), "",
			strconv.FormatInt(10_100+int64(index)*100, 10), "CNY", runtimeIdempotencyField(userID, businessKey), businessKey, "order-extended-tail",
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)),
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
			"20", "20", strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), "1000",
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
		}
	}
	makeCloseArgs := func(eventID string) []any {
		return []any{
			eventID, "trace-extended-tail-close", "order-extended-tail", strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)),
			strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED)),
			strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
			strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20", auction.RuntimeExpiredNoBidReason,
		}
	}

	seedRaw, err := runtimePlaceBidScriptV1.Run(ctx, client, keys, makeBidArgs(0, mustRuntimeEventID(t))...).Text()
	seedReply := decodeRuntimeRaceReply(t, "tail seed bid", runtimeScriptResult{raw: seedRaw, err: err})
	if !seedReply.OK {
		t.Fatalf("tail seed bid was rejected: %+v", seedReply)
	}
	extendedDeadline, err := client.HGet(ctx, keys[0], "ends_at_unix_ms").Int64()
	if err != nil || extendedDeadline != originalDeadline+config.AntiSnipeExtendMs {
		t.Fatalf("tail seed deadline=%d, want %d error=%v", extendedDeadline, originalDeadline+config.AntiSnipeExtendMs, err)
	}

	const closeAttempts = 120
	closeEventIDs := make([]string, closeAttempts)
	for index := range closeEventIDs {
		closeEventIDs[index] = mustRuntimeEventID(t)
	}
	closeResults := make(chan runtimeScriptResult, closeAttempts)
	var closeWait sync.WaitGroup
	closeWait.Add(1)
	go func() {
		defer closeWait.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for index := 0; index < closeAttempts; index++ {
			<-ticker.C
			raw, runErr := runtimeCloseIfExpiredScriptV1.Run(ctx, client, keys, makeCloseArgs(closeEventIDs[index])...).Text()
			closeResults <- runtimeScriptResult{raw: raw, err: runErr}
		}
	}()

	const trailingBids = 8
	for index := 1; index <= trailingBids; index++ {
		time.Sleep(15 * time.Millisecond)
		raw, runErr := runtimePlaceBidScriptV1.Run(ctx, client, keys, makeBidArgs(index, mustRuntimeEventID(t))...).Text()
		reply := decodeRuntimeRaceReply(t, fmt.Sprintf("trailing bid %d", index), runtimeScriptResult{raw: raw, err: runErr})
		if !reply.OK {
			t.Fatalf("trailing bid %d was rejected: %+v", index, reply)
		}
	}
	closeWait.Wait()
	close(closeResults)

	for result := range closeResults {
		reply := decodeRuntimeRaceReply(t, "high-frequency close", result)
		if reply.OK || reply.Code != "NOT_EXPIRED" || reply.EndsAtUnixMs != extendedDeadline {
			t.Fatalf("close worker committed against extended deadline: %+v", reply)
		}
	}
	redisNow, err = client.Time(ctx).Result()
	if err != nil {
		t.Fatalf("read Redis time after close ticks: %v", err)
	}
	if redisNow.UnixMilli() <= originalDeadline {
		t.Fatalf("close ticks did not cross original deadline: redis_now=%d deadline=%d", redisNow.UnixMilli(), originalDeadline)
	}

	state, err := client.HMGet(ctx, keys[0], "status", "version", "bid_count", "extend_count", "ends_at_unix_ms").Result()
	if err != nil {
		t.Fatalf("read extended tail state: %v", err)
	}
	wantVersion := int64(2 + trailingBids)
	if fmt.Sprint(state[0]) != strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)) ||
		fmt.Sprint(state[1]) != strconv.FormatInt(wantVersion, 10) ||
		fmt.Sprint(state[2]) != strconv.FormatInt(1+trailingBids, 10) ||
		fmt.Sprint(state[3]) != "1" || fmt.Sprint(state[4]) != strconv.FormatInt(extendedDeadline, 10) {
		t.Fatalf("unexpected extended tail state: %v", state)
	}
	if outboxLength, outboxErr := client.LLen(ctx, keys[7]).Result(); outboxErr != nil || outboxLength != wantVersion {
		t.Fatalf("extended tail outbox length=%d, want %d error=%v", outboxLength, wantVersion, outboxErr)
	}
	if expiringScore, scoreErr := client.ZScore(ctx, keys[6], lotID).Result(); scoreErr != nil || int64(expiringScore) != extendedDeadline {
		t.Fatalf("extended tail expiring score=%v, want %d error=%v", expiringScore, extendedDeadline, scoreErr)
	}
	if activeLotID, activeErr := client.Get(ctx, keys[8]).Result(); activeErr != nil || activeLotID != lotID {
		t.Fatalf("extended tail active lot=%q, want %q error=%v", activeLotID, lotID, activeErr)
	}
}

func TestRuntimeLuaCommitsExactlyOneTerminalFactForCapBidAgainstCancel(t *testing.T) {
	ctx, client := runtimeConcurrencyRedis(t)
	for iteration := 0; iteration < 32; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			fixtureID := mustRuntimeEventID(t)
			lotID := fmt.Sprintf("cap-race-lot-%d-%s", iteration, fixtureID)
			roomID := fmt.Sprintf("cap-race-room-%d-%s", iteration, fixtureID)
			keys := runtimePropertyKeys(lotID, roomID, 400_000+iteration)
			cleanupRuntimePropertyKeys(t, client, keys, lotID)
			capPrice := int64(10_500)
			config := auction.RuntimeConfigSnapshot{
				LotID: lotID, RoomID: roomID, MainAccountID: "main-cap-race", Title: "cap cancel race",
				ImageURL: "https://example.test/cap-race.png", ConfigVersion: 1, Currency: "CNY",
				StartPriceFen: 10_000, MinIncrementFen: 100, CapPriceFen: &capPrice, DurationMs: 300_000,
				AntiSnipeWindowMs: 10_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
			}
			runRuntimePropertyScript(t, ctx, client, runtimeStartLotScriptV1, keys, runtimeConcurrencyStartArgs(t, config))

			bidEventID := mustRuntimeEventID(t)
			businessKey := fmt.Sprintf("cap-cancel-race-%d", iteration)
			bidArgs := []any{
				bidEventID, "trace-cap-race-bid", fmt.Sprintf("cap-race-bid-%d", iteration), "cap-race-buyer", "封顶买家", auction.MaskBuyerNickname("封顶买家"), "",
				strconv.FormatInt(capPrice, 10), "CNY", runtimeIdempotencyField("cap-race-buyer", businessKey), businessKey, "order-cap-race",
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_EXTENDED)),
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID)),
				strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
				"20", "20", strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10), "1000",
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
			}
			cancelEventID := mustRuntimeEventID(t)
			cancelArgs := []any{
				cancelEventID, "trace-cap-race-cancel", "operator cancellation", "operator-cap-race",
				strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT)),
				strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
				strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20",
			}

			bidResult, cancelResult := runRuntimeRace(ctx, client,
				runtimeScriptCall{script: runtimePlaceBidScriptV1, keys: keys, args: bidArgs},
				runtimeScriptCall{script: runtimeCancelLotScriptV1, keys: keys, args: cancelArgs},
			)
			bidReply := decodeRuntimeRaceReply(t, "cap bid", bidResult)
			cancelReply := decodeRuntimeRaceReply(t, "cancel", cancelResult)
			if bidReply.OK == cancelReply.OK {
				t.Fatalf("exactly one terminal command must commit: bid=%+v cancel=%+v", bidReply, cancelReply)
			}

			status, err := client.HGet(ctx, keys[0], "status").Int64()
			if err != nil {
				t.Fatalf("read terminal status: %v", err)
			}
			wantStatus := int64(v1.LotStatus_LOT_STATUS_CANCELLED)
			winnerEventID := cancelEventID
			if bidReply.OK {
				wantStatus = int64(v1.LotStatus_LOT_STATUS_SETTLED)
				winnerEventID = bidEventID
			}
			lastEventID, err := client.HGet(ctx, keys[0], "last_event_id").Result()
			if err != nil || status != wantStatus || lastEventID != winnerEventID {
				t.Fatalf("terminal identity status=%d event=%q, want status=%d event=%q error=%v", status, lastEventID, wantStatus, winnerEventID, err)
			}
			if version, err := client.HGet(ctx, keys[0], "version").Int64(); err != nil || version != 2 {
				t.Fatalf("terminal version=%d error=%v", version, err)
			}
			if outboxLength, err := client.LLen(ctx, keys[7]).Result(); err != nil || outboxLength != 2 {
				t.Fatalf("terminal outbox length=%d error=%v", outboxLength, err)
			}
			if _, err := client.Get(ctx, keys[8]).Result(); err != redis.Nil {
				t.Fatalf("terminal race retained active room pointer: %v", err)
			}
			if _, err := client.ZScore(ctx, keys[6], lotID).Result(); err != redis.Nil {
				t.Fatalf("terminal race retained expiring candidate: %v", err)
			}
		})
	}
}

func runtimeConcurrencyRedis(t *testing.T) (context.Context, *redis.Client) {
	t.Helper()
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if redisAddress == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	if err := client.Ping(ctx).Err(); err != nil {
		cancel()
		_ = client.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
	})
	return ctx, client
}

func runtimeConcurrencyStartArgs(t *testing.T, config auction.RuntimeConfigSnapshot) []any {
	t.Helper()
	capPrice := ""
	if config.CapPriceFen != nil {
		capPrice = strconv.FormatInt(*config.CapPriceFen, 10)
	}
	return []any{
		mustRuntimeEventID(t), "trace-concurrency-start", config.LotID, config.RoomID, config.MainAccountID, config.Title, config.ImageURL,
		strconv.FormatInt(config.ConfigVersion, 10), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_DRAFT)), "0", config.Currency,
		strconv.FormatInt(config.StartPriceFen, 10), strconv.FormatInt(config.MinIncrementFen, 10), capPrice,
		strconv.FormatInt(config.DurationMs, 10), strconv.FormatInt(config.AntiSnipeWindowMs, 10),
		strconv.FormatInt(config.AntiSnipeExtendMs, 10), strconv.Itoa(int(config.MaxExtendCount)),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
	}
}

type runtimeScriptCall struct {
	script *redis.Script
	keys   []string
	args   []any
}

type runtimeScriptResult struct {
	raw string
	err error
}

func runRuntimeRace(ctx context.Context, client *redis.Client, first, second runtimeScriptCall) (runtimeScriptResult, runtimeScriptResult) {
	start := make(chan struct{})
	results := make([]runtimeScriptResult, 2)
	calls := []runtimeScriptCall{first, second}
	var wait sync.WaitGroup
	for index := range calls {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results[index].raw, results[index].err = calls[index].script.Run(ctx, client, calls[index].keys, calls[index].args...).Text()
		}()
	}
	close(start)
	wait.Wait()
	return results[0], results[1]
}

type runtimeRaceReply struct {
	OK               bool
	Code             string
	EventID          string
	OccurredAtUnixMs int64
	EndsAtUnixMs     int64
}

func decodeRuntimeRaceReply(t *testing.T, command string, result runtimeScriptResult) runtimeRaceReply {
	t.Helper()
	if result.err != nil {
		t.Fatalf("%s returned Redis error: %v", command, result.err)
	}
	var reply struct {
		OK               bool   `json:"ok"`
		Code             string `json:"code"`
		EventID          string `json:"event_id"`
		OccurredAtUnixMs int64  `json:"occurred_at_unix_ms"`
		EndsAtUnixMs     int64  `json:"ends_at_unix_ms"`
	}
	if err := json.Unmarshal([]byte(result.raw), &reply); err != nil {
		t.Fatalf("decode %s response %q: %v", command, result.raw, err)
	}
	return runtimeRaceReply(reply)
}
