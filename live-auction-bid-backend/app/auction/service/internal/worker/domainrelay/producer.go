package domainrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

const (
	maxDomainPayloadBytes      = 512 << 10
	maxDomainHeadersBytes      = 32 << 10
	maxDomainHeaders           = 16
	maxDeadLetterPayloadSample = 256 << 10
	domainProducerBatchBytes   = 1 << 20
)

var (
	ErrInvalidDomainMessage = errors.New("invalid domain outbox message")
	ErrDomainProduce        = errors.New("domain Kafka produce failed")
)

// Producer waits for definitive Kafka acknowledgements for original and dead-letter records.
type Producer interface {
	ProduceDomain(ctx context.Context, message Message) error
	ProduceDeadLetter(ctx context.Context, message Message, attempts int, failure string, failedAt time.Time) error
}

type domainKafkaClient interface {
	Ping(ctx context.Context) error
	ProduceSync(ctx context.Context, records ...*kgo.Record) kgo.ProduceResults
	Close()
}

// KafkaProducer publishes claimed MySQL domain outbox rows with idempotent franz-go defaults.
type KafkaProducer struct {
	client domainKafkaClient
}

// NewKafkaProducer creates an all-ISR-acknowledged producer and verifies broker reachability.
func NewKafkaProducer(ctx context.Context, cfg kafkaclient.Config) (*KafkaProducer, error) {
	options, err := cfg.Options()
	if err != nil {
		return nil, err
	}
	options = append(options,
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchMaxBytes(domainProducerBatchBytes),
		kgo.MaxBufferedRecords(maxClaimLimit*2),
		kgo.MaxBufferedBytes(16<<20),
	)
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create Kafka domain producer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping Kafka domain producer brokers: %w", err)
	}
	return &KafkaProducer{client: client}, nil
}

// ProduceDomain waits for Kafka ACK before returning success to the relay.
func (producer *KafkaProducer) ProduceDomain(ctx context.Context, message Message) error {
	if producer == nil || producer.client == nil {
		return errors.New("kafka domain producer is not initialized")
	}
	record, err := BuildDomainRecord(message)
	if err != nil {
		return err
	}
	if _, err := producer.client.ProduceSync(ctx, record).First(); err != nil {
		return fmt.Errorf("%w: message %s to %s: %v", ErrDomainProduce, message.MessageID, message.Topic, err)
	}
	return nil
}

// ProduceDeadLetter waits for ACK of a bounded JSON envelope that preserves the failed record's identity and routing data.
func (producer *KafkaProducer) ProduceDeadLetter(ctx context.Context, message Message, attempts int, failure string, failedAt time.Time) error {
	if producer == nil || producer.client == nil {
		return errors.New("kafka domain producer is not initialized")
	}
	record, err := BuildDeadLetterRecord(message, attempts, failure, failedAt)
	if err != nil {
		return err
	}
	if _, err := producer.client.ProduceSync(ctx, record).First(); err != nil {
		return fmt.Errorf("%w: dead-letter outbox row %d: %v", ErrDomainProduce, message.ID, err)
	}
	return nil
}

// Close flushes and closes the underlying Kafka client.
func (producer *KafkaProducer) Close() {
	if producer != nil && producer.client != nil {
		producer.client.Close()
	}
}

// BuildDomainRecord validates the stored protobuf and metadata before constructing a Kafka record.
func BuildDomainRecord(message Message) (*kgo.Record, error) {
	headers, event, err := validateDomainMessage(message)
	if err != nil {
		return nil, err
	}
	metadata := domainMetadata(event)
	return &kgo.Record{
		Topic:     message.Topic,
		Key:       []byte(message.PartitionKey),
		Value:     append([]byte(nil), message.Payload...),
		Timestamp: time.UnixMilli(metadata.GetOccurredAtUnixMs()).UTC(),
		Headers:   sortedRecordHeaders(headers),
	}, nil
}

type deadLetterEnvelope struct {
	SchemaVersion          int    `json:"schema_version"`
	OutboxID               int64  `json:"outbox_id"`
	MessageID              string `json:"message_id"`
	CausationID            string `json:"causation_id"`
	OriginalTopic          string `json:"original_topic"`
	OriginalPartitionKey   string `json:"original_partition_key"`
	OriginalHeadersSample  []byte `json:"original_headers_sample"`
	OriginalHeadersBytes   int    `json:"original_headers_bytes"`
	OriginalHeadersSHA256  string `json:"original_headers_sha256"`
	OriginalHeadersTrimmed bool   `json:"original_headers_trimmed"`
	OriginalPayloadSample  []byte `json:"original_payload_sample"`
	OriginalPayloadBytes   int    `json:"original_payload_bytes"`
	OriginalPayloadSHA256  string `json:"original_payload_sha256"`
	OriginalPayloadTrimmed bool   `json:"original_payload_trimmed"`
	Attempts               int    `json:"attempts"`
	Failure                string `json:"failure"`
	FailedAtUnixMs         int64  `json:"failed_at_unix_ms"`
}

