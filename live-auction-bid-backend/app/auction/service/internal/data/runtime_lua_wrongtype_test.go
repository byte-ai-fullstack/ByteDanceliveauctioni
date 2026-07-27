package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestRuntimeLuaPreflightsWrongTypeKeysBeforeWrites(t *testing.T) {
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

	tests := []struct {
		name       string
		command    string
		corruptKey int
	}{
		{name: "start_expiring", command: "start", corruptKey: 6},
		{name: "start_outbox", command: "start", corruptKey: 7},
		{name: "sync_outbox", command: "sync", corruptKey: 7},
		{name: "cancel_expiring", command: "cancel", corruptKey: 6},
		{name: "cancel_outbox", command: "cancel", corruptKey: 7},
		{name: "close_expiring", command: "close", corruptKey: 6},
		{name: "close_outbox", command: "close", corruptKey: 7},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureID := mustRuntimeEventID(t)
			lotID := fmt.Sprintf("wrong-lot-%d-%s", index, fixtureID)
			roomID := fmt.Sprintf("wrong-room-%d-%s", index, fixtureID)
			keys := runtimePropertyKeys(lotID, roomID, 200_000+index)
			cleanupRuntimeWrongTypeKeys(t, client, keys)
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Fatalf("reset wrong-type fixture: %v", err)
			}

			config := auction.RuntimeConfigSnapshot{
				LotID: lotID, RoomID: roomID, MainAccountID: "main-wrongtype", Title: "wrong-type lot",
				ImageURL: "https://example.test/wrongtype.png", ConfigVersion: 1, Currency: "CNY",
				StartPriceFen: 10_000, MinIncrementFen: 100, DurationMs: 300_000,
				AntiSnipeWindowMs: 10_000, AntiSnipeExtendMs: 30_000, MaxExtendCount: 3,
			}
			startArgs := runtimeWrongTypeStartArgs(t, config)
			if test.command != "start" {
				runRuntimePropertyScript(t, ctx, client, runtimeStartLotScriptV1, keys, startArgs)
			}

			var script *redis.Script
			var scriptKeys []string
			var args []any
			switch test.command {
			case "start":
				script, scriptKeys, args = runtimeStartLotScriptV1, keys, startArgs
			case "sync":
				script = runtimeSyncLotConfigScriptV1
				scriptKeys = []string{keys[0], keys[1], keys[2], keys[3], keys[7], keys[9]}
				args = runtimeWrongTypeSyncArgs(t, config)
			case "cancel":
				script, scriptKeys, args = runtimeCancelLotScriptV1, keys, runtimeWrongTypeCancelArgs(t)
			case "close":
				if err := client.HSet(ctx, keys[0], "ends_at_unix_ms", 1).Err(); err != nil {
					t.Fatalf("force expired deadline: %v", err)
				}
				if err := client.ZAdd(ctx, keys[6], redis.Z{Score: 1, Member: lotID}).Err(); err != nil {
					t.Fatalf("force expiring candidate: %v", err)
				}
				script, scriptKeys, args = runtimeCloseIfExpiredScriptV1, keys, runtimeWrongTypeCloseArgs(t)
			default:
				t.Fatalf("unsupported command %q", test.command)
			}

			if err := client.Del(ctx, keys[test.corruptKey]).Err(); err != nil {
				t.Fatalf("clear key before corruption: %v", err)
			}
			if err := client.Set(ctx, keys[test.corruptKey], "wrong-type-sentinel", 0).Err(); err != nil {
				t.Fatalf("corrupt runtime key: %v", err)
			}
			before := snapshotRuntimeMutationSurface(t, ctx, client, keys)

			raw, err := script.Run(ctx, client, scriptKeys, args...).Text()
			if err != nil {
				t.Fatalf("runtime script returned Redis error after partial-write preflight: %v", err)
			}
			var response struct {
				OK   bool   `json:"ok"`
				Code string `json:"code"`
			}
			if err := json.Unmarshal([]byte(raw), &response); err != nil {
				t.Fatalf("decode wrong-type response %q: %v", raw, err)
			}
			if response.OK || response.Code != "RUNTIME_STATE_CORRUPT" {
				t.Fatalf("wrong-type response=%s", raw)
			}

			after := snapshotRuntimeMutationSurface(t, ctx, client, keys)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("%s mutated Redis before wrong-type rejection\nbefore: %#v\nafter:  %#v", test.command, before, after)
			}
		})
	}
}

