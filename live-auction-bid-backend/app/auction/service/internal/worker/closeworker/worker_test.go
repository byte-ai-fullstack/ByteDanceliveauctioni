package closeworker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
)

func TestWorkerRunOnceClassifiesIdempotentLuaOutcomes(t *testing.T) {
	store := &fakeRuntimeStore{
		candidates: []string{"settled", "failed", "extended", "duplicate"},
		outcomes: map[string]fakeOutcome{
			"settled":   {status: v1.LotStatus_LOT_STATUS_SETTLED},
			"failed":    {status: v1.LotStatus_LOT_STATUS_FAILED},
			"extended":  {err: &auction.RuntimeDecisionError{Code: auction.RuntimeCodeNotExpired}},
			"duplicate": {err: &auction.RuntimeDecisionError{Code: auction.RuntimeCodeNotLive}},
		},
		delay: 5 * time.Millisecond,
	}
	worker := newTestWorker(t, store, Config{Interval: time.Second, BatchLimit: 10, Concurrency: 2, OperationTimeout: time.Second})

	summary, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Candidates != 4 || summary.Settled != 1 || summary.Failed != 1 || summary.NotExpired != 1 || summary.Duplicates != 1 || summary.Errors != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if !worker.Ready() || !worker.Stats().Ready || store.maxActive > 2 {
		t.Fatalf("ready=%t stats=%+v max_active=%d", worker.Ready(), worker.Stats(), store.maxActive)
	}
	firstOrders := store.orderSnapshot()
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondOrders := store.orderSnapshot()
	for lotID, first := range firstOrders {
		if first == "" || secondOrders[lotID] != first {
			t.Fatalf("order ID changed for %s: first=%q second=%q", lotID, first, secondOrders[lotID])
		}
	}
}

func TestWorkerFailureChangesReadinessAndPreservesBoundedError(t *testing.T) {
	store := &fakeRuntimeStore{scanErr: errors.New("redis unavailable")}
	worker := newTestWorker(t, store, Config{Interval: time.Second, BatchLimit: 10, Concurrency: 1, OperationTimeout: time.Second})
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("scan failure was ignored")
	}
	stats := worker.Stats()
	if worker.Ready() || stats.Ready || stats.ConsecutiveFail != 1 || stats.LastError == "" || len(stats.LastError) > 512 {
		t.Fatalf("stats=%+v", stats)
	}
	store.scanErr = nil
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !worker.Ready() || worker.Stats().ConsecutiveFail != 0 {
		t.Fatalf("worker did not recover: %+v", worker.Stats())
	}
}

func TestWorkerValidatesConfigurationAndDelegatesPing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	valid := Config{Interval: time.Second, BatchLimit: 10, Concurrency: 1, OperationTimeout: time.Second}
	if _, err := New(nil, valid, logger); err == nil {
		t.Fatal("nil store was accepted")
	}
	if _, err := New(&fakeRuntimeStore{}, valid, nil); err == nil {
		t.Fatal("nil logger was accepted")
	}
	for _, config := range []Config{
		{BatchLimit: 10, Concurrency: 1, OperationTimeout: time.Second},
		{Interval: time.Second, BatchLimit: 1001, Concurrency: 1, OperationTimeout: time.Second},
		{Interval: time.Second, BatchLimit: 10, Concurrency: 0, OperationTimeout: time.Second},
		{Interval: time.Second, BatchLimit: 10, Concurrency: 1},
	} {
		if _, err := New(&fakeRuntimeStore{}, config, logger); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
	store := &fakeRuntimeStore{pingErr: errors.New("ping failed")}
	worker := newTestWorker(t, store, valid)
	if err := worker.Ping(context.Background()); !errors.Is(err, store.pingErr) {
		t.Fatalf("Ping error=%v", err)
	}
	store.pingErr = nil
	if err := worker.Ping(context.Background()); err != nil {
		t.Fatalf("Ping success error=%v", err)
	}
	var nilWorker *Worker
	if err := nilWorker.Ping(context.Background()); err == nil {
		t.Fatal("nil worker Ping succeeded")
	}
	if err := nilWorker.Run(context.Background()); err == nil {
		t.Fatal("nil worker Run succeeded")
	}
}

func TestWorkerRunStopsWithContext(t *testing.T) {
	worker := newTestWorker(t, &fakeRuntimeStore{}, Config{Interval: 10 * time.Millisecond, BatchLimit: 10, Concurrency: 1, OperationTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.After(time.Second)
	for !worker.Ready() {
		select {
		case <-deadline:
			t.Fatal("worker did not complete its initial scan")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if worker.Ready() {
		t.Fatal("stopped worker remained ready")
	}
}

func TestWorkerRejectsCorruptOrMissingRuntimeFacts(t *testing.T) {
	store := &fakeRuntimeStore{
		candidates: []string{"missing", "incomplete", "unexpected"},
		outcomes: map[string]fakeOutcome{
			"missing":    {err: &auction.RuntimeDecisionError{Code: auction.RuntimeCodeStateMissing}},
			"incomplete": {nilFact: true},
			"unexpected": {status: v1.LotStatus_LOT_STATUS_LIVE},
		},
	}
	worker := newTestWorker(t, store, Config{Interval: time.Second, BatchLimit: 10, Concurrency: 3, OperationTimeout: time.Second})
	summary, err := worker.RunOnce(context.Background())
	if err == nil || summary.Errors != 3 || worker.Ready() {
		t.Fatalf("summary=%+v ready=%t error=%v", summary, worker.Ready(), err)
	}
}

func newTestWorker(t *testing.T, store RuntimeStore, config Config) *Worker {
	t.Helper()
	worker, err := New(store, config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

type fakeOutcome struct {
	status  v1.LotStatus
	err     error
	nilFact bool
}

type fakeRuntimeStore struct {
	mu         sync.Mutex
	candidates []string
	outcomes   map[string]fakeOutcome
	orders     map[string]string
	delay      time.Duration
	scanErr    error
	pingErr    error
	active     int
	maxActive  int
}

func (store *fakeRuntimeStore) ScanDueRuntimeLotIDs(context.Context, int64) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.scanErr != nil {
		return nil, store.scanErr
	}
	return append([]string(nil), store.candidates...), nil
}

func (store *fakeRuntimeStore) ExecuteCloseIfExpired(ctx context.Context, lotID, orderID, _ string) (*v1.RuntimeFactV1, error) {
	store.mu.Lock()
	store.active++
	if store.active > store.maxActive {
		store.maxActive = store.active
	}
	if store.orders == nil {
		store.orders = make(map[string]string)
	}
	store.orders[lotID] = orderID
	outcome := store.outcomes[lotID]
	delay := store.delay
	store.mu.Unlock()
	defer func() {
		store.mu.Lock()
		store.active--
		store.mu.Unlock()
	}()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	if outcome.err != nil {
		return nil, outcome.err
	}
	if outcome.nilFact {
		return nil, nil
	}
	return &v1.RuntimeFactV1{LotId: lotID, StateAfter: &v1.LotRuntimeStateV1{Status: outcome.status}}, nil
}

func (store *fakeRuntimeStore) PingRuntime(context.Context) error {
	return store.pingErr
}

func (store *fakeRuntimeStore) orderSnapshot() map[string]string {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make(map[string]string, len(store.orders))
	for lotID, orderID := range store.orders {
		result[lotID] = orderID
	}
	return result
}
