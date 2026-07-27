package realtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

type capturePublisher struct {
	events []*v1.AuctionEvent
}

func (publisher *capturePublisher) Publish(_ context.Context, event *v1.AuctionEvent) error {
	publisher.events = append(publisher.events, event)
	return nil
}

func TestNATSBusConfigDefaults(t *testing.T) {
	bus, err := NewNATSBus(NATSBusConfig{Origin: "node-a"})
	if err != nil {
		t.Fatalf("new nats bus: %v", err)
	}
	if bus.url != defaultNATSURL {
		t.Fatalf("default url mismatch: %s", bus.url)
	}
	if bus.origin != "node-a" || bus.name == "" {
		t.Fatalf("origin/name mismatch: origin=%s name=%s", bus.origin, bus.name)
	}
	if bus.subs == nil {
		t.Fatal("room subscription registry must be initialized")
	}
	if bus.reconnectWait != defaultNATSReconnectWait ||
		bus.reconnectJitter != defaultNATSReconnectJitter ||
		bus.flushTimeout != defaultNATSFlushTimeout ||
		bus.dispatchTimeout != defaultNATSDispatchTimeout {
		t.Fatalf(
			"duration defaults mismatch: reconnect_wait=%s reconnect_jitter=%s flush=%s dispatch=%s",
			bus.reconnectWait,
			bus.reconnectJitter,
			bus.flushTimeout,
			bus.dispatchTimeout,
		)
	}
}

func TestNATSBusRequiresStableOrigin(t *testing.T) {
	if _, err := NewNATSBus(NATSBusConfig{}); err == nil {
		t.Fatal("missing origin would make different gateways suppress each other's messages")
	}
}

func TestNATSBusRejectsNegativeReconnectJitter(t *testing.T) {
	_, err := NewNATSBus(NATSBusConfig{Origin: "node-a", ReconnectJitter: -time.Millisecond})
	if err == nil {
		t.Fatal("negative reconnect jitter must be rejected")
	}
}

func TestNATSBusStartsAndRetainsRoomWhileServerUnavailable(t *testing.T) {
	bus, err := NewNATSBus(NATSBusConfig{
		URL:             "nats://127.0.0.1:1",
		Origin:          "node-a",
		ReconnectWait:   10 * time.Millisecond,
		ReconnectJitter: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new nats bus: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := bus.Start(ctx, &capturePublisher{}); err != nil {
		t.Fatalf("start during NATS outage: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	if err := bus.RetainRoom(ctx, "room-a"); err != nil {
		t.Fatalf("retain room during NATS outage: %v", err)
	}
	bus.mu.Lock()
	subscriptionCount := len(bus.subs)
	bus.mu.Unlock()
	if subscriptionCount != 1 {
		t.Fatalf("subscription intent must survive initial outage, got %d", subscriptionCount)
	}

	if err := bus.Publish(ctx, &v1.AuctionEvent{Id: "evt-1", RoomId: "room-a"}); err != nil {
		t.Fatalf("realtime acceleration outage must not fail authoritative path: %v", err)
	}
	if err := bus.ReleaseRoom("room-a"); err != nil {
		t.Fatalf("release retained room: %v", err)
	}
}

func TestNATSBusPublishOnlyModeCannotRetainRooms(t *testing.T) {
	bus, err := NewNATSBus(NATSBusConfig{
		URL:             "nats://127.0.0.1:1",
		Origin:          "auction-service-a",
		ReconnectWait:   10 * time.Millisecond,
		ReconnectJitter: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new nats bus: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := bus.StartPublisher(ctx); err != nil {
		t.Fatalf("start publish-only bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.RetainRoom(ctx, "room-a"); err == nil {
		t.Fatal("publish-only auction service retained a gateway room subscription")
	}
	if err := bus.Publish(ctx, &v1.AuctionEvent{Id: "evt-1", RoomId: "room-a"}); err != nil {
		t.Fatalf("publish-only outage must remain best effort: %v", err)
	}
}

func TestNATSBusSubscriberModeRequiresSink(t *testing.T) {
	bus, err := NewNATSBus(NATSBusConfig{Origin: "gateway-a"})
	if err != nil {
		t.Fatalf("new nats bus: %v", err)
	}
	if err := bus.Start(context.Background(), nil); err == nil {
		t.Fatal("subscriber mode accepted a nil event sink")
	}
}

func TestNATSBusPublishRejectsEventWithoutRoom(t *testing.T) {
	bus := &NATSBus{origin: "node-a"}
	if err := bus.Publish(context.Background(), &v1.AuctionEvent{Id: "evt-1"}); err == nil {
		t.Fatal("event without room must not use a global NATS subject")
	}
}

func TestNATSBusDispatchSkipsOwnOrigin(t *testing.T) {
	bus := &NATSBus{origin: "node-a"}
	payload, err := encodeNATSEventEnvelope("node-a", &v1.AuctionEvent{Id: "evt-1"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sink := &capturePublisher{}
	delivered, err := bus.dispatchPayload(context.Background(), sink, payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if delivered || len(sink.events) != 0 {
		t.Fatalf("own origin should be skipped, delivered=%v events=%d", delivered, len(sink.events))
	}
}

func TestNATSBusDispatchDeliversRemoteOrigin(t *testing.T) {
	bus := &NATSBus{origin: "node-b"}
	payload, err := encodeNATSEventEnvelope("node-a", &v1.AuctionEvent{Id: "evt-1", RoomId: "room-a"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sink := &capturePublisher{}
	delivered, err := bus.dispatchPayload(context.Background(), sink, payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !delivered || len(sink.events) != 1 || sink.events[0].GetRoomId() != "room-a" {
		t.Fatalf("remote event should be delivered, delivered=%v events=%+v", delivered, sink.events)
	}
}

func TestNATSEventEnvelopeRoundTrip(t *testing.T) {
	payload, err := encodeNATSEventEnvelope("node-a", &v1.AuctionEvent{
		Id: "evt-1", RoomId: "room-a", LotId: "lot-a", Type: v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED,
		BuyerUserId: "buyer-a", OrderId: "order-a", OrderVisibility: v1.OrderVisibility_ORDER_VISIBILITY_READY, LotVersion: 7,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	origin, event, err := decodeNATSEventEnvelope(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if origin != "node-a" || event.GetId() != "evt-1" || event.GetRoomId() != "room-a" ||
		event.GetBuyerUserId() != "buyer-a" || event.GetOrderId() != "order-a" ||
		event.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_READY || event.GetLotVersion() != 7 {
		t.Fatalf("round trip mismatch: origin=%s event=%+v", origin, event)
	}
}

func TestIsTransientNATSError(t *testing.T) {
	for _, err := range []error{
		nats.ErrConnectionReconnecting,
		nats.ErrDisconnected,
		nats.ErrReconnectBufExceeded,
		nats.ErrTimeout,
		nats.ErrNoServers,
	} {
		if !isTransientNATSError(err) {
			t.Fatalf("expected transient NATS error: %v", err)
		}
		if !isTransientNATSError(errors.Join(errors.New("wrapped"), err)) {
			t.Fatalf("expected wrapped transient NATS error: %v", err)
		}
	}
	if isTransientNATSError(errors.New("permanent")) {
		t.Fatal("permanent error must not be classified as transient")
	}
}
