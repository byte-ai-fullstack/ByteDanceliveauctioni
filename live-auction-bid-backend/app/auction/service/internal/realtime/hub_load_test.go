package realtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const wsLoadConnectionsEnv = "AUCTION_WS_LOAD_CONNECTIONS"

type loadSnapshotProvider struct {
	snapshot *v1.RoomSnapshot
}

func (provider loadSnapshotProvider) Snapshot(context.Context, string) (*v1.RoomSnapshot, error) {
	return provider.snapshot, nil
}

type loadFailure struct {
	connection int
	err        error
}

func TestHubWebSocketLoad(t *testing.T) {
	connectionCount := loadPositiveInt(t, wsLoadConnectionsEnv, 0)
	if connectionCount == 0 {
		t.Skipf("set %s=20000 to run the single-Gateway capacity gate", wsLoadConnectionsEnv)
	}
	workerCount := loadPositiveInt(t, "AUCTION_WS_LOAD_DIAL_WORKERS", 256)
	if workerCount > connectionCount {
		workerCount = connectionCount
	}
	readTimeout := loadDuration(t, "AUCTION_WS_LOAD_READ_TIMEOUT", 45*time.Second)
	p99Limit := loadDuration(t, "AUCTION_WS_LOAD_P99_LIMIT", 500*time.Millisecond)
	reconnectEnabled := loadBool(t, "AUCTION_WS_LOAD_RECONNECT", false)
	roomID := "room-ws-load"

	hub := NewHub(loadSnapshotProvider{snapshot: loadRoomSnapshot(roomID, 1, v1.LotStatus_LOT_STATUS_LIVE)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeRoom(w, r, roomID)
	}))
	defer server.Close()

	clients := make([]*websocket.Conn, connectionCount)
	defer closeLoadConnections(clients)
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	failures := openLoadConnections(wsURL(server.URL, "/ws/rooms/"+roomID+"?scope=public"), clients, workerCount, readTimeout)
	if len(failures) > 0 {
		first := failures[0]
		t.Fatalf("opened %d/%d websocket connections; first failure at connection %d: %v", connectionCount-len(failures), connectionCount, first.connection, first.err)
	}
	if got := hub.ConnectionCount(); got != connectionCount {
		t.Fatalf("hub connection count=%d want=%d", got, connectionCount)
	}
	connectionDuration := time.Since(startedAt)
	var afterOpen runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&afterOpen)

	deliveries, deliveryFailures, enqueueDuration := publishLoadSnapshotVersion(hub, clients, roomID, 2, v1.LotStatus_LOT_STATUS_SETTLED, readTimeout)
	if len(deliveryFailures) > 0 {
		first := deliveryFailures[0]
		t.Fatalf("delivered terminal snapshot to %d/%d websocket connections; first failure at connection %d: %v", len(deliveries), connectionCount, first.connection, first.err)
	}
	if len(deliveries) != connectionCount {
		t.Fatalf("terminal snapshot delivery count=%d want=%d", len(deliveries), connectionCount)
	}
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i] < deliveries[j] })
	p50 := loadPercentile(deliveries, 50)
	p95 := loadPercentile(deliveries, 95)
	p99 := loadPercentile(deliveries, 99)
	heapDelta := nonNegativeUint64(afterOpen.HeapAlloc, before.HeapAlloc)

	t.Logf("websocket load connections=%d dial_workers=%d connect_duration=%s connect_rate=%.1f/s enqueue_duration=%s delivery_p50=%s delivery_p95=%s delivery_p99=%s delivery_max=%s heap_delta_bytes=%d heap_bytes_per_connection=%d",
		connectionCount,
		workerCount,
		connectionDuration,
		float64(connectionCount)/connectionDuration.Seconds(),
		enqueueDuration,
		p50,
		p95,
		p99,
		deliveries[len(deliveries)-1],
		heapDelta,
		heapDelta/uint64(connectionCount),
	)
	if p99 > p99Limit {
		t.Fatalf("terminal snapshot delivery p99=%s exceeds limit=%s", p99, p99Limit)
	}
	if reconnectEnabled {
		runHubWebSocketReconnectLoad(t, server, hub, clients, roomID, workerCount, readTimeout, p99Limit)
	}
}

