package searchreconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

func TestBuildRepairRecordPreservesCanonicalIdentity(t *testing.T) {
	payload := repairPayloadFixture(t)
	record, err := BuildRepairRecord(payload)
	if err != nil {
		t.Fatal(err)
	}
	record.Partition = 0
	record.Offset = 1
	decoded, err := searchstate.DecodeRecord(record)
	if err != nil || decoded.Document.LotID != "lot-1" || decoded.Document.LotVersion != 7 || string(record.Value) != string(payload) {
		t.Fatalf("record=%+v decoded=%+v error=%v", record, decoded, err)
	}
	if len(record.Headers) != 5 {
		t.Fatalf("headers=%+v", record.Headers)
	}
	for index := 1; index < len(record.Headers); index++ {
		if record.Headers[index-1].Key > record.Headers[index].Key {
			t.Fatal("repair headers are not deterministic")
		}
	}
}

func TestKafkaRepairPublisherWaitsForAcknowledgement(t *testing.T) {
	client := &fakeRepairKafkaClient{}
	publisher := &KafkaRepairPublisher{client: client}
	if err := publisher.Publish(context.Background(), repairPayloadFixture(t)); err != nil || len(client.records) != 1 {
		t.Fatalf("records=%d error=%v", len(client.records), err)
	}
	client.err = errors.New("not enough replicas")
	if err := publisher.Publish(context.Background(), repairPayloadFixture(t)); err == nil {
		t.Fatal("Kafka acknowledgement failure was ignored")
	}
}

func repairPayloadFixture(t *testing.T) []byte {
	t.Helper()
	causationID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := eventcontract.DomainMessageID(causationID, eventcontract.LotStateTopicV1)
	if err != nil {
		t.Fatal(err)
	}
	event := &v1.LotStateDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{
			MessageId: messageID, CausationId: causationID, TraceId: "trace-1", SchemaVersion: 1, OccurredAtUnixMs: 1_700_000_000_000,
		},
		LotId: "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: 7,
		Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "Jade vase", Description: "Vintage", Category: "jewelry",
		StartPriceFen: 10_000, CurrentPriceFen: 12_000, Currency: "CNY", StartsAtUnixMs: 100, EndsAtUnixMs: 200,
	}
	event.ContentHash, err = eventcontract.LotStateContentHash(event)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

type fakeRepairKafkaClient struct {
	records []*kgo.Record
	err     error
}

func (*fakeRepairKafkaClient) Ping(context.Context) error { return nil }
func (client *fakeRepairKafkaClient) ProduceSync(_ context.Context, records ...*kgo.Record) kgo.ProduceResults {
	client.records = append(client.records, records...)
	results := make(kgo.ProduceResults, 0, len(records))
	for _, record := range records {
		results = append(results, kgo.ProduceResult{Record: record, Err: client.err})
	}
	return results
}
func (*fakeRepairKafkaClient) Close() {}