// BuildDeadLetterRecord creates a bounded record even when the original row is a poison message.
func BuildDeadLetterRecord(message Message, attempts int, failure string, failedAt time.Time) (*kgo.Record, error) {
	if message.ID <= 0 || attempts <= 0 || failedAt.UnixMilli() <= 0 {
		return nil, fmt.Errorf("%w: dead-letter identity, attempts, and time are required", ErrInvalidDomainMessage)
	}
	payloadSample := message.Payload
	trimmed := len(payloadSample) > maxDeadLetterPayloadSample
	if trimmed {
		payloadSample = payloadSample[:maxDeadLetterPayloadSample]
	}
	digest := sha256.Sum256(message.Payload)
	headersSample := message.HeadersJSON
	headersTrimmed := len(headersSample) > maxDomainHeadersBytes
	if headersTrimmed {
		headersSample = headersSample[:maxDomainHeadersBytes]
	}
	headersDigest := sha256.Sum256(message.HeadersJSON)
	envelope := deadLetterEnvelope{
		SchemaVersion:          1,
		OutboxID:               message.ID,
		MessageID:              message.MessageID,
		CausationID:            message.CausationID,
		OriginalTopic:          message.Topic,
		OriginalPartitionKey:   message.PartitionKey,
		OriginalHeadersSample:  append([]byte(nil), headersSample...),
		OriginalHeadersBytes:   len(message.HeadersJSON),
		OriginalHeadersSHA256:  hex.EncodeToString(headersDigest[:]),
		OriginalHeadersTrimmed: headersTrimmed,
		OriginalPayloadSample:  append([]byte(nil), payloadSample...),
		OriginalPayloadBytes:   len(message.Payload),
		OriginalPayloadSHA256:  hex.EncodeToString(digest[:]),
		OriginalPayloadTrimmed: trimmed,
		Attempts:               attempts,
		Failure:                sanitizeFailure(failure),
		FailedAtUnixMs:         failedAt.UnixMilli(),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal dead-letter envelope: %v", ErrInvalidDomainMessage, err)
	}
	sourceMessageID := safeDeadLetterIdentity(message)
	sourceTopic := safeHeaderValue(message.Topic, "invalid")
	return &kgo.Record{
		Topic:     eventcontract.DomainDLQTopicV1,
		Key:       []byte(sourceMessageID),
		Value:     payload,
		Timestamp: failedAt.UTC(),
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.DeadLetterContentType)},
			{Key: eventcontract.DomainHeaderMessageID, Value: []byte(sourceMessageID)},
			{Key: eventcontract.DeadLetterHeaderSourceTopic, Value: []byte(sourceTopic)},
			{Key: eventcontract.DeadLetterHeaderSourceMessageID, Value: []byte(sourceMessageID)},
			{Key: eventcontract.DeadLetterHeaderErrorClass, Value: []byte("domain_publish_failed")},
		},
	}, nil
}

func validateDomainMessage(message Message) (map[string]string, proto.Message, error) {
	if message.ID <= 0 || !isDomainTopic(message.Topic) || !validBoundedText(message.PartitionKey, 64) || len(message.Payload) == 0 || len(message.Payload) > maxDomainPayloadBytes || len(message.HeadersJSON) == 0 || len(message.HeadersJSON) > maxDomainHeadersBytes || message.CreatedAtMs <= 0 {
		return nil, nil, fmt.Errorf("%w: invalid outbox identity, topic, key, payload, headers, or time", ErrInvalidDomainMessage)
	}
	expectedMessageID, err := eventcontract.DomainMessageID(message.CausationID, message.Topic)
	if err != nil || message.MessageID != expectedMessageID {
		return nil, nil, fmt.Errorf("%w: message_id is not derived from causation_id and topic", ErrInvalidDomainMessage)
	}
	headers := make(map[string]string)
	if err := json.Unmarshal(message.HeadersJSON, &headers); err != nil || len(headers) == 0 || len(headers) > maxDomainHeaders {
		return nil, nil, fmt.Errorf("%w: headers_json must be a bounded string map", ErrInvalidDomainMessage)
	}
	for key, value := range headers {
		if !validHeaderName(key) || !validBoundedText(value, 256) {
			return nil, nil, fmt.Errorf("%w: invalid Kafka header", ErrInvalidDomainMessage)
		}
	}
	if headers[eventcontract.RuntimeHeaderContentType] != eventcontract.DomainEventContentType ||
		headers[eventcontract.DomainHeaderMessageID] != message.MessageID ||
		headers[eventcontract.DomainHeaderCausationID] != message.CausationID ||
		headers[eventcontract.RuntimeHeaderSchemaVersion] != "1" ||
		!validBoundedText(headers[eventcontract.RuntimeHeaderTraceID], 128) {
		return nil, nil, fmt.Errorf("%w: required Kafka headers do not match the outbox row", ErrInvalidDomainMessage)
	}
	event := domainEventForTopic(message.Topic)
	if event == nil {
		return nil, nil, fmt.Errorf("%w: unsupported domain topic", ErrInvalidDomainMessage)
	}
	if err := proto.Unmarshal(message.Payload, event); err != nil {
		return nil, nil, fmt.Errorf("%w: unmarshal %s protobuf: %v", ErrInvalidDomainMessage, message.Topic, err)
	}
	metadata := domainMetadata(event)
	if metadata == nil || metadata.GetMessageId() != message.MessageID || metadata.GetCausationId() != message.CausationID || metadata.GetTraceId() != headers[eventcontract.RuntimeHeaderTraceID] || metadata.GetSchemaVersion() != 1 || metadata.GetOccurredAtUnixMs() != message.CreatedAtMs {
		return nil, nil, fmt.Errorf("%w: protobuf metadata does not match outbox identity", ErrInvalidDomainMessage)
	}
	if err := validateDomainPayload(message, event); err != nil {
		return nil, nil, err
	}
	return headers, event, nil
}

