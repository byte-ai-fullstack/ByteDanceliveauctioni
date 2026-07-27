package projector

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

var ErrInvalidRuntimeRecord = errors.New("invalid Kafka runtime record")

type DecodedRecord struct {
	Topic       string
	Partition   int32
	Offset      int64
	Fact        *v1.RuntimeFactV1
	Payload     []byte
	PayloadHash string
	OwnerEpoch  int64
	OutboxShard int
}

// DecodeRecord validates Kafka metadata and the protobuf RuntimeFact before any database transaction begins.
func DecodeRecord(record *kgo.Record) (DecodedRecord, error) {
	if record == nil {
		return DecodedRecord{}, fmt.Errorf("%w: record is required", ErrInvalidRuntimeRecord)
	}
	if record.Topic != eventcontract.RuntimeProjectionTopicV1 {
		return DecodedRecord{}, fmt.Errorf("%w: unexpected topic %q", ErrInvalidRuntimeRecord, record.Topic)
	}
	if record.Partition < 0 || record.Offset < 0 {
		return DecodedRecord{}, fmt.Errorf("%w: partition and offset must be non-negative", ErrInvalidRuntimeRecord)
	}
	if len(record.Value) == 0 || len(record.Value) > eventcontract.MaxRuntimeFactBytes {
		return DecodedRecord{}, fmt.Errorf("%w: payload size %d is outside the allowed range", ErrInvalidRuntimeRecord, len(record.Value))
	}
	fact := new(v1.RuntimeFactV1)
	if err := proto.Unmarshal(record.Value, fact); err != nil {
		return DecodedRecord{}, fmt.Errorf("%w: unmarshal protobuf: %v", ErrInvalidRuntimeRecord, err)
	}
	if err := eventcontract.ValidateRuntimeFact(fact); err != nil {
		return DecodedRecord{}, fmt.Errorf("%w: %v", ErrInvalidRuntimeRecord, err)
	}
	if string(record.Key) != fact.GetLotId() {
		return DecodedRecord{}, fmt.Errorf("%w: Kafka key does not match lot_id", ErrInvalidRuntimeRecord)
	}

	headers, err := runtimeHeaders(record.Headers)
	if err != nil {
		return DecodedRecord{}, err
	}
	if headers[eventcontract.RuntimeHeaderContentType] != eventcontract.RuntimeFactContentType {
		return DecodedRecord{}, fmt.Errorf("%w: unsupported content_type", ErrInvalidRuntimeRecord)
	}
	if headers[eventcontract.RuntimeHeaderEventID] != fact.GetEventId() {
		return DecodedRecord{}, fmt.Errorf("%w: event_id header mismatch", ErrInvalidRuntimeRecord)
	}
	if headers[eventcontract.RuntimeHeaderTraceID] != fact.GetTraceId() {
		return DecodedRecord{}, fmt.Errorf("%w: trace_id header mismatch", ErrInvalidRuntimeRecord)
	}
	if headers[eventcontract.RuntimeHeaderSchemaVersion] != strconv.FormatUint(uint64(fact.GetSchemaVersion()), 10) {
		return DecodedRecord{}, fmt.Errorf("%w: schema_version header mismatch", ErrInvalidRuntimeRecord)
	}
	if headers[eventcontract.RuntimeHeaderLotVersion] != strconv.FormatInt(fact.GetLotVersion(), 10) {
		return DecodedRecord{}, fmt.Errorf("%w: lot_version header mismatch", ErrInvalidRuntimeRecord)
	}
	ownerEpoch, err := parseCanonicalPositiveInt64(headers[eventcontract.RuntimeHeaderOwnerEpoch])
	if err != nil {
		return DecodedRecord{}, fmt.Errorf("%w: invalid owner_epoch header", ErrInvalidRuntimeRecord)
	}
	outboxShard64, err := parseCanonicalNonNegativeInt64(headers[eventcontract.RuntimeHeaderOutboxShard])
	if err != nil || outboxShard64 >= data.RuntimeOutboxShardCount {
		return DecodedRecord{}, fmt.Errorf("%w: invalid outbox_shard header", ErrInvalidRuntimeRecord)
	}

	payload := append([]byte(nil), record.Value...)
	hash := sha256.Sum256(payload)
	return DecodedRecord{
		Topic:       record.Topic,
		Partition:   record.Partition,
		Offset:      record.Offset,
		Fact:        fact,
		Payload:     payload,
		PayloadHash: hex.EncodeToString(hash[:]),
		OwnerEpoch:  ownerEpoch,
		OutboxShard: int(outboxShard64),
	}, nil
}

func runtimeHeaders(values []kgo.RecordHeader) (map[string]string, error) {
	required := map[string]struct{}{
		eventcontract.RuntimeHeaderContentType:   {},
		eventcontract.RuntimeHeaderEventID:       {},
		eventcontract.RuntimeHeaderTraceID:       {},
		eventcontract.RuntimeHeaderSchemaVersion: {},
		eventcontract.RuntimeHeaderLotVersion:    {},
		eventcontract.RuntimeHeaderOwnerEpoch:    {},
		eventcontract.RuntimeHeaderOutboxShard:   {},
	}
	result := make(map[string]string, len(required))
	for _, header := range values {
		if _, tracked := required[header.Key]; !tracked {
			continue
		}
		if _, duplicate := result[header.Key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate %s header", ErrInvalidRuntimeRecord, header.Key)
		}
		result[header.Key] = string(header.Value)
	}
	for name := range required {
		if _, exists := result[name]; !exists {
			return nil, fmt.Errorf("%w: missing %s header", ErrInvalidRuntimeRecord, name)
		}
	}
	return result, nil
}

func parseCanonicalPositiveInt64(value string) (int64, error) {
	parsed, err := parseCanonicalNonNegativeInt64(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("expected canonical positive integer")
	}
	return parsed, nil
}

func parseCanonicalNonNegativeInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("expected canonical non-negative integer")
	}
	return parsed, nil
}
