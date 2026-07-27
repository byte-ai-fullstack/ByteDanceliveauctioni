package searchrebuild

import (
	"context"
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

func TestReadLotStateBoundsReturnsEveryPartition(t *testing.T) {
	requester := &lotStateKafkaRequesterStub{earliest: 3, latest: 11}
	bounds, err := ReadLotStateBounds(context.Background(), requester)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounds) != 2 || bounds[0] != (PartitionBounds{Earliest: 3, Latest: 11}) || bounds[1].Latest != 11 {
		t.Fatalf("bounds=%v", bounds)
	}
	if err := ValidateCatchupStarts(map[int32]int64{0: 5, 1: 3}, bounds); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatchupStarts(map[int32]int64{0: 2, 1: 3}, bounds); !errors.Is(err, ErrKafkaRetentionCliff) {
		t.Fatalf("retention error=%v", err)
	}
}

func TestKafkaCatchupAppliesContiguousRecordsAndTracksNextOffsets(t *testing.T) {
	poller := &catchupPollerStub{fetches: []kgo.Fetches{lotStateFetches(
		lotStateRecord(t, 0, 5, 6),
		lotStateRecord(t, 0, 6, 7),
	)}}
	catchup := newKafkaCatchup(poller, map[int32]int64{0: 5})
	var versions []int64
	applied, err := catchup.CatchUpTo(context.Background(), map[int32]int64{0: 7}, func(_ context.Context, record searchstate.Record) error {
		versions = append(versions, record.Event.GetLotVersion())
		return nil
	})
	if err != nil || applied != 2 || len(versions) != 2 || versions[0] != 6 || versions[1] != 7 || catchup.Positions()[0] != 7 {
		t.Fatalf("applied=%d versions=%v positions=%v error=%v", applied, versions, catchup.Positions(), err)
	}
}

func TestKafkaCatchupRejectsOffsetGapBeforeApplying(t *testing.T) {
	poller := &catchupPollerStub{fetches: []kgo.Fetches{lotStateFetches(lotStateRecord(t, 0, 6, 7))}}
	catchup := newKafkaCatchup(poller, map[int32]int64{0: 5})
	if _, err := catchup.CatchUpTo(context.Background(), map[int32]int64{0: 7}, func(context.Context, searchstate.Record) error {
		return nil
	}); !errors.Is(err, ErrKafkaCatchupGap) {
		t.Fatalf("gap error=%v", err)
	}
}

type lotStateKafkaRequesterStub struct {
	earliest int64
	latest   int64
}

func (stub *lotStateKafkaRequesterStub) Request(_ context.Context, request kmsg.Request) (kmsg.Response, error) {
	switch typed := request.(type) {
	case *kmsg.MetadataRequest:
		response := kmsg.NewPtrMetadataResponse()
		topic := kmsg.NewMetadataResponseTopic()
		topic.Topic = kmsg.StringPtr(eventcontract.LotStateTopicV1)
		for _, partitionID := range []int32{1, 0} {
			partition := kmsg.NewMetadataResponseTopicPartition()
			partition.Partition = partitionID
			topic.Partitions = append(topic.Partitions, partition)
		}
		response.Topics = []kmsg.MetadataResponseTopic{topic}
		return response, nil
	case *kmsg.ListOffsetsRequest:
		response := kmsg.NewPtrListOffsetsResponse()
		for _, requestTopic := range typed.Topics {
			topic := kmsg.NewListOffsetsResponseTopic()
			topic.Topic = requestTopic.Topic
			for _, requestPartition := range requestTopic.Partitions {
				partition := kmsg.NewListOffsetsResponseTopicPartition()
				partition.Partition = requestPartition.Partition
				if requestPartition.Timestamp == -2 {
					partition.Offset = stub.earliest
				} else {
					partition.Offset = stub.latest
				}
				topic.Partitions = append(topic.Partitions, partition)
			}
			response.Topics = append(response.Topics, topic)
		}
		return response, nil
	default:
		return nil, errors.New("unexpected request")
	}
}

type catchupPollerStub struct {
	fetches []kgo.Fetches
	index   int
}

func (stub *catchupPollerStub) PollRecords(context.Context, int) kgo.Fetches {
	if stub.index >= len(stub.fetches) {
		return nil
	}
	result := stub.fetches[stub.index]
	stub.index++
	return result
}

func lotStateFetches(records ...*kgo.Record) kgo.Fetches {
	return kgo.Fetches{{Topics: []kgo.FetchTopic{{
		Topic:      eventcontract.LotStateTopicV1,
		Partitions: []kgo.FetchPartition{{Partition: 0, Records: records}},
	}}}}
}

func lotStateRecord(t *testing.T, partition int32, offset, version int64) *kgo.Record {
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
		LotId: "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: version,
		Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "翡翠手镯", Description: "冰糯种", Category: "珠宝",
		Tags: []string{"翡翠"}, ImageUrl: "https://example.test/lot.jpg", StartPriceFen: 10_000,
		CurrentPriceFen: 12_000, Currency: "CNY", StartsAtUnixMs: 100, EndsAtUnixMs: 200,
	}
	event.ContentHash, err = eventcontract.LotStateContentHash(event)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: eventcontract.LotStateTopicV1, Partition: partition, Offset: offset,
		Key: []byte(event.GetLotId()), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.DomainEventContentType)},
			{Key: eventcontract.DomainHeaderMessageID, Value: []byte(messageID)},
			{Key: eventcontract.DomainHeaderCausationID, Value: []byte(causationID)},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte("trace-1")},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
		},
	}
}