func openLoadConnections(url string, clients []*websocket.Conn, workerCount int, readTimeout time.Duration) []loadFailure {
	jobs := make(chan int)
	failures := make(chan loadFailure, len(clients))
	var workers sync.WaitGroup
	dialer := websocket.Dialer{HandshakeTimeout: readTimeout, ReadBufferSize: 1024, WriteBufferSize: 1024}
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				conn, _, err := dialer.Dial(url, nil)
				if err == nil {
					err = readLoadPublicVersion(conn, 1, readTimeout)
				}
				if err != nil {
					if conn != nil {
						_ = conn.Close()
					}
					failures <- loadFailure{connection: index, err: err}
					continue
				}
				clients[index] = conn
			}
		}()
	}
	for index := range clients {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	close(failures)
	result := make([]loadFailure, 0, len(failures))
	for failure := range failures {
		result = append(result, failure)
	}
	return result
}

func publishLoadSnapshotVersion(hub *Hub, clients []*websocket.Conn, roomID string, version int64, status v1.LotStatus, readTimeout time.Duration) ([]time.Duration, []loadFailure, time.Duration) {
	started := make(chan struct{})
	deliveries := make(chan time.Duration, len(clients))
	failures := make(chan loadFailure, len(clients))
	var readers sync.WaitGroup
	var ready sync.WaitGroup
	var publishedAt time.Time
	readers.Add(len(clients))
	ready.Add(len(clients))
	for index, conn := range clients {
		go func(index int, conn *websocket.Conn) {
			defer readers.Done()
			ready.Done()
			<-started
			if err := readLoadPublicVersion(conn, version, readTimeout); err != nil {
				failures <- loadFailure{connection: index, err: err}
				return
			}
			deliveries <- time.Since(publishedAt)
		}(index, conn)
	}
	ready.Wait()
	publishedAt = time.Now()
	close(started)
	snapshot := loadRoomSnapshot(roomID, version, status)
	event := &v1.AuctionEvent{
		Id:               fmt.Sprintf("evt-ws-load-version-%d", version),
		Type:             v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED,
		RoomId:           roomID,
		LotId:            snapshot.GetCurrentLot().GetId(),
		OccurredAtUnixMs: publishedAt.UnixMilli(),
		Lot:              snapshot.GetCurrentLot(),
		Snapshot:         snapshot,
	}
	err := hub.Publish(context.Background(), event)
	enqueueDuration := time.Since(publishedAt)
	if err != nil {
		failures <- loadFailure{connection: -1, err: err}
		closeLoadConnections(clients)
	}
	readers.Wait()
	close(deliveries)
	close(failures)
	deliveryResult := make([]time.Duration, 0, len(deliveries))
	for delivery := range deliveries {
		deliveryResult = append(deliveryResult, delivery)
	}
	failureResult := make([]loadFailure, 0, len(failures))
	for failure := range failures {
		failureResult = append(failureResult, failure)
	}
	return deliveryResult, failureResult, enqueueDuration
}

