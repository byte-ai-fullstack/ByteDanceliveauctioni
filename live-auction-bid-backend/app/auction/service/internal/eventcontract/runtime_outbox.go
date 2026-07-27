package eventcontract

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const maxRuntimeOutboxEnvelopeBytes = MaxRuntimeFactBytes + 64

var ErrInvalidRuntimeOutboxItem = errors.New("invalid runtime outbox item")

// MarshalRuntimeFactJSON produces the Redis outbox representation after full contract validation.
func MarshalRuntimeFactJSON(fact *v1.RuntimeFactV1) ([]byte, error) {
	if err := ValidateRuntimeFact(fact); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRuntimeOutboxItem, err)
	}
	payload, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(fact)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal JSON: %v", ErrInvalidRuntimeOutboxItem, err)
	}
	if len(payload) > MaxRuntimeFactBytes {
		return nil, fmt.Errorf("%w: JSON size %d exceeds %d", ErrInvalidRuntimeOutboxItem, len(payload), MaxRuntimeFactBytes)
	}
	return payload, nil
}

// MarshalRuntimeFactBinary produces the deterministic Kafka record value.
func MarshalRuntimeFactBinary(fact *v1.RuntimeFactV1) ([]byte, error) {
	if err := ValidateRuntimeFact(fact); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRuntimeOutboxItem, err)
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(fact)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal protobuf: %v", ErrInvalidRuntimeOutboxItem, err)
	}
	if len(payload) > MaxRuntimeFactBytes {
		return nil, fmt.Errorf("%w: protobuf size %d exceeds %d", ErrInvalidRuntimeOutboxItem, len(payload), MaxRuntimeFactBytes)
	}
	return payload, nil
}

// EncodeRuntimeOutboxItem prefixes a validated fact with the event ID used by the fenced ACK script.
func EncodeRuntimeOutboxItem(fact *v1.RuntimeFactV1) (string, error) {
	payload, err := MarshalRuntimeFactJSON(fact)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(fact.GetEventId(), "\r\n") {
		return "", fmt.Errorf("%w: event_id contains a line break", ErrInvalidRuntimeOutboxItem)
	}
	return fact.GetEventId() + "\n" + string(payload), nil
}

// DecodeRuntimeOutboxItem validates both the ACK prefix and the embedded RuntimeFact.
func DecodeRuntimeOutboxItem(item string) (*v1.RuntimeFactV1, error) {
	if len(item) == 0 || len(item) > maxRuntimeOutboxEnvelopeBytes {
		return nil, fmt.Errorf("%w: envelope size %d is outside the allowed range", ErrInvalidRuntimeOutboxItem, len(item))
	}
	newline := strings.IndexByte(item, '\n')
	if newline <= 0 || newline == len(item)-1 {
		return nil, fmt.Errorf("%w: expected event_id and JSON separated by one newline", ErrInvalidRuntimeOutboxItem)
	}
	eventID := item[:newline]
	if strings.ContainsRune(eventID, '\r') {
		return nil, fmt.Errorf("%w: event_id contains a carriage return", ErrInvalidRuntimeOutboxItem)
	}
	if err := ValidateEventID(eventID); err != nil {
		return nil, fmt.Errorf("%w: prefix: %v", ErrInvalidRuntimeOutboxItem, err)
	}

	payload := []byte(item[newline+1:])
	fact := new(v1.RuntimeFactV1)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, fact); err != nil {
		return nil, fmt.Errorf("%w: unmarshal JSON: %v", ErrInvalidRuntimeOutboxItem, err)
	}
	if fact.GetEventId() != eventID {
		return nil, fmt.Errorf("%w: prefix event_id does not match payload", ErrInvalidRuntimeOutboxItem)
	}
	if err := ValidateRuntimeFact(fact); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRuntimeOutboxItem, err)
	}
	return fact, nil
}

// RuntimeFactBinaryEqual reports semantic equality using the same deterministic bytes sent to Kafka.
func RuntimeFactBinaryEqual(left, right *v1.RuntimeFactV1) (bool, error) {
	leftPayload, err := MarshalRuntimeFactBinary(left)
	if err != nil {
		return false, err
	}
	rightPayload, err := MarshalRuntimeFactBinary(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftPayload, rightPayload), nil
}
