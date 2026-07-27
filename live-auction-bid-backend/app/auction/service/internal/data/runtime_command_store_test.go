package data

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
	"live-auction-bid/backend/app/auction/service/internal/worker/closeworker"
)

func TestRuntimeCommandStoreUsesAtomicRedisListOutbox(t *testing.T) {
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if redisAddress == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}
	store, err := NewRuntimeCommandStore(client, RuntimeOutboxShardCount)
	if err != nil {
		t.Fatal(err)
	}
	lot := &v1.Lot{
		Id: "lot-command-adapter", RoomId: "room-command-adapter", MainAccountId: "account-command-adapter",
		Title: "production runtime adapter", ImageUrl: "https://example.test/lot.jpg", Status: v1.LotStatus_LOT_STATUS_DRAFT,
		Version: 4, ConfigVersion: 3,
		Rule: &v1.BidRule{
			StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"},
			DurationSeconds: 300, AntiSnipeWindowSeconds: 10, AntiSnipeExtendSeconds: 30, MaxExtendCount: 3,
		},
	}
	shard := store.runtimeOutboxShard(lot.GetId())
	keys := []string{
		runtimeStateKey(lot.GetId()), runtimeRankingKey(lot.GetId()), runtimeRankMetaKey(lot.GetId()), runtimeParticipantsKey(lot.GetId()),
		runtimeRecentKey(lot.GetId()), runtimeIdempotencyHashKey(lot.GetId()), runtimeRoomActiveLotKey(lot.GetRoomId()),
		runtimeOutboxPendingKey(shard), runtimeOutboxInflightKey(shard), runtimePriorityReconcileKey(),
		runtimeFrozenLotKey(lot.GetId()), runtimeRoomDisplayLotKey(lot.GetRoomId()),
	}
	if err := client.Del(ctx, keys...).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, keys...).Err()
		_ = client.ZRem(cleanupCtx, runtimeExpiringKey(), lot.GetId()).Err()
	})
	missingRequest := &v1.PlaceBidRequest{LotId: lot.GetId(), Amount: &v1.Money{Amount: 10_100, Currency: "CNY"}, IdempotencyKey: "missing-runtime"}
	_, err = store.PlaceBidRuntime(ctx, lot, missingRequest, "buyer-1", "测试买家", "", "missing-bid", time.Now().UnixMilli())
	var missingReject *auction.RuntimeBidRejectError
	if !errors.As(err, &missingReject) || missingReject.Code != string(apperr.CodeProjectionPending) {
		t.Fatalf("missing runtime bid error=%v reject=%+v", err, missingReject)
	}

	startFact, err := store.executeStartLotRedis(ctx, lot, "trace-start-adapter")
	if err != nil {
		t.Fatalf("ExecuteStartLot: %v", err)
	}
	if startFact.GetPrevLotVersion() != 4 || startFact.GetLotVersion() != 5 || startFact.GetConfigVersion() != 3 {
		t.Fatalf("start fact=%+v", startFact)
	}
	if due, err := store.ScanDueRuntimeLotIDs(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("future runtime lot appeared due: lots=%v error=%v", due, err)
	}
	if err := client.ZAdd(ctx, runtimeExpiringKey(), redis.Z{Score: 1, Member: lot.GetId()}).Err(); err != nil {
		t.Fatal(err)
	}
	if due, err := store.ScanDueRuntimeLotIDs(ctx, 10); err != nil || len(due) != 1 || due[0] != lot.GetId() {
		t.Fatalf("due runtime scan lots=%v error=%v", due, err)
	}
	closer, err := closeworker.New(store, closeworker.Config{
		Interval: time.Second, BatchLimit: 10, Concurrency: 2, OperationTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	closeSummary, err := closer.RunOnce(ctx)
	if err != nil || closeSummary.NotExpired != 1 || closeSummary.Settled != 0 || closeSummary.Failed != 0 {
		t.Fatalf("early close candidate summary=%+v error=%v", closeSummary, err)
	}
	if status, err := client.HGet(ctx, runtimeStateKey(lot.GetId()), "status").Int(); err != nil || status != int(v1.LotStatus_LOT_STATUS_LIVE) {
		t.Fatalf("candidate scan closed a live lot: status=%d error=%v", status, err)
	}

	request := &v1.PlaceBidRequest{LotId: lot.GetId(), Amount: &v1.Money{Amount: 10_100, Currency: "CNY"}, IdempotencyKey: "adapter-idempotency"}
	requestContext := requestctx.WithRequestContext(ctx, requestctx.RequestContext{TraceID: "trace-bid-adapter"})
	result, err := store.PlaceBidRuntime(requestContext, &v1.Lot{Id: lot.GetId()}, request, "buyer-1", "测试买家", "", "bid-adapter", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("PlaceBidRuntime: %v", err)
	}
	if result.RuntimeEventID == "" || result.LotVersion != 6 || result.Bid.GetId() != "bid-adapter" {
		t.Fatalf("runtime result=%+v", result)
	}
	replayed, err := store.PlaceBidRuntime(requestContext, &v1.Lot{Id: lot.GetId()}, request, "buyer-1", "测试买家", "", "different-bid", time.Now().UnixMilli())
	if err != nil || !replayed.Replayed || replayed.RuntimeEventID != result.RuntimeEventID || replayed.Bid.GetId() != result.Bid.GetId() {
		t.Fatalf("replayed result=%+v error=%v", replayed, err)
	}

	cancelFact, err := store.ExecuteCancelLot(ctx, lot.GetId(), "operator cancelled", "operator-1", "trace-cancel-adapter")
	if err != nil {
		t.Fatalf("ExecuteCancelLot: %v", err)
	}
	if cancelFact.GetLotVersion() != 7 || cancelFact.GetStateAfter().GetStatus() != v1.LotStatus_LOT_STATUS_CANCELLED {
		t.Fatalf("cancel fact=%+v", cancelFact)
	}
	if displayed, found, err := store.DisplayedRuntimeLotID(ctx, lot.GetRoomId()); err != nil || !found || displayed != lot.GetId() {
		t.Fatalf("cancelled lot display pointer=%q found=%t error=%v", displayed, found, err)
	}
	if pending, err := client.LLen(ctx, runtimeOutboxPendingKey(shard)).Result(); err != nil || pending != 3 {
		t.Fatalf("pending=%d error=%v", pending, err)
	}
	if marked, err := client.SIsMember(ctx, runtimePriorityReconcileKey(), lot.GetId()).Result(); err != nil || !marked {
		t.Fatalf("priority reconcile marked=%t error=%v", marked, err)
	}
}

func TestRuntimeConfigFromLotRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := NewRuntimeCommandStore(nil, RuntimeOutboxShardCount); err == nil {
		t.Fatal("nil Redis client was accepted")
	}
	if _, err := runtimeConfigFromLot(nil); err == nil {
		t.Fatal("nil lot was accepted")
	}
	lot := &v1.Lot{Id: "lot", Rule: &v1.BidRule{StartPrice: &v1.Money{Amount: 1, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 1, Currency: "CNY"}}}
	if _, err := runtimeConfigFromLot(lot); err == nil {
		t.Fatal("zero duration was accepted")
	}
}

func TestRedisOnlyRuntimeCommandStoreCannotStartLot(t *testing.T) {
	store := &RuntimeCommandStore{Store: &Store{}}
	if _, err := store.ExecuteStartLot(context.Background(), &v1.Lot{Id: "lot-1"}, "trace"); err == nil || !strings.Contains(err.Error(), "MySQL-backed") {
		t.Fatalf("Redis-only worker start error=%v", err)
	}
}
