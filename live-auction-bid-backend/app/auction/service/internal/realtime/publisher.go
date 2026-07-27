package realtime

import (
	"context"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

type EventPublisher interface {
	Publish(ctx context.Context, event *v1.AuctionEvent) error
}

type Publisher struct {
	sinks []EventPublisher
}

func NewPublisher(sinks ...EventPublisher) *Publisher {
	return &Publisher{sinks: sinks}
}

func (p *Publisher) Publish(ctx context.Context, event *v1.AuctionEvent) error {
	if event == nil {
		return nil
	}
	for _, sink := range p.sinks {
		if sink == nil {
			continue
		}
		if err := sink.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
