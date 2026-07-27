package esindex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

func TestBuildDeadLetterRecordPreservesBoundedEvidence(t *testing.T) {
	source := validLotStateKafkaRecord(t)
	source.Value = make([]byte, maxDeadLetterPayloadSample+100)
	failedAt := time.UnixMilli(1_700_000_100_000)
	record, err := BuildDeadLetterRecord(source, "document_identity_conflict", errors.New("identity\nconflict"), failedAt)
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
	if envelope.Consumer != "index-es" || !envelope.SourcePayloadTrimmed || len(envelope.SourcePayloadSample) != maxDeadLetterPayloadSample ||
		len(envelope.SourcePayloadSHA256) != 64 || strings.ContainsAny(envelope.Failure, "\r\n") {
		t.Fatalf("envelope=%+v", envelope)
	}
	if _, err := BuildDeadLetterRecord(nil, "", nil, time.Time{}); err == nil {
		t.Fatal("invalid DLQ input was accepted")
	}
}

func TestKafkaClientRejectsInvalidConfigurationAndNilOperations(t *testing.T) {
	if _, err := NewKafkaClient(context.Background(), kafkaclient.Config{}, KafkaClientConfig{}); err == nil {
		t.Fatal("invalid Kafka consumer configuration was accepted")
	}
	var client *KafkaClient
	if err := client.CommitRecord(context.Background(), nil); err == nil {
		t.Fatal("nil commit was accepted")
	}
	if err := client.ProduceDeadLetter(context.Background(), nil, "", errors.New("failure"), time.Now()); err == nil {
		t.Fatal("nil DLQ client was accepted")
	}
	client.Close()
	if safeErrorClass("bad-class") != "elasticsearch_index_error" || sanitizeFailure("\n") != "unknown Elasticsearch index failure" {
		t.Fatal("Kafka evidence sanitizers accepted invalid values")
	}
}