func runHubWebSocketReconnectLoad(
	t *testing.T,
	oldServer *httptest.Server,
	oldHub *Hub,
	oldClients []*websocket.Conn,
	roomID string,
	workerCount int,
	readTimeout time.Duration,
	p99Limit time.Duration,
) {
	t.Helper()
	spread := loadDuration(t, "AUCTION_WS_LOAD_RECONNECT_SPREAD", 30*time.Second)
	recoveryLimit := loadDuration(t, "AUCTION_WS_LOAD_RECOVERY_LIMIT", 90*time.Second)
	disruptedAt := time.Now()
	oldHub.forceCloseConnections()
	oldServer.CloseClientConnections()
	closeLoadConnections(oldClients)
	if err := waitLoadConnectionCount(oldHub, 0, 10*time.Second); err != nil {
		t.Fatalf("old Gateway did not release all websocket connections: %v", err)
	}

	replacementHub := NewHub(loadSnapshotProvider{snapshot: loadRoomSnapshot(roomID, 2, v1.LotStatus_LOT_STATUS_SETTLED)})
	replacementServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replacementHub.ServeRoom(w, r, roomID)
	}))
	defer replacementServer.Close()
	reconnected := make([]*websocket.Conn, len(oldClients))
	defer closeLoadConnections(reconnected)
	recoveries, failures := openLoadConnectionsWithJitter(
		wsURL(replacementServer.URL, "/ws/rooms/"+roomID+"?scope=public"),
		reconnected,
		workerCount,
		readTimeout,
		2,
		disruptedAt,
		spread,
	)
	if len(failures) > 0 {
		first := failures[0]
		t.Fatalf("reconnected %d/%d websocket connections; first failure at connection %d: %v", len(recoveries), len(reconnected), first.connection, first.err)
	}
	if len(recoveries) != len(reconnected) {
		t.Fatalf("reconnect recovery count=%d want=%d", len(recoveries), len(reconnected))
	}
	if got := replacementHub.ConnectionCount(); got != len(reconnected) {
		t.Fatalf("replacement hub connection count=%d want=%d", got, len(reconnected))
	}
	sort.Slice(recoveries, func(i, j int) bool { return recoveries[i] < recoveries[j] })
	recoveryP50 := loadPercentile(recoveries, 50)
	recoveryP95 := loadPercentile(recoveries, 95)
	recoveryP99 := loadPercentile(recoveries, 99)
	recoveryMax := recoveries[len(recoveries)-1]
	if recoveryMax > recoveryLimit {
		t.Fatalf("all websocket clients recovered in %s, exceeds limit=%s", recoveryMax, recoveryLimit)
	}

	deliveries, deliveryFailures, enqueueDuration := publishLoadSnapshotVersion(
		replacementHub,
		reconnected,
		roomID,
		3,
		v1.LotStatus_LOT_STATUS_SETTLED,
		readTimeout,
	)
	if len(deliveryFailures) > 0 {
		first := deliveryFailures[0]
		t.Fatalf("post-reconnect delivery reached %d/%d clients; first failure at connection %d: %v", len(deliveries), len(reconnected), first.connection, first.err)
	}
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i] < deliveries[j] })
	deliveryP99 := loadPercentile(deliveries, 99)
	if deliveryP99 > p99Limit {
		t.Fatalf("post-reconnect terminal delivery p99=%s exceeds limit=%s", deliveryP99, p99Limit)
	}
	t.Logf("websocket reconnect connections=%d jitter_spread=%s recovery_p50=%s recovery_p95=%s recovery_p99=%s recovery_max=%s post_reconnect_enqueue=%s post_reconnect_delivery_p99=%s",
		len(reconnected), spread, recoveryP50, recoveryP95, recoveryP99, recoveryMax, enqueueDuration, deliveryP99)

	closeLoadConnections(reconnected)
	if err := waitLoadConnectionCount(replacementHub, 0, 10*time.Second); err != nil {
		t.Fatalf("replacement Gateway leaked websocket connections: %v", err)
	}
}

func openLoadConnectionsWithJitter(
	url string,
	clients []*websocket.Conn,
	workerCount int,
	readTimeout time.Duration,
	expectedVersion int64,
	recoveryStartedAt time.Time,
	spread time.Duration,
) ([]time.Duration, []loadFailure) {
	type scheduledConnection struct {
		index int
		delay time.Duration
	}
	schedule := make([]scheduledConnection, len(clients))
	for index := range clients {
		schedule[index] = scheduledConnection{index: index, delay: loadReconnectDelay(index, spread)}
	}
	sort.Slice(schedule, func(i, j int) bool { return schedule[i].delay < schedule[j].delay })
	jobs := make(chan int)
	failures := make(chan loadFailure, len(clients))
	recoveries := make(chan time.Duration, len(clients))
	var workers sync.WaitGroup
	dialer := websocket.Dialer{HandshakeTimeout: readTimeout, ReadBufferSize: 1024, WriteBufferSize: 1024}
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				conn, _, err := dialer.Dial(url, nil)
				if err == nil {
					err = readLoadPublicVersion(conn, expectedVersion, readTimeout)
				}
				if err != nil {
					if conn != nil {
						_ = conn.Close()
					}
					failures <- loadFailure{connection: index, err: err}
					continue
				}
				clients[index] = conn
				recoveries <- time.Since(recoveryStartedAt)
			}
		}()
	}
	for _, item := range schedule {
		wait := time.Until(recoveryStartedAt.Add(item.delay))
		if wait > 0 {
			time.Sleep(wait)
		}
		jobs <- item.index
	}
	close(jobs)
	workers.Wait()
	close(recoveries)
	close(failures)
	recoveryResult := make([]time.Duration, 0, len(recoveries))
	for recovery := range recoveries {
		recoveryResult = append(recoveryResult, recovery)
	}
	failureResult := make([]loadFailure, 0, len(failures))
	for failure := range failures {
		failureResult = append(failureResult, failure)
	}
	return recoveryResult, failureResult
}

