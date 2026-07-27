package realtime

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

var natsEventMarshal = protojson.MarshalOptions{
	UseEnumNumbers: false,
	UseProtoNames:  false,
}

var natsEventUnmarshal = protojson.UnmarshalOptions{
	DiscardUnknown: true,
}

type natsEventEnvelope struct {
	Origin string `json:"origin"`
	Event  []byte `json:"event"`
}

func encodeNATSEventEnvelope(origin string, event *v1.AuctionEvent) (string, error) {
	if event == nil {
		return "", errors.New("realtime event is required")
	}
	raw, err := natsEventMarshal.Marshal(event)
	if err != nil {
		return "", err
	}
	payload, err := jsonMarshal(natsEventEnvelope{Origin: strings.TrimSpace(origin), Event: raw})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func decodeNATSEventEnvelope(payload string) (string, *v1.AuctionEvent, error) {
	var envelope natsEventEnvelope
	if err := jsonUnmarshal([]byte(payload), &envelope); err != nil {
		return "", nil, err
	}
	event := &v1.AuctionEvent{}
	if err := natsEventUnmarshal.Unmarshal(envelope.Event, event); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(envelope.Origin), event, nil
}

func dispatchNATSEvent(ctx context.Context, ownOrigin string, sink EventPublisher, payload string) (bool, error) {
	origin, event, err := decodeNATSEventEnvelope(payload)
	if err != nil {
		return false, err
	}
	if origin == strings.TrimSpace(ownOrigin) {
		return false, nil
	}
	if err := sink.Publish(ctx, event); err != nil {
		return false, err
	}
	return true, nil
}
