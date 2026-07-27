package data

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestRuntimeOutboxLuaContractsContainFencingAndNoJSONAck(t *testing.T) {
	checks := []struct {
		name     string
		source   string
		required []string
	}{
		{name: "acquire", source: runtimeOutboxAcquireOwnerLua, required: []string{"redis.call('INCR', epoch_key)", "instance_id .. ':' .. tostring(epoch)", "'PX', ttl_ms"}},
		{name: "renew", source: runtimeOutboxRenewOwnerLua, required: []string{"redis.call('GET', owner_key) ~= expected_owner", "redis.call('PEXPIRE', owner_key, ttl_ms)"}},
		{name: "release", source: runtimeOutboxReleaseOwnerLua, required: []string{"redis.call('GET', owner_key) ~= expected_owner", "redis.call('DEL', owner_key)"}},
		{name: "peek", source: runtimeOutboxPeekInflightLua, required: []string{"redis.error_reply('NOT_OWNER')", "redis.call('LINDEX', inflight_key, -1)"}},
		{name: "take", source: runtimeOutboxTakeLua, required: []string{"redis.error_reply('NOT_OWNER')", "redis.error_reply('INFLIGHT_NOT_EMPTY')", "redis.call('LMOVE', pending_key, inflight_key, 'RIGHT', 'LEFT')"}},
		{name: "ack", source: runtimeOutboxAckLua, required: []string{"string.find(item, '\\n', 1, true)", "string.sub(item, 1, newline - 1) ~= expected_event_id", "redis.call('RPOP', inflight_key)"}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if strings.TrimSpace(check.source) == "" {
				t.Fatal("embedded Lua source is empty")
			}
			for _, required := range check.required {
				if !strings.Contains(check.source, required) {
					t.Fatalf("Lua source is missing %q", required)
				}
			}
		})
	}
	if strings.Contains(runtimeOutboxAckLua, "cjson") {
		t.Fatal("ACK deletion path must not decode or encode JSON")
	}
}

func TestRuntimeOutboxQueueFencesTakeoverAndPreservesFIFO(t *testing.T) {
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
	queue := &RuntimeOutboxQueue{redis: client}
	const shard = 15
	keys := []string{runtimeOutboxPendingKey(shard), runtimeOutboxInflightKey(shard), runtimeOutboxOwnerKey(shard), runtimeOutboxEpochKey(shard)}
	if err := client.Del(ctx, keys...).Err(); err != nil {
		t.Fatalf("clear dedicated outbox test keys: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, keys...).Err()
	})

	firstEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	secondEventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	firstItem := firstEventID + "\n{\"event_id\":\"" + firstEventID + "\"}"
	secondItem := secondEventID + "\n{\"event_id\":\"" + secondEventID + "\"}"
	if err := client.LPush(ctx, keys[0], firstItem, secondItem).Err(); err != nil {
		t.Fatalf("seed pending outbox: %v", err)
	}
	if stats, err := queue.Stats(ctx, shard); err != nil || stats.Pending != 2 || stats.Inflight != 0 || stats.OldestItem != firstItem {
		t.Fatalf("seeded queue stats=%+v error=%v", stats, err)
	}

	ownerA, acquired, err := queue.Acquire(ctx, shard, "relay-a", 5*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire owner A: acquired=%v error=%v", acquired, err)
	}
	if ownerA.Epoch != 1 || ownerA.OwnerToken != "relay-a:1" {
		t.Fatalf("owner A mismatch: %+v", ownerA)
	}
	if ttl, err := client.PTTL(ctx, keys[2]).Result(); err != nil || ttl <= 0 || ttl > ownerA.TTL {
		t.Fatalf("owner TTL=%s error=%v", ttl, err)
	}
	if _, acquired, err := queue.Acquire(ctx, shard, "relay-b", 5*time.Second); err != nil || acquired {
		t.Fatalf("owner B acquired occupied shard: acquired=%v error=%v", acquired, err)
	}

	item, found, err := queue.Take(ctx, ownerA)
	if err != nil || !found || item != firstItem {
		t.Fatalf("FIFO take item=%q found=%v error=%v", item, found, err)
	}
	if _, _, err := queue.Take(ctx, ownerA); !errors.Is(err, ErrRuntimeOutboxInflightNotEmpty) {
		t.Fatalf("second take error=%v want inflight-not-empty", err)
	}
	if stats, err := queue.Stats(ctx, shard); err != nil || stats.Pending != 1 || stats.Inflight != 1 || stats.OldestItem != firstItem {
		t.Fatalf("inflight queue stats=%+v error=%v", stats, err)
	}
	if peeked, found, err := queue.PeekInflight(ctx, ownerA); err != nil || !found || peeked != firstItem {
		t.Fatalf("peek item=%q found=%v error=%v", peeked, found, err)
	}
	if result, err := queue.Ack(ctx, ownerA, secondEventID); err != nil || result != RuntimeOutboxAckMismatch {
		t.Fatalf("mismatched ack result=%q error=%v", result, err)
	}

	released, err := queue.Release(ctx, ownerA)
	if err != nil || !released {
		t.Fatalf("release owner A: released=%v error=%v", released, err)
	}
	ownerB, acquired, err := queue.Acquire(ctx, shard, "relay-b", 5*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire owner B: acquired=%v error=%v", acquired, err)
	}
	if ownerB.Epoch != ownerA.Epoch+1 || ownerB.OwnerToken != "relay-b:2" {
		t.Fatalf("owner B fencing epoch mismatch: %+v", ownerB)
	}
	if renewed, err := queue.Renew(ctx, ownerB); err != nil || !renewed {
		t.Fatalf("renew owner B: renewed=%v error=%v", renewed, err)
	}
	if _, _, err := queue.PeekInflight(ctx, ownerA); !errors.Is(err, ErrRuntimeOutboxNotOwner) {
		t.Fatalf("stale owner peek error=%v want not-owner", err)
	}
	if released, err := queue.Release(ctx, ownerA); err != nil || released {
		t.Fatalf("stale owner release: released=%v error=%v", released, err)
	}
	if result, err := queue.Ack(ctx, ownerA, firstEventID); err != nil || result != RuntimeOutboxAckNotOwner {
		t.Fatalf("stale owner ack result=%q error=%v", result, err)
	}
	if item, found, err := queue.PeekInflight(ctx, ownerB); err != nil || !found || item != firstItem {
		t.Fatalf("takeover did not drain inflight first: item=%q found=%v error=%v", item, found, err)
	}
	if result, err := queue.Ack(ctx, ownerB, firstEventID); err != nil || result != RuntimeOutboxAckOK {
		t.Fatalf("owner B ack first result=%q error=%v", result, err)
	}
	item, found, err = queue.Take(ctx, ownerB)
	if err != nil || !found || item != secondItem {
		t.Fatalf("second FIFO take item=%q found=%v error=%v", item, found, err)
	}
	if result, err := queue.Ack(ctx, ownerB, secondEventID); err != nil || result != RuntimeOutboxAckOK {
		t.Fatalf("owner B ack second result=%q error=%v", result, err)
	}
	if result, err := queue.Ack(ctx, ownerB, secondEventID); err != nil || result != RuntimeOutboxAckEmpty {
		t.Fatalf("empty ack result=%q error=%v", result, err)
	}

	if err := client.LPush(ctx, keys[1], "malformed-without-newline").Err(); err != nil {
		t.Fatal(err)
	}
	if result, err := queue.Ack(ctx, ownerB, secondEventID); err != nil || result != RuntimeOutboxAckMalformed {
		t.Fatalf("malformed ack result=%q error=%v", result, err)
	}
	if length, err := client.LLen(ctx, keys[1]).Result(); err != nil || length != 1 {
		t.Fatalf("malformed ACK removed inflight item: length=%d error=%v", length, err)
	}
}

