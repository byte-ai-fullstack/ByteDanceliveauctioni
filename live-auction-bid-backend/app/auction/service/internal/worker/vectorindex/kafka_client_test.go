package vectorindex

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestBuildDeadLetterRecordPreservesBoundedEvidence(t *testing.T) {
	source := validLotStateKafkaRecord(t)
	source.Value = make([]byte, maxDeadLetterPayloadSample+100)
	failedAt := time.UnixMilli(1_700_000_100_000)
	record, err := BuildDeadLetterRecord(source, "embedding_failed", errors.New("provider\nunavailable"), failedAt)
	if err != nil {
		t.Fatalf("BuildDeadLetterRecord: %v", err)
	}
	if record.Topic != eventcontract.DomainDLQTopicV1 || string(record.Key) != "lot-1" {
		t.Fatalf("record=%+v", record)
	}
	var envelope deadLetterEnvelope
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Consumer != "index-pgvector" || !envelope.SourcePayloadTrimmed || len(envelope.SourcePayloadSample) != maxDeadLetterPayloadSample ||
		len(envelope.SourcePayloadSHA256) != 64 || strings.ContainsAny(envelope.Failure, "\r\n") {
		t.Fatalf("envelope=%+v", envelope)
	}
	if _, err := BuildDeadLetterRecord(nil, "", nil, time.Time{}); err == nil {
		t.Fatal("invalid DLQ input was accepted")
	}
}
