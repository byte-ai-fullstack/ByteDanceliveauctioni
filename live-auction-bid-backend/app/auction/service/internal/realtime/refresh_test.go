package realtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

type mutableSnapshotProvider struct {
	mu       sync.RWMutex
	snapshot *v1.RoomSnapshot
}

type signalingSnapshotProvider struct {
	snapshot *v1.RoomSnapshot
	called   chan struct{}
	once     sync.Once
}

func (provider *signalingSnapshotProvider) Snapshot(context.Context, string) (*v1.RoomSnapshot, error) {
	provider.once.Do(func() { close(provider.called) })
	return proto.Clone(provider.snapshot).(*v1.RoomSnapshot), nil
}

func (provider *mutableSnapshotProvider) Snapshot(context.Context, string) (*v1.RoomSnapshot, error) {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	return proto.Clone(provider.snapshot).(*v1.RoomSnapshot), nil
}

func (provider *mutableSnapshotProvider) set(snapshot *v1.RoomSnapshot) {
	provider.mu.Lock()
	provider.snapshot = snapshot
	provider.mu.Unlock()
}

func TestSnapshotRefreshRecoversTerminalStateMissedFromNATS(t *testing.T) {
	initial := testSnapshot()
	provider := &mutableSnapshotProvider{snapshot: initial}
	hub := NewHub(provider)
	conn := &connection{
		hub: hub, roomID: "room1", scope: ScopePublic, ctx: context.Background(),
		latestState: make(chan *wireBatch, 1), critical: make(chan *criticalFrame, criticalQueueCapacity), done: make(chan struct{}),
	}
	if err := hub.join(conn); err != nil {
		t.Fatalf("join refresh test connection: %v", err)
	}
	t.Cleanup(func() {
		hub.leave(conn)
		conn.close()
	})
	if _, err := hub.prepareRoomFrames(initial, v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT, "initial", 1_000); err != nil {
		t.Fatalf("prepare initial cache: %v", err)
	}

	terminal := proto.Clone(initial).(*v1.RoomSnapshot)
	terminal.CurrentLot.Version = 8
	terminal.CurrentLot.Status = v1.LotStatus_LOT_STATUS_SETTLED
	terminal.CurrentLot.FinalPrice = &v1.Money{Amount: 12_000, Currency: "CNY"}
	terminal.CurrentLot.WinnerUserId = "buyer1"
	terminal.CurrentLot.WinnerNickname = "张三"
	terminal.CurrentLot.SettledAtUnixMs = 2_000
	provider.set(terminal)

	err := hub.RefreshSnapshots(context.Background(), SnapshotRefreshConfig{
		Interval: time.Second, RequestTimeout: time.Second, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("refresh terminal snapshot: %v", err)
	}
	select {
	case batch := <-conn.latestState:
		if len(batch.frames) == 0 {
			t.Fatal("refresh emitted an empty wire batch")
		}
		envelope := &v1.RealtimeEnvelopeV1{}
		if err := protojson.Unmarshal(batch.frames[0].data, envelope); err != nil {
			t.Fatalf("decode refreshed envelope: %v", err)
		}
		public := envelope.GetPublicSnapshot()
		if public.GetLotVersion() != 8 || public.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED || public.GetSettlement() == nil {
			t.Fatalf("refresh did not recover complete terminal state: %+v", public)
		}
	default:
		t.Fatal("authoritative version advance did not emit a refresh")
	}
	heartbeat := hub.heartbeatFrame("room1", 5_000)
	if heartbeat == nil {
		t.Fatal("refreshed room did not update heartbeat cache")
	}
	envelope := &v1.RealtimeEnvelopeV1{}
	if err := protojson.Unmarshal(heartbeat.data, envelope); err != nil {
		t.Fatalf("decode refreshed heartbeat: %v", err)
	}
	if envelope.GetHeartbeat().GetAuthoritativeLotVersion() != 8 || envelope.GetHeartbeat().GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED {
		t.Fatalf("heartbeat retained stale NATS state: %+v", envelope.GetHeartbeat())
	}
}

func TestSnapshotRefreshDoesNotRebroadcastUnchangedVersion(t *testing.T) {
	initial := testSnapshot()
	provider := &mutableSnapshotProvider{snapshot: initial}
	hub := NewHub(provider)
	conn := &connection{
		hub: hub, roomID: "room1", scope: ScopePublic, ctx: context.Background(),
		latestState: make(chan *wireBatch, 1), critical: make(chan *criticalFrame, criticalQueueCapacity), done: make(chan struct{}),
	}
	if err := hub.join(conn); err != nil {
		t.Fatalf("join refresh test connection: %v", err)
	}
	t.Cleanup(func() {
		hub.leave(conn)
		conn.close()
	})
	if _, err := hub.prepareRoomFrames(initial, v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT, "initial", 1_000); err != nil {
		t.Fatalf("prepare initial cache: %v", err)
	}
	if err := hub.RefreshSnapshots(context.Background(), SnapshotRefreshConfig{
		Interval: time.Second, RequestTimeout: time.Second, Concurrency: 1,
	}); err != nil {
		t.Fatalf("refresh unchanged snapshot: %v", err)
	}
	select {
	case batch := <-conn.latestState:
		t.Fatalf("unchanged version was rebroadcast: %+v", batch)
	default:
	}
}

func TestRunSnapshotRefreshPollsViewedRoomsAndStopsCleanly(t *testing.T) {
	provider := &signalingSnapshotProvider{snapshot: testSnapshot(), called: make(chan struct{})}
	hub := NewHub(provider)
	conn := &connection{
		hub: hub, roomID: "room1", scope: ScopePublic, ctx: context.Background(),
		latestState: make(chan *wireBatch, 1), critical: make(chan *criticalFrame, criticalQueueCapacity), done: make(chan struct{}),
	}
	if err := hub.join(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		hub.leave(conn)
		conn.close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- hub.RunSnapshotRefresh(ctx, SnapshotRefreshConfig{Interval: time.Millisecond, RequestTimeout: time.Second, Concurrency: 1})
	}()
	select {
	case <-provider.called:
	case <-time.After(time.Second):
		t.Fatal("snapshot refresh did not poll an active room")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunSnapshotRefresh: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot refresh did not stop after cancellation")
	}
}

func TestRunSnapshotRefreshRejectsInvalidConfigAndStopsWhenDraining(t *testing.T) {
	hub := NewHub(nil)
	if err := hub.RunSnapshotRefresh(context.Background(), SnapshotRefreshConfig{}); err == nil {
		t.Fatal("RunSnapshotRefresh accepted invalid config")
	}
	hub.BeginDrain()
	if err := hub.RunSnapshotRefresh(context.Background(), SnapshotRefreshConfig{Interval: time.Millisecond, RequestTimeout: time.Second, Concurrency: 1}); err != nil {
		t.Fatalf("draining RunSnapshotRefresh: %v", err)
	}
}
