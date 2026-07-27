package data

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const RuntimeOutboxShardCount = 16

var (
	ErrRuntimeOutboxNotOwner         = errors.New("runtime outbox owner fencing rejected")
	ErrRuntimeOutboxInflightNotEmpty = errors.New("runtime outbox inflight is not empty")
	ErrRuntimeOutboxInvalidArgument  = errors.New("invalid runtime outbox argument")
)

//go:embed lua/outbox_acquire_owner.lua
var runtimeOutboxAcquireOwnerLua string

//go:embed lua/outbox_renew_owner.lua
var runtimeOutboxRenewOwnerLua string

//go:embed lua/outbox_release_owner.lua
var runtimeOutboxReleaseOwnerLua string

//go:embed lua/outbox_peek_inflight.lua
var runtimeOutboxPeekInflightLua string

//go:embed lua/outbox_take.lua
var runtimeOutboxTakeLua string

//go:embed lua/outbox_ack.lua
var runtimeOutboxAckLua string

var (
	runtimeOutboxAcquireOwnerScript = redis.NewScript(runtimeOutboxAcquireOwnerLua)
	runtimeOutboxRenewOwnerScript   = redis.NewScript(runtimeOutboxRenewOwnerLua)
	runtimeOutboxReleaseOwnerScript = redis.NewScript(runtimeOutboxReleaseOwnerLua)
	runtimeOutboxPeekInflightScript = redis.NewScript(runtimeOutboxPeekInflightLua)
	runtimeOutboxTakeScript         = redis.NewScript(runtimeOutboxTakeLua)
	runtimeOutboxAckScript          = redis.NewScript(runtimeOutboxAckLua)
	runtimeOutboxStatsScript        = redis.NewScript(`
local pending = redis.call('LLEN', KEYS[1])
local inflight = redis.call('LLEN', KEYS[2])
local oldest = ''
if inflight > 0 then
  oldest = redis.call('LINDEX', KEYS[2], 0) or ''
elseif pending > 0 then
  oldest = redis.call('LINDEX', KEYS[1], -1) or ''
end
return {pending, inflight, oldest}
`)
)

type RuntimeOutboxOwnership struct {
	Shard      int
	InstanceID string
	Epoch      int64
	OwnerToken string
	TTL        time.Duration
}

type RuntimeOutboxAckResult string

type RuntimeOutboxStats struct {
	Pending    int64
	Inflight   int64
	OldestItem string
}

const (
	RuntimeOutboxAckOK        RuntimeOutboxAckResult = "OK"
	RuntimeOutboxAckNotOwner  RuntimeOutboxAckResult = "NOT_OWNER"
	RuntimeOutboxAckEmpty     RuntimeOutboxAckResult = "EMPTY"
	RuntimeOutboxAckMalformed RuntimeOutboxAckResult = "MALFORMED"
	RuntimeOutboxAckMismatch  RuntimeOutboxAckResult = "MISMATCH"
)

type RuntimeOutboxQueue struct {
	redis redis.UniversalClient
}

func NewRuntimeOutboxQueue(store *Store) *RuntimeOutboxQueue {
	if store == nil {
		return &RuntimeOutboxQueue{}
	}
	return NewRuntimeOutboxQueueFromRedis(store.redis)
}

// NewRuntimeOutboxQueueFromRedis creates the Relay queue without requiring a MySQL-backed Store.
func NewRuntimeOutboxQueueFromRedis(client redis.UniversalClient) *RuntimeOutboxQueue {
	return &RuntimeOutboxQueue{redis: client}
}

