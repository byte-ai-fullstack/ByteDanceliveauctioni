package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestIntegrationRuntimeStartSerializesWithDraftPatch(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AUCTION_PROJECTOR_TEST_MYSQL_DSN"))
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if dsn == "" || redisAddress == "" {
		t.Skip("AUCTION_PROJECTOR_TEST_MYSQL_DSN and AUCTION_TEST_REDIS_ADDR are required")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open integration MySQL: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get integration sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("ping integration MySQL: %v", err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping integration Redis: %v", err)
	}

	store := &Store{db: db, redis: client, outboxShards: RuntimeOutboxShardCount, outboxPendingLimit: 10_000}
	runID := time.Now().UnixNano()
	for index := 0; index < 8; index++ {
		lot := runtimeStartRaceLot(fmt.Sprintf("it-start-race-%d-%d", runID, index))
		model, err := lotToModel(lot)
		if err != nil {
			t.Fatalf("lotToModel: %v", err)
		}
		if err := db.WithContext(ctx).Create(model).Error; err != nil {
			t.Fatalf("seed integration lot: %v", err)
		}
		t.Cleanup(func() {
			cleanupRuntimeStartRace(context.Background(), db, client, lot)
		})

		requested := proto.Clone(lot).(*v1.Lot)
		patched := proto.Clone(lot).(*v1.Lot)
		patchedTitle := fmt.Sprintf("patched-%d", index)
		if err := auction.ApplyDraftPatch(patched, &v1.PatchLotDraftRequest{LotId: lot.GetId(), Title: patchedTitle}); err != nil {
			t.Fatalf("prepare concurrent patch: %v", err)
		}

		start := make(chan struct{})
		var wait sync.WaitGroup
		var startResult auction.RuntimeStartResult
		var startErr error
		var patchErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			startResult, startErr = store.ExecuteStartLot(ctx, requested, "trace-start-race")
		}()
		go func() {
			defer wait.Done()
			<-start
			patchErr = store.Save(ctx, patched, lot.GetVersion(), nil)
		}()
		close(start)
		wait.Wait()

		if startErr != nil {
			t.Fatalf("runtime start failed: %v", startErr)
		}
		if startResult.Fact == nil || startResult.SourceLot == nil {
			t.Fatalf("runtime start result incomplete: %+v", startResult)
		}

		var persisted AuctionLotModel
		if err := db.WithContext(ctx).Where("id = ?", lot.GetId()).First(&persisted).Error; err != nil {
			t.Fatalf("read integration lot: %v", err)
		}
		if patchErr == nil {
			if persisted.Version != patched.GetVersion() || persisted.ConfigVersion != patched.GetConfigVersion() || persisted.Title != patchedTitle {
				t.Fatalf("successful patch not persisted before start: %+v", persisted)
			}
			if startResult.Fact.GetPrevLotVersion() != patched.GetVersion() || startResult.Fact.GetConfigVersion() != patched.GetConfigVersion() || startResult.SourceLot.GetTitle() != patchedTitle {
				t.Fatalf("start used stale configuration after successful patch: source=%+v fact=%+v", startResult.SourceLot, startResult.Fact)
			}
		} else {
			if !errors.Is(patchErr, apperr.ErrInvalidArgument) {
				t.Fatalf("start-winning patch error=%v want invalid argument", patchErr)
			}
			if persisted.Version != lot.GetVersion() || persisted.ConfigVersion != lot.GetConfigVersion() {
				t.Fatalf("rejected patch changed MySQL versions: %+v", persisted)
			}
			if startResult.Fact.GetPrevLotVersion() != lot.GetVersion() || startResult.Fact.GetConfigVersion() != lot.GetConfigVersion() {
				t.Fatalf("start fact does not match locked configuration: %+v", startResult.Fact)
			}
		}
	}
}