func runtimeWrongTypeStartArgs(t *testing.T, config auction.RuntimeConfigSnapshot) []any {
	t.Helper()
	return []any{
		mustRuntimeEventID(t), "trace-wrongtype-start", config.LotID, config.RoomID, config.MainAccountID, config.Title, config.ImageURL,
		strconv.FormatInt(config.ConfigVersion, 10), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_DRAFT)), "0", config.Currency,
		strconv.FormatInt(config.StartPriceFen, 10), strconv.FormatInt(config.MinIncrementFen, 10), "",
		strconv.FormatInt(config.DurationMs, 10), strconv.FormatInt(config.AntiSnipeWindowMs, 10),
		strconv.FormatInt(config.AntiSnipeExtendMs, 10), strconv.Itoa(int(config.MaxExtendCount)),
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_LIVE)), strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes),
	}
}

func runtimeWrongTypeSyncArgs(t *testing.T, config auction.RuntimeConfigSnapshot) []any {
	t.Helper()
	return []any{
		mustRuntimeEventID(t), "trace-wrongtype-sync", "1", "2", config.LotID, config.RoomID, config.MainAccountID,
		config.Title, config.ImageURL, config.Currency, strconv.FormatInt(config.StartPriceFen, 10), "200", "",
		strconv.FormatInt(config.DurationMs, 10), strconv.FormatInt(config.AntiSnipeWindowMs, 10),
		strconv.FormatInt(config.AntiSnipeExtendMs, 10), strconv.Itoa(int(config.MaxExtendCount)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20",
	}
}

func runtimeWrongTypeCancelArgs(t *testing.T) []any {
	t.Helper()
	return []any{
		mustRuntimeEventID(t), "trace-wrongtype-cancel", "wrong-type cancellation", "operator-wrongtype",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_CANCELLED)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
		strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20",
	}
}

func runtimeWrongTypeCloseArgs(t *testing.T) []any {
	t.Helper()
	return []any{
		mustRuntimeEventID(t), "trace-wrongtype-close", "order-wrongtype",
		strconv.Itoa(int(v1.LotStatus_LOT_STATUS_SETTLED)), strconv.Itoa(int(v1.LotStatus_LOT_STATUS_FAILED)),
		strconv.Itoa(int(v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED)),
		strconv.Itoa(int(eventcontract.RuntimeSchemaVersionV1)), strconv.FormatInt(int64(runtimeTerminalRetention/time.Second), 10),
		strconv.Itoa(eventcontract.MaxRuntimeFactBytes), "20", auction.RuntimeExpiredNoBidReason,
	}
}

type runtimeRedisKeySnapshot struct {
	Type        string
	Value       any
	TTLCategory string
}

func snapshotRuntimeMutationSurface(t *testing.T, ctx context.Context, client *redis.Client, keys []string) map[string]runtimeRedisKeySnapshot {
	t.Helper()
	snapshot := make(map[string]runtimeRedisKeySnapshot, len(keys))
	for _, key := range keys {
		keyType, err := client.Type(ctx, key).Result()
		if err != nil {
			t.Fatalf("read Redis key type for %s: %v", key, err)
		}
		var value any
		switch keyType {
		case "none":
		case "string":
			value, err = client.Get(ctx, key).Result()
		case "hash":
			value, err = client.HGetAll(ctx, key).Result()
		case "list":
			value, err = client.LRange(ctx, key, 0, -1).Result()
		case "set":
			members, membersErr := client.SMembers(ctx, key).Result()
			err = membersErr
			sort.Strings(members)
			value = members
		case "zset":
			value, err = client.ZRangeWithScores(ctx, key, 0, -1).Result()
		default:
			err = fmt.Errorf("unsupported Redis key type %q", keyType)
		}
		if err != nil {
			t.Fatalf("read Redis key %s as %s: %v", key, keyType, err)
		}
		ttl, err := client.PTTL(ctx, key).Result()
		if err != nil {
			t.Fatalf("read Redis TTL for %s: %v", key, err)
		}
		ttlCategory := "expiring"
		switch ttl {
		case -2:
			ttlCategory = "missing"
		case -1:
			ttlCategory = "persistent"
		}
		snapshot[key] = runtimeRedisKeySnapshot{Type: keyType, Value: value, TTLCategory: ttlCategory}
	}
	return snapshot
}

func cleanupRuntimeWrongTypeKeys(t *testing.T, client *redis.Client, keys []string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Del(ctx, keys...).Err()
	})
}
