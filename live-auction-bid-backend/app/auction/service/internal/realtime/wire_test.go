package realtime

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
)

func TestPublicSnapshotSerializedBytesContainNoRawBuyerIdentity(t *testing.T) {
	snapshot := realtimeWireSnapshot(7, "buyer-a", "张三", "buyer-b", "李四")
	frame, err := encodeRealtimeEnvelope("evt-public", 1000, publicSnapshotV1(snapshot))
	if err != nil {
		t.Fatalf("encode public snapshot: %v", err)
	}
	for _, secret := range [][]byte{[]byte("buyer-a"), []byte("buyer-b"), []byte("张三"), []byte("李四"), []byte("private-avatar")} {
		if bytes.Contains(frame.data, secret) {
			t.Fatalf("public bytes leaked identity %q: %s", secret, frame.data)
		}
	}
	if !bytes.Contains(frame.data, []byte("张***")) || !bytes.Contains(frame.data, []byte("李***")) {
		t.Fatalf("public bytes should retain masked display names: %s", frame.data)
	}
}

func TestHubReusesOnePublicEncodingForSameRoomVersion(t *testing.T) {
	hub := NewHub(nil)
	snapshot := realtimeWireSnapshot(7, "buyer-a", "张三", "buyer-b", "李四")
	first, err := hub.prepareRoomFrames(snapshot, v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_ACCEPTED, "evt-1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.prepareRoomFrames(snapshot, v1.AuctionEventType_AUCTION_EVENT_TYPE_RANKING_UPDATED, "evt-2", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if first.public != second.public || first.admin != second.admin {
		t.Fatal("same (room, lot version) must reuse shared public/admin encoded payloads")
	}
	original := append([]byte(nil), first.public.data...)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !bytes.Equal(first.public.data, original) {
				t.Errorf("shared payload changed during concurrent read")
			}
		}()
	}
	wg.Wait()
	if !bytes.Equal(first.public.data, original) {
		t.Fatal("shared payload was mutated")
	}
}

func TestHubRoutesPersonalDeltaOnlyToMatchingUserAndEmitsTombstone(t *testing.T) {
	hub := NewHub(nil)
	a := realtimeTestConnection(hub, "buyer-a")
	b := realtimeTestConnection(hub, "buyer-b")
	if err := hub.join(a); err != nil {
		t.Fatal(err)
	}
	if err := hub.join(b); err != nil {
		t.Fatal(err)
	}
	defer hub.leave(a)
	defer hub.leave(b)

	first := realtimeWireSnapshot(7, "buyer-a", "张三", "buyer-b", "李四")
	if err := hub.deliver(context.Background(), &v1.AuctionEvent{Id: "evt-7", RoomId: "room1", LotId: "lot1", Type: v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_ACCEPTED, Snapshot: first}); err != nil {
		t.Fatal(err)
	}
	assertPersonalBatch(t, <-a.latestState, "buyer-a", false)
	assertPersonalBatch(t, <-b.latestState, "buyer-b", false)

	second := realtimeWireSnapshot(8, "buyer-a", "张三", "", "")
	if err := hub.deliver(context.Background(), &v1.AuctionEvent{Id: "evt-8", RoomId: "room1", LotId: "lot1", Type: v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_ACCEPTED, Snapshot: second}); err != nil {
		t.Fatal(err)
	}
	assertPersonalBatch(t, <-a.latestState, "buyer-a", false)
	assertPersonalBatch(t, <-b.latestState, "buyer-b", true)
}

func TestOrderCreatedSignalDoesNotClaimProjectionIsReady(t *testing.T) {
	snapshot := realtimeWireSnapshot(9, "buyer-a", "张三", "", "")
	snapshot.CurrentLot.Status = v1.LotStatus_LOT_STATUS_SETTLED
	snapshot.CurrentLot.WinnerUserId = "buyer-a"
	delta := personalDeltaV1(snapshot, "buyer-a", v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED, false)
	if delta.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_CREATING || delta.GetYourOrderId() == "" {
		t.Fatalf("optimistic runtime signal must remain CREATING until MySQL projection is verified: %+v", delta)
	}
}