func TestIntegrationRuntimeStartSerializesWithQueueTransition(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AUCTION_PROJECTOR_TEST_MYSQL_DSN"))
	redisAddress := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if dsn == "" || redisAddress == "" {
		t.Skip("AUCTION_PROJECTOR_TEST_MYSQL_DSN and AUCTION_TEST_REDIS_ADDR are required")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open integration MySQL: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get integration sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	client := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("ping integration MySQL: %v", err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping integration Redis: %v", err)
	}

	store := &Store{db: db, redis: client, outboxShards: RuntimeOutboxShardCount, outboxPendingLimit: 10_000}
	runID := time.Now().UnixNano()
	for index := 0; index < 4; index++ {
		lot := runtimeStartRaceLot(fmt.Sprintf("it-start-queue-race-%d-%d", runID, index))
		model, err := lotToModel(lot)
		if err != nil {
			t.Fatalf("lotToModel: %v", err)
		}
		if err := db.WithContext(ctx).Create(model).Error; err != nil {
			t.Fatalf("seed integration lot: %v", err)
		}
		t.Cleanup(func() {
			cleanupRuntimeStartRace(context.Background(), db, client, lot)
		})

		start := make(chan struct{})
		var wait sync.WaitGroup
		var startResult auction.RuntimeStartResult
		var startErr error
		var queueErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			startResult, startErr = store.ExecuteStartLot(ctx, proto.Clone(lot).(*v1.Lot), "trace-start-queue-race")
		}()
		go func() {
			defer wait.Done()
			<-start
			_, _, _, queueErr = store.QueueLotAsNext(ctx, lot.GetId(), lot.GetMainAccountId(), "", time.Now().UnixMilli())
		}()
		close(start)
		wait.Wait()

		if startErr != nil {
			t.Fatalf("runtime start failed: %v", startErr)
		}
		var persisted AuctionLotModel
		if err := db.WithContext(ctx).Where("id = ?", lot.GetId()).First(&persisted).Error; err != nil {
			t.Fatalf("read integration lot: %v", err)
		}
		if queueErr == nil {
			if persisted.Version != lot.GetVersion()+1 || persisted.Status != int32(v1.LotStatus_LOT_STATUS_QUEUED) {
				t.Fatalf("successful queue transition not persisted before start: %+v", persisted)
			}
			if startResult.Fact.GetPrevLotVersion() != persisted.Version || startResult.SourceLot.GetStatus() != v1.LotStatus_LOT_STATUS_QUEUED {
				t.Fatalf("start did not use queued source version: source=%+v fact=%+v", startResult.SourceLot, startResult.Fact)
			}
		} else {
			if !errors.Is(queueErr, apperr.ErrInvalidArgument) {
				t.Fatalf("start-winning queue error=%v want invalid argument", queueErr)
			}
			if persisted.Version != lot.GetVersion() || persisted.Status != int32(v1.LotStatus_LOT_STATUS_DRAFT) {
				t.Fatalf("rejected queue changed MySQL state: %+v", persisted)
			}
			if startResult.Fact.GetPrevLotVersion() != lot.GetVersion() {
				t.Fatalf("start fact does not match locked draft version: %+v", startResult.Fact)
			}
		}
	}
}

func runtimeStartRaceLot(lotID string) *v1.Lot {
	return &v1.Lot{
		Id: lotID, RoomId: "room-" + lotID, MainAccountId: "main-integration", Title: "start race",
		Description: "integration", ImageUrl: "https://example.test/start-race.jpg",
		Status: v1.LotStatus_LOT_STATUS_DRAFT, QueueStatus: v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE,
		Rule: &v1.BidRule{
			StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, MinIncrement: &v1.Money{Amount: 100, Currency: "CNY"},
			DurationSeconds: 60, AntiSnipeWindowSeconds: 10, AntiSnipeExtendSeconds: 30, MaxExtendCount: 3,
		},
		CurrentPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, FinalPrice: &v1.Money{Currency: "CNY"},
		Version: 1, ConfigVersion: 1, Stock: 1,
	}
}

func cleanupRuntimeStartRace(ctx context.Context, db *gorm.DB, client *redis.Client, lot *v1.Lot) {
	if db != nil && lot != nil {
		_ = db.WithContext(ctx).Exec("DELETE FROM auction_domain_outbox WHERE topic = ? AND partition_key = ?", eventcontract.LotStateTopicV1, lot.GetId()).Error
		_ = db.WithContext(ctx).Where("lot_id = ?", lot.GetId()).Delete(&AuctionEventModel{}).Error
		_ = db.WithContext(ctx).Where("lot_id = ?", lot.GetId()).Delete(&AuctionLotStatsModel{}).Error
		_ = db.WithContext(ctx).Where("lot_id = ?", lot.GetId()).Delete(&AuctionLotPresentationModel{}).Error
		_ = db.WithContext(ctx).Where("id = ?", lot.GetId()).Delete(&AuctionLotModel{}).Error
		_ = db.WithContext(ctx).Where("room_id = ?", lot.GetRoomId()).Delete(&AuctionRoomStateModel{}).Error
	}
	if client == nil || lot == nil {
		return
	}
	shard := runtimeOutboxShard(lot.GetId(), RuntimeOutboxShardCount)
	items, _ := client.LRange(ctx, runtimeOutboxPendingKey(shard), 0, -1).Result()
	for _, item := range items {
		if strings.Contains(item, "\n") && strings.Contains(item, `"lot_id":"`+lot.GetId()+`"`) {
			_ = client.LRem(ctx, runtimeOutboxPendingKey(shard), 0, item).Err()
		}
	}
	_ = client.Del(ctx,
		runtimeStateKey(lot.GetId()), runtimeRankingKey(lot.GetId()), runtimeRankMetaKey(lot.GetId()),
		runtimeParticipantsKey(lot.GetId()), runtimeRecentKey(lot.GetId()), runtimeIdempotencyHashKey(lot.GetId()),
		runtimeRoomActiveLotKey(lot.GetRoomId()), runtimeRoomDisplayLotKey(lot.GetRoomId()), runtimeFrozenLotKey(lot.GetId()),
	).Err()
	_ = client.ZRem(ctx, runtimeExpiringKey(), lot.GetId()).Err()
	_ = client.SRem(ctx, runtimePriorityReconcileKey(), lot.GetId()).Err()
}
