package enrichment

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const (
	maxDomainPayloadBytes = 512 << 10
	maxDomainHeaders      = 16
	maxDomainHeaderBytes  = 32 << 10
)

var ErrInvalidRecord = errors.New("invalid order enrichment record")

// Record is a fully validated order-enrichment domain message and its Kafka position.
type Record struct {
	Topic        string
	Partition    int32
	Offset       int64
	MessageID    string
	CausationID  string
	TraceID      string
	Payload      []byte
	PayloadHash  string
	Event        *v1.OrderEnrichmentRequestedDomainEventV1
	OccurredAtMs int64
}

// DecodeRecord verifies Kafka headers, protobuf metadata, identity, and payload bounds.
func DecodeRecord(record *kgo.Record) (Record, error) {
	if record == nil || record.Topic != eventcontract.OrderEnrichmentTopicV1 || record.Partition < 0 || record.Offset < 0 ||
		len(record.Value) == 0 || len(record.Value) > maxDomainPayloadBytes || len(record.Headers) == 0 || len(record.Headers) > maxDomainHeaders {
		return Record{}, fmt.Errorf("%w: invalid source position, payload, or header count", ErrInvalidRecord)
	}
	orderID := string(record.Key)
	if !validID(orderID) {
		return Record{}, fmt.Errorf("%w: invalid order partition key", ErrInvalidRecord)
	}
	headers, err := decodeHeaders(record.Headers)
	if err != nil {
		return Record{}, err
	}
	messageID := headers[eventcontract.DomainHeaderMessageID]
	causationID := headers[eventcontract.DomainHeaderCausationID]
	traceID := headers[eventcontract.RuntimeHeaderTraceID]
	if headers[eventcontract.RuntimeHeaderContentType] != eventcontract.DomainEventContentType ||
		headers[eventcontract.RuntimeHeaderSchemaVersion] != "1" || !validText(messageID, 128) ||
		!validText(causationID, 64) || !validText(traceID, 128) {
		return Record{}, fmt.Errorf("%w: required domain headers are missing or invalid", ErrInvalidRecord)
	}
	expectedMessageID, err := eventcontract.DomainMessageID(causationID, eventcontract.OrderEnrichmentTopicV1)
	if err != nil || messageID != expectedMessageID {
		return Record{}, fmt.Errorf("%w: message_id is not derived from causation_id and topic", ErrInvalidRecord)
	}
	event := new(v1.OrderEnrichmentRequestedDomainEventV1)
	if err := proto.Unmarshal(record.Value, event); err != nil {
		return Record{}, fmt.Errorf("%w: decode protobuf: %v", ErrInvalidRecord, err)
	}
	metadata := event.GetMetadata()
	if metadata == nil || metadata.GetMessageId() != messageID || metadata.GetCausationId() != causationID ||
		metadata.GetTraceId() != traceID || metadata.GetSchemaVersion() != 1 || metadata.GetOccurredAtUnixMs() <= 0 {
		return Record{}, fmt.Errorf("%w: protobuf metadata does not match Kafka headers", ErrInvalidRecord)
	}
	if event.GetOrderId() != orderID || !validID(event.GetLotId()) ||
		!validOptionalID(event.GetAddressId()) || !validOptionalID(event.GetShopId()) {
		return Record{}, fmt.Errorf("%w: order enrichment payload identity is invalid", ErrInvalidRecord)
	}
	digest := sha256.Sum256(record.Value)
	return Record{
		Topic:        record.Topic,
		Partition:    record.Partition,
		Offset:       record.Offset,
		MessageID:    messageID,
		CausationID:  causationID,
		TraceID:      traceID,
		Payload:      append([]byte(nil), record.Value...),
		PayloadHash:  hex.EncodeToString(digest[:]),
		Event:        event,
		OccurredAtMs: metadata.GetOccurredAtUnixMs(),
	}, nil
}

func decodeHeaders(source []kgo.RecordHeader) (map[string]string, error) {
	result := make(map[string]string, len(source))
	totalBytes := 0
	for _, header := range source {
		totalBytes += len(header.Key) + len(header.Value)
		if totalBytes > maxDomainHeaderBytes || !validHeaderName(header.Key) {
			return nil, fmt.Errorf("%w: header names or aggregate size are invalid", ErrInvalidRecord)
		}
		if _, exists := result[header.Key]; exists {
			return nil, fmt.Errorf("%w: duplicate header %q", ErrInvalidRecord, header.Key)
		}
		result[header.Key] = string(header.Value)
	}
	return result, nil
}

func validHeaderName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validID(value string) bool { return validText(value, 64) }

func validOptionalID(value string) bool {
	return value == "" || validID(value)
}

func validText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func recordPosition(record *kgo.Record) string {
	if record == nil {
		return "unknown"
	}
	return record.Topic + "/" + strconv.FormatInt(int64(record.Partition), 10) + "/" + strconv.FormatInt(record.Offset, 10)
}