func TestSlowConnectionOverwritesStateButCriticalOverflowDisconnects(t *testing.T) {
	hub := NewHub(nil)
	conn := &connection{
		hub:         hub,
		roomID:      "room1",
		ctx:         context.Background(),
		latestState: make(chan *wireBatch, 1),
		critical:    make(chan *criticalFrame, 1),
		done:        make(chan struct{}),
	}
	first := &immutablePayload{data: []byte("first"), kind: "public_snapshot"}
	second := &immutablePayload{data: []byte("second"), kind: "public_snapshot"}
	conn.enqueueLatest(&wireBatch{frames: []*immutablePayload{first}})
	conn.enqueueLatest(&wireBatch{frames: []*immutablePayload{second}})
	if got := <-conn.latestState; len(got.frames) != 1 || got.frames[0] != second {
		t.Fatalf("latest state slot did not overwrite old state: %+v", got)
	}
	select {
	case <-conn.done:
		t.Fatal("replaceable state overflow must not disconnect")
	default:
	}

	critical := &criticalFrame{frame: &immutablePayload{data: []byte("critical"), kind: "reconnect"}}
	if !conn.enqueueCritical(critical) {
		t.Fatal("first critical frame should fit")
	}
	if conn.enqueueCritical(critical) {
		t.Fatal("second critical frame should overflow bounded FIFO")
	}
	select {
	case <-conn.done:
	default:
		t.Fatal("critical overflow must disconnect with resync requirement")
	}
}

func TestHeartbeatEncodingIsSharedWithinFiveSecondBucket(t *testing.T) {
	hub := NewHub(nil)
	_, err := hub.prepareRoomFrames(realtimeWireSnapshot(7, "buyer-a", "张三", "", ""), v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT, "evt-7", 1000)
	if err != nil {
		t.Fatal(err)
	}
	first := hub.heartbeatFrame("room1", 10_001)
	second := hub.heartbeatFrame("room1", 14_999)
	third := hub.heartbeatFrame("room1", 15_001)
	if first == nil || first != second {
		t.Fatal("connections in one room must share one heartbeat encoding per bucket")
	}
	if third == nil || third == first {
		t.Fatal("next heartbeat bucket must carry a fresh server timestamp")
	}
}

func realtimeTestConnection(hub *Hub, userID string) *connection {
	return &connection{
		hub:    hub,
		roomID: "room1",
		scope:  ScopePublic,
		ctx:    context.Background(),
		authCtx: auth.AuthContext{
			TokenStatus: auth.TokenStatusValid,
			Claims:      &auth.Claims{UserID: userID},
		},
		latestState: make(chan *wireBatch, 1),
		critical:    make(chan *criticalFrame, criticalQueueCapacity),
		done:        make(chan struct{}),
	}
}

func assertPersonalBatch(t *testing.T, batch *wireBatch, wantUserID string, wantTombstone bool) {
	t.Helper()
	if batch == nil || len(batch.frames) != 2 {
		t.Fatalf("expected public+personal batch, got %+v", batch)
	}
	envelope := &v1.RealtimeEnvelopeV1{}
	if err := protojson.Unmarshal(batch.frames[1].data, envelope); err != nil {
		t.Fatalf("decode personal frame: %v", err)
	}
	delta := envelope.GetPersonalDelta()
	if delta == nil || delta.GetUserId() != wantUserID || delta.GetTombstone() != wantTombstone {
		t.Fatalf("personal route mismatch: want user=%s tombstone=%t got=%+v", wantUserID, wantTombstone, delta)
	}
	if bytes.Contains(batch.frames[1].data, []byte(otherBuyer(wantUserID))) {
		t.Fatalf("personal payload leaked another buyer: %s", batch.frames[1].data)
	}
}

func otherBuyer(userID string) string {
	if userID == "buyer-a" {
		return "buyer-b"
	}
	return "buyer-a"
}

func realtimeWireSnapshot(version int64, firstID, firstName, secondID, secondName string) *v1.RoomSnapshot {
	ranking := []*v1.RankingItem{{
		Rank:        1,
		UserId:      firstID,
		Nickname:    firstName,
		AvatarUrl:   "https://private-avatar.example/first",
		Amount:      &v1.Money{Amount: 12000, Currency: "CNY"},
		BidAtUnixMs: 900,
	}}
	if secondID != "" {
		ranking = append(ranking, &v1.RankingItem{
			Rank:        2,
			UserId:      secondID,
			Nickname:    secondName,
			AvatarUrl:   "https://private-avatar.example/second",
			Amount:      &v1.Money{Amount: 11000, Currency: "CNY"},
			BidAtUnixMs: 800,
		})
	}
	return &v1.RoomSnapshot{
		RoomId: "room1",
		CurrentLot: &v1.Lot{
			Id:              "lot1",
			RoomId:          "room1",
			MainAccountId:   "main1",
			Status:          v1.LotStatus_LOT_STATUS_LIVE,
			Version:         version,
			CurrentPrice:    &v1.Money{Amount: 12000, Currency: "CNY"},
			LeadingUserId:   firstID,
			LeadingNickname: firstName,
			EndsAtUnixMs:    2000,
			Stats:           &v1.LotStats{BidCount: int64(len(ranking))},
		},
		Ranking: ranking,
	}
}