func (q *RuntimeOutboxQueue) Acquire(ctx context.Context, shard int, instanceID string, ttl time.Duration) (RuntimeOutboxOwnership, bool, error) {
	if err := q.validate(shard); err != nil {
		return RuntimeOutboxOwnership{}, false, err
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" || strings.ContainsAny(instanceID, ":\r\n") || ttl <= 0 || ttl.Milliseconds() <= 0 {
		return RuntimeOutboxOwnership{}, false, ErrRuntimeOutboxInvalidArgument
	}
	result, err := runtimeOutboxAcquireOwnerScript.Run(ctx, q.redis,
		[]string{runtimeOutboxOwnerKey(shard), runtimeOutboxEpochKey(shard)},
		instanceID, ttl.Milliseconds(),
	).Slice()
	if err != nil {
		return RuntimeOutboxOwnership{}, false, mapRuntimeOutboxScriptError(err)
	}
	if len(result) != 3 {
		return RuntimeOutboxOwnership{}, false, fmt.Errorf("acquire runtime outbox shard %d: unexpected result length %d", shard, len(result))
	}
	acquired, err := runtimeOutboxResultInt64(result[0])
	if err != nil {
		return RuntimeOutboxOwnership{}, false, fmt.Errorf("acquire runtime outbox shard %d: %w", shard, err)
	}
	if acquired == 0 {
		return RuntimeOutboxOwnership{}, false, nil
	}
	epoch, err := runtimeOutboxResultInt64(result[1])
	if err != nil {
		return RuntimeOutboxOwnership{}, false, fmt.Errorf("acquire runtime outbox shard %d epoch: %w", shard, err)
	}
	if epoch <= 0 {
		return RuntimeOutboxOwnership{}, false, fmt.Errorf("acquire runtime outbox shard %d: invalid epoch %d", shard, epoch)
	}
	ownerToken := fmt.Sprint(result[2])
	expectedToken := instanceID + ":" + strconv.FormatInt(epoch, 10)
	if ownerToken != expectedToken {
		return RuntimeOutboxOwnership{}, false, fmt.Errorf("acquire runtime outbox shard %d: owner token mismatch", shard)
	}
	return RuntimeOutboxOwnership{Shard: shard, InstanceID: instanceID, Epoch: epoch, OwnerToken: ownerToken, TTL: ttl}, true, nil
}

func (q *RuntimeOutboxQueue) Renew(ctx context.Context, ownership RuntimeOutboxOwnership) (bool, error) {
	if err := q.validateOwnership(ownership); err != nil {
		return false, err
	}
	result, err := runtimeOutboxRenewOwnerScript.Run(ctx, q.redis,
		[]string{runtimeOutboxOwnerKey(ownership.Shard)}, ownership.OwnerToken, ownership.TTL.Milliseconds(),
	).Int()
	if err != nil {
		return false, mapRuntimeOutboxScriptError(err)
	}
	return result == 1, nil
}

func (q *RuntimeOutboxQueue) Release(ctx context.Context, ownership RuntimeOutboxOwnership) (bool, error) {
	if err := q.validateOwnership(ownership); err != nil {
		return false, err
	}
	result, err := runtimeOutboxReleaseOwnerScript.Run(ctx, q.redis,
		[]string{runtimeOutboxOwnerKey(ownership.Shard)}, ownership.OwnerToken,
	).Int()
	if err != nil {
		return false, mapRuntimeOutboxScriptError(err)
	}
	return result == 1, nil
}

func (q *RuntimeOutboxQueue) PeekInflight(ctx context.Context, ownership RuntimeOutboxOwnership) (string, bool, error) {
	if err := q.validateOwnership(ownership); err != nil {
		return "", false, err
	}
	item, err := runtimeOutboxPeekInflightScript.Run(ctx, q.redis,
		[]string{runtimeOutboxOwnerKey(ownership.Shard), runtimeOutboxInflightKey(ownership.Shard)}, ownership.OwnerToken,
	).Text()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapRuntimeOutboxScriptError(err)
	}
	return item, true, nil
}

func (q *RuntimeOutboxQueue) Take(ctx context.Context, ownership RuntimeOutboxOwnership) (string, bool, error) {
	if err := q.validateOwnership(ownership); err != nil {
		return "", false, err
	}
	item, err := runtimeOutboxTakeScript.Run(ctx, q.redis,
		[]string{runtimeOutboxOwnerKey(ownership.Shard), runtimeOutboxPendingKey(ownership.Shard), runtimeOutboxInflightKey(ownership.Shard)}, ownership.OwnerToken,
	).Text()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapRuntimeOutboxScriptError(err)
	}
	return item, true, nil
}

