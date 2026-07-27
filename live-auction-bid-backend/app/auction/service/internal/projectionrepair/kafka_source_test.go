package projectionrepair

import (
	"context"
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

func TestKafkaSourceReadsBoundsAndRangeThroughInjectedClients(t *testing.T) {
	admin := &repairKafkaAdminStub{earliest: 5, latest: 20}
	reader := &repairKafkaReaderStub{rangePollerStub: rangePollerStub{fetches: []kgo.Fetches{{{
		Topics: []kgo.FetchTopic{{Topic: eventcontract.RuntimeProjectionTopicV1, Partitions: []kgo.FetchPartition{{
			Partition: 2, Records: []*kgo.Record{{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, Offset: 10}},
		}}}},
	}}}}}
	source := &KafkaSource{
		config: kafkaclient.Config{
			Brokers: []string{"127.0.0.1:19092"}, ClientID: "projection-repair-test",
			SecurityProtocol: kafkaclient.SecurityProtocolPlaintext,
		},
		admin: admin,
		newReader: func(...kgo.Opt) (kafkaReader, error) {
			return reader, nil
		},
	}
	bound, err := source.Bounds(context.Background(), 2)
	if err != nil || bound.Earliest != 5 || bound.Latest != 20 || admin.calls != 2 {
		t.Fatalf("bound=%+v calls=%d error=%v", bound, admin.calls, err)
	}
	records, err := source.ReadRange(context.Background(), 2, 10, 11)
	if err != nil || len(records) != 1 || !reader.pinged || !reader.closed {
		t.Fatalf("records=%v pinged=%t closed=%t error=%v", records, reader.pinged, reader.closed, err)
	}
	source.Close()
	if !admin.closed {
		t.Fatal("Kafka admin was not closed")
	}
}

func TestKafkaSourceRejectsInvalidStateRangesAndReaderFactory(t *testing.T) {
	var nilSource *KafkaSource
	if _, err := nilSource.Bounds(context.Background(), 0); err == nil {
		t.Fatal("nil source bounds were accepted")
	}
	if _, err := nilSource.ReadRange(context.Background(), 0, 0, 1); err == nil {
		t.Fatal("nil source range was accepted")
	}
	admin := &repairKafkaAdminStub{earliest: 0, latest: 1}
	source := &KafkaSource{
		config: kafkaclient.Config{
			Brokers: []string{"127.0.0.1:19092"}, ClientID: "projection-repair-test",
			SecurityProtocol: kafkaclient.SecurityProtocolPlaintext,
		},
		admin: admin,
	}
	if _, err := source.Bounds(context.Background(), -1); err == nil {
		t.Fatal("negative partition was accepted")
	}
	if records, err := source.ReadRange(context.Background(), 0, 1, 1); err != nil || len(records) != 0 {
		t.Fatalf("empty range=%v error=%v", records, err)
	}
	if _, err := source.ReadRange(context.Background(), 0, 1, 0); err == nil {
		t.Fatal("backwards range was accepted")
	}
	if _, err := source.ReadRange(context.Background(), 0, 0, 1); err == nil {
		t.Fatal("nil reader factory was accepted")
	}
}

func TestReadRangeRequiresContinuousExpectedRecords(t *testing.T) {
	poller := &rangePollerStub{fetches: []kgo.Fetches{{{
		Topics: []kgo.FetchTopic{{
			Topic: eventcontract.RuntimeProjectionTopicV1,
			Partitions: []kgo.FetchPartition{{Partition: 2, Records: []*kgo.Record{
				{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, Offset: 10},
				{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, Offset: 11},
			}}},
		}},
	}}}}
	records, err := readRange(context.Background(), poller, 2, 10, 12)
	if err != nil {
		t.Fatalf("readRange: %v", err)
	}
	if len(records) != 2 || records[0].Offset != 10 || records[1].Offset != 11 {
		t.Fatalf("records=%v", records)
	}

	gap := &rangePollerStub{fetches: []kgo.Fetches{{{
		Topics: []kgo.FetchTopic{{Topic: eventcontract.RuntimeProjectionTopicV1, Partitions: []kgo.FetchPartition{{
			Partition: 2, Records: []*kgo.Record{{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, Offset: 11}},
		}}}},
	}}}}
	if _, err := readRange(context.Background(), gap, 2, 10, 12); !errors.Is(err, ErrUnsafeReplay) {
		t.Fatalf("gap error=%v", err)
	}
}

func TestReadRangeRejectsInvalidDependenciesAndUnexpectedPartition(t *testing.T) {
	if _, err := readRange(context.Background(), nil, 0, 0, 1); err == nil {
		t.Fatal("nil poller was accepted")
	}
	poller := &rangePollerStub{fetches: []kgo.Fetches{{{
		Topics: []kgo.FetchTopic{{Topic: eventcontract.RuntimeProjectionTopicV1, Partitions: []kgo.FetchPartition{{
			Partition: 3, Records: []*kgo.Record{{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 3, Offset: 0}},
		}}}},
	}}}}
	if _, err := readRange(context.Background(), poller, 2, 0, 1); err == nil {
		t.Fatal("unexpected partition was accepted")
	}
	if minInt64(2, 3) != 2 || minInt64(3, 2) != 2 {
		t.Fatal("minInt64 returned wrong result")
	}
}

type rangePollerStub struct {
	fetches []kgo.Fetches
	index   int
}

type repairKafkaReaderStub struct {
	rangePollerStub
	pinged bool
	closed bool
}

func (stub *repairKafkaReaderStub) Ping(context.Context) error {
	stub.pinged = true
	return nil
}

func (stub *repairKafkaReaderStub) Close() { stub.closed = true }

type repairKafkaAdminStub struct {
	earliest int64
	latest   int64
	calls    int
	closed   bool
}

func (stub *repairKafkaAdminStub) Request(_ context.Context, request kmsg.Request) (kmsg.Response, error) {
	stub.calls++
	list := request.(*kmsg.ListOffsetsRequest)
	response := kmsg.NewPtrListOffsetsResponse()
	for _, topic := range list.Topics {
		responseTopic := kmsg.NewListOffsetsResponseTopic()
		responseTopic.Topic = topic.Topic
		for _, partition := range topic.Partitions {
			responsePartition := kmsg.NewListOffsetsResponseTopicPartition()
			responsePartition.Partition = partition.Partition
			if partition.Timestamp == -2 {
				responsePartition.Offset = stub.earliest
			} else {
				responsePartition.Offset = stub.latest
			}
			responseTopic.Partitions = append(responseTopic.Partitions, responsePartition)
		}
		response.Topics = append(response.Topics, responseTopic)
	}
	return response, nil
}

func (stub *repairKafkaAdminStub) Close() { stub.closed = true }

func (stub *rangePollerStub) PollRecords(context.Context, int) kgo.Fetches {
	if stub.index >= len(stub.fetches) {
		return nil
	}
	result := stub.fetches[stub.index]
	stub.index++
	return result
}
