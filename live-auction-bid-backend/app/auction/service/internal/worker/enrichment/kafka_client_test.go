package enrichment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestBuildDeadLetterRecordPreservesBoundedEvidence(t *testing.T) {
	source := validKafkaRecord(t)
	source.Value = make([]byte, maxDeadLetterPayloadSample+10)
	record, err := BuildDeadLetterRecord(source, "SOURCE_CORRUPT", errors.New("line one\nline two"), time.UnixMilli(1_700_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if record.Topic != eventcontract.DomainDLQTopicV1 || string(record.Key) != "order-1" {
		t.Fatalf("dead letter routing = %+v", record)
	}
	var envelope consumerDeadLetterEnvelope
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.SourcePayloadTrimmed || len(envelope.SourcePayloadSample) != maxDeadLetterPayloadSample || envelope.SourcePayloadBytes != maxDeadLetterPayloadSample+10 {
		t.Fatalf("dead letter payload bounds = %+v", envelope)
	}
	if envelope.ErrorClass != "source_corrupt" || strings.Contains(envelope.Failure, "\n") {
		t.Fatalf("dead letter error fields = %+v", envelope)
	}
}

func TestBuildDeadLetterRecordRejectsIncompleteSource(t *testing.T) {
	if _, err := BuildDeadLetterRecord(nil, "invalid", errors.New("bad"), time.Now()); err == nil {
		t.Fatal("nil source was accepted")
	}
	if got := safeErrorClass("not valid!"); got != "enrichment_error" {
		t.Fatalf("safeErrorClass() = %q", got)
	}
}
