package esindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

const maxDeadLetterPayloadSample = 256 << 10

type KafkaClientConfig struct {
	GroupID           string
	SessionTimeout    time.Duration
	HeartbeatInterval time.Duration
}

type KafkaClient struct{ client *kgo.Client }

func NewKafkaClient(ctx context.Context, kafkaConfig kafkaclient.Config, config KafkaClientConfig) (*KafkaClient, error) {
	config.GroupID = strings.TrimSpace(config.GroupID)
	if !searchstate.ValidText(config.GroupID, 128) || config.SessionTimeout <= 0 || config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.SessionTimeout {
		return nil, errors.New("elasticsearch Kafka group and heartbeat configuration is invalid")
	}
	options, err := kafkaConfig.Options()
	if err != nil {
		return nil, err
	}
	options = append(options,
		kgo.ConsumerGroup(config.GroupID),
		kgo.ConsumeTopics(eventcontract.LotStateTopicV1),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
		kgo.SessionTimeout(config.SessionTimeout),
		kgo.HeartbeatInterval(config.HeartbeatInterval),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.MaxBufferedRecords(1000),
		kgo.MaxBufferedBytes(16<<20),
	)
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create Elasticsearch Kafka client: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping Kafka for Elasticsearch consumer: %w", err)
	}
	return &KafkaClient{client: client}, nil
}

func (client *KafkaClient) PollRecords(ctx context.Context, maxRecords int) kgo.Fetches {
	return client.client.PollRecords(ctx, maxRecords)
}

func (client *KafkaClient) AllowRebalance() { client.client.AllowRebalance() }

func (client *KafkaClient) CommitRecord(ctx context.Context, record *kgo.Record) error {
	if client == nil || client.client == nil || record == nil {
		return errors.New("elasticsearch Kafka commit client and record are required")
	}
	if err := client.client.CommitRecords(ctx, record); err != nil {
		return fmt.Errorf("commit Elasticsearch Kafka record %s: %w", searchstate.Position(record), err)
	}
	return nil
}

func (client *KafkaClient) ProduceDeadLetter(ctx context.Context, source *kgo.Record, errorClass string, cause error, failedAt time.Time) error {
	if client == nil || client.client == nil {
		return errors.New("elasticsearch DLQ client is required")
	}
	record, err := BuildDeadLetterRecord(source, errorClass, cause, failedAt)
	if err != nil {
		return err
	}
	if _, err := client.client.ProduceSync(ctx, record).First(); err != nil {
		return fmt.Errorf("produce Elasticsearch dead letter for %s: %w", searchstate.Position(source), err)
	}
	return nil
}

type deadLetterEnvelope struct {
	SchemaVersion        int    `json:"schema_version"`
	Consumer             string `json:"consumer"`
	SourceTopic          string `json:"source_topic"`
	SourcePartition      int32  `json:"source_partition"`
	SourceOffset         int64  `json:"source_offset"`
	SourceKey            []byte `json:"source_key"`
	SourceMessageID      string `json:"source_message_id"`
	SourcePayloadSample  []byte `json:"source_payload_sample"`
	SourcePayloadBytes   int    `json:"source_payload_bytes"`
	SourcePayloadSHA256  string `json:"source_payload_sha256"`
	SourcePayloadTrimmed bool   `json:"source_payload_trimmed"`
	ErrorClass           string `json:"error_class"`
	Failure              string `json:"failure"`
	FailedAtUnixMs       int64  `json:"failed_at_unix_ms"`
}

func BuildDeadLetterRecord(source *kgo.Record, errorClass string, cause error, failedAt time.Time) (*kgo.Record, error) {
	if source == nil || cause == nil || failedAt.UnixMilli() <= 0 || source.Topic == "" || source.Partition < 0 || source.Offset < 0 {
		return nil, errors.New("elasticsearch DLQ source, cause, and time are required")
	}
	messageID := sourceHeader(source, eventcontract.DomainHeaderMessageID)
	if !searchstate.ValidText(messageID, 128) {
		messageID = searchstate.Position(source)
	}
	payloadSample := source.Value
	trimmed := len(payloadSample) > maxDeadLetterPayloadSample
	if trimmed {
		payloadSample = payloadSample[:maxDeadLetterPayloadSample]
	}
	digest := sha256.Sum256(source.Value)
	envelope := deadLetterEnvelope{
		SchemaVersion: 1, Consumer: "index-es", SourceTopic: source.Topic,
		SourcePartition: source.Partition, SourceOffset: source.Offset, SourceKey: append([]byte(nil), source.Key...),
		SourceMessageID: messageID, SourcePayloadSample: append([]byte(nil), payloadSample...),
		SourcePayloadBytes: len(source.Value), SourcePayloadSHA256: hex.EncodeToString(digest[:]), SourcePayloadTrimmed: trimmed,
		ErrorClass: safeErrorClass(errorClass), Failure: sanitizeFailure(cause.Error()), FailedAtUnixMs: failedAt.UnixMilli(),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal Elasticsearch dead letter: %w", err)
	}
	return &kgo.Record{
		Topic: eventcontract.DomainDLQTopicV1, Key: append([]byte(nil), source.Key...), Value: payload, Timestamp: failedAt.UTC(),
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.DeadLetterContentType)},
			{Key: eventcontract.DomainHeaderMessageID, Value: []byte(messageID)},
			{Key: eventcontract.DeadLetterHeaderSourceTopic, Value: []byte(source.Topic)},
			{Key: eventcontract.DeadLetterHeaderSourceMessageID, Value: []byte(messageID)},
			{Key: eventcontract.DeadLetterHeaderErrorClass, Value: []byte(envelope.ErrorClass)},
		},
	}, nil
}

func (client *KafkaClient) Close() {
	if client != nil && client.client != nil {
		client.client.Close()
	}
}

func sourceHeader(record *kgo.Record, key string) string {
	if record == nil {
		return ""
	}
	for _, header := range record.Headers {
		if header.Key == key {
			return string(header.Value)
		}
	}
	return ""
}

func safeErrorClass(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return "elasticsearch_index_error"
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return "elasticsearch_index_error"
		}
	}
	return value
}

func sanitizeFailure(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\x00' {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	if len(value) > 512 {
		value = value[:512]
	}
	if value == "" {
		return "unknown Elasticsearch index failure"
	}
	return value
}