func TestRuntimeOutboxQueueRejectsInvalidLocalArguments(t *testing.T) {
	queue := NewRuntimeOutboxQueue(nil)
	if _, _, err := queue.Acquire(context.Background(), 0, "relay", time.Second); err == nil {
		t.Fatal("uninitialized queue acquisition must fail")
	}
	queue = &RuntimeOutboxQueue{redis: redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})}
	t.Cleanup(func() { _ = queue.redis.Close() })
	if _, _, err := queue.Acquire(context.Background(), -1, "relay", time.Second); !errors.Is(err, ErrRuntimeOutboxInvalidArgument) {
		t.Fatalf("invalid shard error=%v", err)
	}
	if _, _, err := queue.Acquire(context.Background(), 0, "relay:bad", time.Second); !errors.Is(err, ErrRuntimeOutboxInvalidArgument) {
		t.Fatalf("invalid instance error=%v", err)
	}
	invalidOwnership := RuntimeOutboxOwnership{Shard: 0, InstanceID: "relay", Epoch: 1, OwnerToken: "wrong", TTL: time.Second}
	if _, err := queue.Renew(context.Background(), invalidOwnership); !errors.Is(err, ErrRuntimeOutboxInvalidArgument) {
		t.Fatalf("invalid ownership error=%v", err)
	}
	if _, err := queue.Ack(context.Background(), RuntimeOutboxOwnership{Shard: 0, InstanceID: "relay", Epoch: 1, OwnerToken: "relay:1", TTL: time.Second}, "bad\nevent"); !errors.Is(err, ErrRuntimeOutboxInvalidArgument) {
		t.Fatalf("invalid event id error=%v", err)
	}
}

func TestNewRuntimeOutboxQueueFromRedisRejectsNilClientOnUse(t *testing.T) {
	queue := NewRuntimeOutboxQueueFromRedis(nil)
	if _, _, err := queue.Acquire(context.Background(), 0, "relay-1", time.Second); err == nil {
		t.Fatal("queue with nil Redis client returned no error")
	}
}

func TestRuntimeOutboxResultInt64(t *testing.T) {
	for _, value := range []any{int64(7), "8", []byte("9")} {
		if result, err := runtimeOutboxResultInt64(value); err != nil || result < 7 || result > 9 {
			t.Fatalf("result for %T(%v)=%d error=%v", value, value, result, err)
		}
	}
	if _, err := runtimeOutboxResultInt64(struct{}{}); err == nil {
		t.Fatal("unsupported result type must fail")
	}
}
