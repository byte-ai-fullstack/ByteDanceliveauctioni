package data

import (
	_ "embed"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const runtimeTerminalRetention = 90 * 24 * time.Hour

//go:embed lua/start_lot.lua
var runtimeStartLotLua string

//go:embed lua/cancel_lot.lua
var runtimeCancelLotLuaV1 string

//go:embed lua/close_if_expired.lua
var runtimeCloseIfExpiredLuaV1 string

//go:embed lua/sync_lot_config.lua
var runtimeSyncLotConfigLuaV1 string

//go:embed lua/place_bid.lua
var runtimePlaceBidLuaV1 string

var runtimeStartLotScriptV1 = redis.NewScript(runtimeStartLotLua)
var runtimeCancelLotScriptV1 = redis.NewScript(runtimeCancelLotLuaV1)
var runtimeCloseIfExpiredScriptV1 = redis.NewScript(runtimeCloseIfExpiredLuaV1)
var runtimeSyncLotConfigScriptV1 = redis.NewScript(runtimeSyncLotConfigLuaV1)
var runtimePlaceBidScriptV1 = redis.NewScript(runtimePlaceBidLuaV1)

func runtimeExpiringKey() string {
	return "auction:runtime:expiring"
}

func runtimeOutboxPendingKey(shard int) string {
	return "auction:runtime:outbox:pending:" + strconv.Itoa(shard)
}

func runtimeIdempotencyHashKey(lotID string) string {
	return runtimeTag(lotID) + ":idempotency"
}

func runtimeIdempotencyField(userID, key string) string {
	return userID + "\x1f" + key
}

func runtimeRoomActiveLotKey(roomID string) string {
	return "auction:room:{" + roomID + "}:active_lot"
}

func runtimeRoomDisplayLotKey(roomID string) string {
	return "auction:room:{" + roomID + "}:display_lot"
}

func runtimeFrozenLotKey(lotID string) string {
	return "auction:runtime:frozen:lot:" + lotID
}