func loadReconnectDelay(index int, spread time.Duration) time.Duration {
	if spread <= 0 {
		return 0
	}
	value := uint64(index + 1)
	value += 0x9e3779b97f4a7c15
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	value ^= value >> 31
	fraction := float64(value>>11) / float64(uint64(1)<<53)
	return time.Duration(fraction * float64(spread))
}

func waitLoadConnectionCount(hub *Hub, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := hub.ConnectionCount(); got == want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("connection count=%d want=%d", hub.ConnectionCount(), want)
}

func readLoadPublicVersion(conn *websocket.Conn, version int64, timeout time.Duration) error {
	if conn == nil {
		return fmt.Errorf("websocket connection is nil")
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		envelope := &v1.RealtimeEnvelopeV1{}
		if err := protojson.Unmarshal(payload, envelope); err != nil {
			return fmt.Errorf("decode realtime envelope: %w", err)
		}
		if envelope.GetPublicSnapshot().GetLotVersion() == version {
			return nil
		}
	}
}

func loadRoomSnapshot(roomID string, version int64, status v1.LotStatus) *v1.RoomSnapshot {
	return &v1.RoomSnapshot{
		RoomId: roomID,
		CurrentLot: &v1.Lot{
			Id:              "lot-ws-load",
			RoomId:          roomID,
			Title:           "WebSocket capacity gate",
			Status:          status,
			Version:         version,
			CurrentPrice:    &v1.Money{Amount: 12000 + version, Currency: "CNY"},
			FinalPrice:      &v1.Money{Amount: 12000 + version, Currency: "CNY"},
			EndsAtUnixMs:    time.Now().Add(time.Minute).UnixMilli(),
			SettledAtUnixMs: time.Now().UnixMilli(),
			Stats:           &v1.LotStats{BidCount: version},
		},
		Ranking: []*v1.RankingItem{{
			Rank: 1, UserId: "buyer-ws-load", Nickname: "容量买家", Amount: &v1.Money{Amount: 12000 + version, Currency: "CNY"},
		}},
		ServerTimeUnixMs: time.Now().UnixMilli(),
	}
}

func loadPositiveInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive integer", key)
	}
	return value
}

func loadDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive duration", key)
	}
	return value
}

func loadBool(t *testing.T, key string, fallback bool) bool {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		t.Fatalf("%s must be a boolean", key)
	}
	return value
}

func TestLoadReconnectDelayUsesBoundedFullJitter(t *testing.T) {
	const samples = 10_000
	spread := 30 * time.Second
	minimum := spread
	maximum := time.Duration(0)
	for index := 0; index < samples; index++ {
		delay := loadReconnectDelay(index, spread)
		if delay < 0 || delay >= spread {
			t.Fatalf("delay[%d]=%s outside [0,%s)", index, delay, spread)
		}
		if delay < minimum {
			minimum = delay
		}
		if delay > maximum {
			maximum = delay
		}
	}
	if minimum > spread/100 || maximum < spread*99/100 {
		t.Fatalf("full jitter did not cover the configured window: min=%s max=%s spread=%s", minimum, maximum, spread)
	}
	if loadReconnectDelay(1, 0) != 0 {
		t.Fatal("zero spread must reconnect immediately")
	}
}

func loadPercentile(values []time.Duration, percentile int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := (len(values)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(values) {
		index = len(values)
	}
	return values[index-1]
}

func nonNegativeUint64(after, before uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}

func closeLoadConnections(clients []*websocket.Conn) {
	for _, conn := range clients {
		if conn != nil {
			_ = conn.Close()
		}
	}
}
