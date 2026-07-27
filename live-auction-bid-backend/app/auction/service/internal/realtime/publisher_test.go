package realtime

import (
	"context"
	"errors"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestPublisherFansOutInOrderAndStopsOnFailure(t *testing.T) {
	callOrder := make([]string, 0, 3)
	sentinel := errors.New("sink unavailable")
	first := eventPublisherFunc(func(context.Context, *v1.AuctionEvent) error {
		callOrder = append(callOrder, "first")
		return nil
	})
	second := eventPublisherFunc(func(context.Context, *v1.AuctionEvent) error {
		callOrder = append(callOrder, "second")
		return sentinel
	})
	third := eventPublisherFunc(func(context.Context, *v1.AuctionEvent) error {
		callOrder = append(callOrder, "third")
		return nil
	})
	publisher := NewPublisher(first, nil, second, third)
	event := &v1.AuctionEvent{RoomId: "room-1"}
	if err := publisher.Publish(context.Background(), event); !errors.Is(err, sentinel) {
		t.Fatalf("Publish error=%v want sentinel", err)
	}
	if len(callOrder) != 2 || callOrder[0] != "first" || callOrder[1] != "second" {
		t.Fatalf("call order=%v", callOrder)
	}
}

func TestPublisherIgnoresNilEvent(t *testing.T) {
	called := false
	publisher := NewPublisher(eventPublisherFunc(func(context.Context, *v1.AuctionEvent) error {
		called = true
		return nil
	}))
	if err := publisher.Publish(context.Background(), nil); err != nil {
		t.Fatalf("Publish nil event: %v", err)
	}
	if called {
		t.Fatal("nil event reached sink")
	}
}

type eventPublisherFunc func(context.Context, *v1.AuctionEvent) error

func (publish eventPublisherFunc) Publish(ctx context.Context, event *v1.AuctionEvent) error {
	return publish(ctx, event)
}
