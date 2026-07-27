package searchstate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

const (
	maxLotStatePayloadBytes = 512 << 10
	maxLotStateHeaders      = 16
	maxLotStateHeaderBytes  = 32 << 10
)

var ErrInvalidRecord = errors.New("invalid lot-state search index record")

// Record is the validated Kafka envelope shared by independent search projections.
type Record struct {
	Topic       string
	Partition   int32
	Offset      int64
	MessageID   string
	CausationID string
	TraceID     string
	Event       *v1.LotStateDomainEventV1
	Document    searchindex.LotDocument
}

func (record Record) LastEventID() string { return record.CausationID }

func DecodeRecord(source *kgo.Record) (Record, error) {
	if source == nil || source.Topic != eventcontract.LotStateTopicV1 || source.Partition < 0 || source.Offset < 0 ||
		len(source.Value) == 0 || len(source.Value) > maxLotStatePayloadBytes || len(source.Headers) == 0 || len(source.Headers) > maxLotStateHeaders {
		return Record{}, fmt.Errorf("%w: source position, payload, or header count is invalid", ErrInvalidRecord)
	}
	lotID := string(source.Key)
	if !ValidText(lotID, 64) {
		return Record{}, fmt.Errorf("%w: partition key is invalid", ErrInvalidRecord)
	}
	headers, err := decodeHeaders(source.Headers)
	if err != nil {
		return Record{}, err
	}
	messageID := headers[eventcontract.DomainHeaderMessageID]
	causationID := headers[eventcontract.DomainHeaderCausationID]
	traceID := headers[eventcontract.RuntimeHeaderTraceID]
	if headers[eventcontract.RuntimeHeaderContentType] != eventcontract.DomainEventContentType ||
		headers[eventcontract.RuntimeHeaderSchemaVersion] != "1" || !ValidText(messageID, 128) ||
		eventcontract.ValidateEventID(causationID) != nil || !ValidText(traceID, 128) {
		return Record{}, fmt.Errorf("%w: required domain headers are missing or invalid", ErrInvalidRecord)
	}
	expectedMessageID, err := eventcontract.DomainMessageID(causationID, eventcontract.LotStateTopicV1)
	if err != nil || expectedMessageID != messageID {
		return Record{}, fmt.Errorf("%w: message identity is invalid", ErrInvalidRecord)
	}
	event := new(v1.LotStateDomainEventV1)
	if err := proto.Unmarshal(source.Value, event); err != nil {
		return Record{}, fmt.Errorf("%w: decode protobuf: %v", ErrInvalidRecord, err)
	}
	metadata := event.GetMetadata()
	if metadata == nil || metadata.GetMessageId() != messageID || metadata.GetCausationId() != causationID ||
		metadata.GetTraceId() != traceID || metadata.GetSchemaVersion() != 1 || metadata.GetOccurredAtUnixMs() <= 0 ||
		event.GetLotId() != lotID || eventcontract.ValidateLotStateDomainEvent(event) != nil {
		return Record{}, fmt.Errorf("%w: protobuf metadata or lot document is invalid", ErrInvalidRecord)
	}
	return Record{
		Topic: source.Topic, Partition: source.Partition, Offset: source.Offset,
		MessageID: messageID, CausationID: causationID, TraceID: traceID,
		Event: event, Document: searchindex.LotDocumentFromDomainEvent(event),
	}, nil
}

func decodeHeaders(source []kgo.RecordHeader) (map[string]string, error) {
	result := make(map[string]string, len(source))
	totalBytes := 0
	for _, header := range source {
		totalBytes += len(header.Key) + len(header.Value)
		if totalBytes > maxLotStateHeaderBytes || !validHeaderName(header.Key) {
			return nil, fmt.Errorf("%w: header names or aggregate size are invalid", ErrInvalidRecord)
		}
		if _, duplicate := result[header.Key]; duplicate {
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

func ValidText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func Position(record *kgo.Record) string {
	if record == nil {
		return "unknown"
	}
	return fmt.Sprintf("%s/%d/%d", record.Topic, record.Partition, record.Offset)
}
