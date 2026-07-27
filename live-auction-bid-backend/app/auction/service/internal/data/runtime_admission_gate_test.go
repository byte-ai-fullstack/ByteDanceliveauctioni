package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

type runtimeAdmissionGateStub struct {
	err   error
	calls int
}

func (gate *runtimeAdmissionGateStub) Check(context.Context) error {
	gate.calls++
	return gate.err
}

func TestRuntimeAdmissionGateRejectsOnlyRiskIncreasingCommandsBeforeIO(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:0",
		DialTimeout: time.Millisecond,
		ReadTimeout: time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	store := &Store{
		db:           &gorm.DB{},
		redis:        client,
		outboxShards: RuntimeOutboxShardCount,
	}
	gate := &runtimeAdmissionGateStub{err: errors.New("projection unhealthy")}
	if err := store.SetRuntimeAdmissionGate(gate); err != nil {
		t.Fatalf("SetRuntimeAdmissionGate() error = %v", err)
	}

	_, startErr := store.ExecuteStartLot(context.Background(), &v1.Lot{
		Id: "lot-1", MainAccountId: "account-1",
	}, "trace-start")
	if !errors.Is(startErr, apperr.ErrOverloaded) {
		t.Fatalf("ExecuteStartLot() error = %v, want OVERLOADED", startErr)
	}
	_, _, bidErr := store.ExecutePlaceBid(
		context.Background(),
		"lot-1",
		&v1.PlaceBidRequest{Amount: &v1.Money{Amount: 100, Currency: "CNY"}},
		"buyer-1", "buyer", "", "bid-1", "order-1", "trace-bid",
	)
	if !errors.Is(bidErr, apperr.ErrOverloaded) {
		t.Fatalf("ExecutePlaceBid() error = %v, want OVERLOADED", bidErr)
	}
	_, syncErr := store.ExecuteSyncLotConfig(context.Background(), &v1.Lot{Id: "lot-1"}, 1, "trace-sync")
	if !errors.Is(syncErr, apperr.ErrOverloaded) {
		t.Fatalf("ExecuteSyncLotConfig() error = %v, want OVERLOADED", syncErr)
	}
	if gate.calls != 3 {
		t.Fatalf("admission gate calls = %d, want 3", gate.calls)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = store.ExecuteCancelLot(canceled, "lot-1", "operator cancel", "operator-1", "trace-cancel")
	_, _ = store.ExecuteCloseIfExpired(canceled, "lot-1", "order-1", "trace-close")
	if gate.calls != 3 {
		t.Fatalf("cancel or close unexpectedly called admission gate; calls = %d", gate.calls)
	}
}

func TestRuntimeAdmissionGateBindingAndContext(t *testing.T) {
	t.Parallel()

	if err := (*Store)(nil).SetRuntimeAdmissionGate(&runtimeAdmissionGateStub{}); err == nil {
		t.Fatal("nil Store accepted an admission gate")
	}
	store := &Store{}
	if err := store.SetRuntimeAdmissionGate(nil); err == nil {
		t.Fatal("nil admission gate was accepted")
	}
	if err := store.checkRuntimeAdmission(context.Background()); err != nil {
		t.Fatalf("disabled admission gate error = %v", err)
	}
	gate := &runtimeAdmissionGateStub{err: errors.New("closed")}
	if err := store.SetRuntimeAdmissionGate(gate); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.checkRuntimeAdmission(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission error = %v, want context.Canceled", err)
	}
}

func TestProjectionGateDBRejectsUninitializedStore(t *testing.T) {
	t.Parallel()

	if _, err := (*Store)(nil).ProjectionGateDB(); err == nil {
		t.Fatal("nil Store returned a projection gate DB")
	}
	if _, err := (&Store{}).ProjectionGateDB(); err == nil {
		t.Fatal("uninitialized Store returned a projection gate DB")
	}
}