func (q *RuntimeOutboxQueue) Ack(ctx context.Context, ownership RuntimeOutboxOwnership, eventID string) (RuntimeOutboxAckResult, error) {
	if err := q.validateOwnership(ownership); err != nil {
		return "", err
	}
	if strings.TrimSpace(eventID) == "" || strings.ContainsAny(eventID, "\r\n") {
		return "", ErrRuntimeOutboxInvalidArgument
	}
	result, err := runtimeOutboxAckScript.Run(ctx, q.redis,
		[]string{runtimeOutboxOwnerKey(ownership.Shard), runtimeOutboxInflightKey(ownership.Shard)}, ownership.OwnerToken, eventID,
	).Text()
	if err != nil {
		return "", mapRuntimeOutboxScriptError(err)
	}
	ackResult := RuntimeOutboxAckResult(result)
	switch ackResult {
	case RuntimeOutboxAckOK, RuntimeOutboxAckNotOwner, RuntimeOutboxAckEmpty, RuntimeOutboxAckMalformed, RuntimeOutboxAckMismatch:
		return ackResult, nil
	default:
		return "", fmt.Errorf("ack runtime outbox shard %d: unknown result %q", ownership.Shard, result)
	}
}

func (q *RuntimeOutboxQueue) Stats(ctx context.Context, shard int) (RuntimeOutboxStats, error) {
	if err := q.validate(shard); err != nil {
		return RuntimeOutboxStats{}, err
	}
	values, err := runtimeOutboxStatsScript.Run(ctx, q.redis,
		[]string{runtimeOutboxPendingKey(shard), runtimeOutboxInflightKey(shard)},
	).Slice()
	if err != nil {
		return RuntimeOutboxStats{}, fmt.Errorf("read runtime outbox shard %d stats: %w", shard, err)
	}
	if len(values) != 3 {
		return RuntimeOutboxStats{}, fmt.Errorf("read runtime outbox shard %d stats: unexpected result length %d", shard, len(values))
	}
	pending, err := runtimeOutboxResultInt64(values[0])
	if err != nil {
		return RuntimeOutboxStats{}, fmt.Errorf("read runtime outbox shard %d pending: %w", shard, err)
	}
	inflight, err := runtimeOutboxResultInt64(values[1])
	if err != nil {
		return RuntimeOutboxStats{}, fmt.Errorf("read runtime outbox shard %d inflight: %w", shard, err)
	}
	return RuntimeOutboxStats{Pending: pending, Inflight: inflight, OldestItem: fmt.Sprint(values[2])}, nil
}

func (q *RuntimeOutboxQueue) validate(shard int) error {
	if q == nil || q.redis == nil {
		return errors.New("runtime outbox queue is not initialized")
	}
	if shard < 0 || shard >= RuntimeOutboxShardCount {
		return fmt.Errorf("%w: shard %d is outside [0,%d)", ErrRuntimeOutboxInvalidArgument, shard, RuntimeOutboxShardCount)
	}
	return nil
}

func (q *RuntimeOutboxQueue) validateOwnership(ownership RuntimeOutboxOwnership) error {
	if err := q.validate(ownership.Shard); err != nil {
		return err
	}
	if strings.TrimSpace(ownership.InstanceID) == "" || strings.ContainsAny(ownership.InstanceID, ":\r\n") || ownership.Epoch <= 0 || ownership.OwnerToken != ownership.InstanceID+":"+strconv.FormatInt(ownership.Epoch, 10) || ownership.TTL <= 0 || ownership.TTL.Milliseconds() <= 0 {
		return ErrRuntimeOutboxInvalidArgument
	}
	return nil
}

func mapRuntimeOutboxScriptError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(err.Error())
	message = strings.TrimPrefix(message, "ERR ")
	switch message {
	case "NOT_OWNER":
		return ErrRuntimeOutboxNotOwner
	case "INFLIGHT_NOT_EMPTY":
		return ErrRuntimeOutboxInflightNotEmpty
	case "INVALID_ARGUMENT":
		return ErrRuntimeOutboxInvalidArgument
	default:
		return err
	}
}

func runtimeOutboxResultInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("expected integer result, got %T", value)
	}
}

func runtimeOutboxInflightKey(shard int) string {
	return "auction:runtime:outbox:inflight:" + strconv.Itoa(shard)
}

func runtimeOutboxOwnerKey(shard int) string {
	return "auction:runtime:outbox:owner:" + strconv.Itoa(shard)
}

func runtimeOutboxEpochKey(shard int) string {
	return "auction:runtime:outbox:epoch:" + strconv.Itoa(shard)
}