func domainEventForTopic(topic string) proto.Message {
	switch topic {
	case eventcontract.BidAcceptedTopicV1:
		return new(v1.BidAcceptedDomainEventV1)
	case eventcontract.LotSettledTopicV1:
		return new(v1.LotSettledDomainEventV1)
	case eventcontract.OrderCreatedTopicV1:
		return new(v1.OrderCreatedDomainEventV1)
	case eventcontract.LotStateTopicV1:
		return new(v1.LotStateDomainEventV1)
	case eventcontract.OrderEnrichmentTopicV1:
		return new(v1.OrderEnrichmentRequestedDomainEventV1)
	default:
		return nil
	}
}

func domainMetadata(event proto.Message) *v1.DomainEventMetadataV1 {
	switch typed := event.(type) {
	case *v1.BidAcceptedDomainEventV1:
		return typed.GetMetadata()
	case *v1.LotSettledDomainEventV1:
		return typed.GetMetadata()
	case *v1.OrderCreatedDomainEventV1:
		return typed.GetMetadata()
	case *v1.LotStateDomainEventV1:
		return typed.GetMetadata()
	case *v1.OrderEnrichmentRequestedDomainEventV1:
		return typed.GetMetadata()
	default:
		return nil
	}
}

func validateDomainPayload(message Message, event proto.Message) error {
	validID := func(value string) bool { return validBoundedText(value, 64) }
	switch typed := event.(type) {
	case *v1.BidAcceptedDomainEventV1:
		if typed.GetLotId() != message.PartitionKey || !validID(typed.GetRoomId()) || !validID(typed.GetBidId()) || !validID(typed.GetUserId()) || typed.GetAmountFen() <= 0 || !validCurrency(typed.GetCurrency()) || typed.GetLotVersion() <= 0 {
			return fmt.Errorf("%w: invalid bid accepted payload", ErrInvalidDomainMessage)
		}
	case *v1.LotSettledDomainEventV1:
		if typed.GetLotId() != message.PartitionKey || !validID(typed.GetRoomId()) || !validID(typed.GetWinnerUserId()) || !validID(typed.GetOrderId()) || typed.GetFinalPriceFen() <= 0 || !validCurrency(typed.GetCurrency()) || typed.GetLotVersion() <= 0 {
			return fmt.Errorf("%w: invalid lot settled payload", ErrInvalidDomainMessage)
		}
	case *v1.OrderCreatedDomainEventV1:
		if typed.GetOrderId() != message.PartitionKey || !validID(typed.GetLotId()) || !validID(typed.GetRoomId()) || !validID(typed.GetBuyerUserId()) || typed.GetTotalAmountFen() <= 0 || !validCurrency(typed.GetCurrency()) || typed.GetLotVersion() <= 0 {
			return fmt.Errorf("%w: invalid order created payload", ErrInvalidDomainMessage)
		}
	case *v1.LotStateDomainEventV1:
		if typed.GetLotId() != message.PartitionKey || eventcontract.ValidateLotStateDomainEvent(typed) != nil {
			return fmt.Errorf("%w: invalid lot state payload", ErrInvalidDomainMessage)
		}
	case *v1.OrderEnrichmentRequestedDomainEventV1:
		if typed.GetOrderId() != message.PartitionKey || !validID(typed.GetLotId()) {
			return fmt.Errorf("%w: invalid order enrichment payload", ErrInvalidDomainMessage)
		}
	default:
		return fmt.Errorf("%w: unsupported domain payload", ErrInvalidDomainMessage)
	}
	return nil
}

func sortedRecordHeaders(headers map[string]string) []kgo.RecordHeader {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]kgo.RecordHeader, 0, len(keys))
	for _, key := range keys {
		result = append(result, kgo.RecordHeader{Key: key, Value: []byte(headers[key])})
	}
	return result
}

func isDomainTopic(topic string) bool { return domainEventForTopic(topic) != nil }

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

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func safeDeadLetterIdentity(message Message) string {
	if validBoundedText(message.MessageID, 128) {
		return message.MessageID
	}
	return "outbox-" + strconv.FormatInt(message.ID, 10)
}

func safeHeaderValue(value, fallback string) string {
	if validBoundedText(value, 128) {
		return value
	}
	return fallback
}
