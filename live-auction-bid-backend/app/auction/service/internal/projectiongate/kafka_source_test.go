package projectiongate

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

type fakeProjectionGateKafkaClient struct {
	mu            sync.Mutex
	partitions    []int32
	earliest      map[int32]int64
	latest        map[int32]int64
	metadataError int16
	requestErr    error
	requests      []string
	fetches       []kgo.Fetches
	added         map[string]map[int32]kgo.Offset
	removed       map[string][]int32
	closed        bool
}

func (client *fakeProjectionGateKafkaClient) Request(
	_ context.Context,
	request kmsg.Request,
) (kmsg.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.requestErr != nil {
		return nil, client.requestErr
	}
	switch value := request.(type) {
	case *kmsg.MetadataRequest:
		client.requests = append(client.requests, "metadata")
		response := kmsg.NewPtrMetadataResponse()
		topic := kmsg.NewMetadataResponseTopic()
		topic.Topic = kmsg.StringPtr(eventcontract.RuntimeProjectionTopicV1)
		topic.ErrorCode = client.metadataError
		for _, partition := range client.partitions {
			item := kmsg.NewMetadataResponseTopicPartition()
			item.Partition = partition
			topic.Partitions = append(topic.Partitions, item)
		}
		response.Topics = []kmsg.MetadataResponseTopic{topic}
		return response, nil
	case *kmsg.ListOffsetsRequest:
		label := "latest"
		values := client.latest
		if len(value.Topics) == 1 && len(value.Topics[0].Partitions) > 0 && value.Topics[0].Partitions[0].Timestamp == -2 {
			label = "earliest"
			values = client.earliest
		}
		client.requests = append(client.requests, label)
		response := kmsg.NewPtrListOffsetsResponse()
		topic := kmsg.NewListOffsetsResponseTopic()
		topic.Topic = eventcontract.RuntimeProjectionTopicV1
		for _, requested := range value.Topics[0].Partitions {
			item := kmsg.NewListOffsetsResponseTopicPartition()
			item.Partition = requested.Partition
			item.Offset = values[requested.Partition]
			topic.Partitions = append(topic.Partitions, item)
		}
		response.Topics = []kmsg.ListOffsetsResponseTopic{topic}
		return response, nil
	default:
		return nil, errors.New("unexpected request")
	}
}

func (client *fakeProjectionGateKafkaClient) AddConsumePartitions(assignments map[string]map[int32]kgo.Offset) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.added = assignments
}

func (client *fakeProjectionGateKafkaClient) RemoveConsumePartitions(partitions map[string][]int32) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.removed = partitions
}

func (client *fakeProjectionGateKafkaClient) PollRecords(context.Context, int) kgo.Fetches {
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.fetches) == 0 {
		return nil
	}
	result := client.fetches[0]
	client.fetches = client.fetches[1:]
	return result
}

func (client *fakeProjectionGateKafkaClient) Close() {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.closed = true
}

func TestKafkaSourceReadsRuntimeTopicBounds(t *testing.T) {
	t.Parallel()

	client := &fakeProjectionGateKafkaClient{
		partitions: []int32{2, 0, 1},
		earliest:   map[int32]int64{0: 10, 1: 20, 2: 30},
		latest:     map[int32]int64{0: 15, 1: 25, 2: 35},
	}
	source := &KafkaSource{client: client}
	bounds, err := source.Bounds(context.Background())
	if err != nil {
		t.Fatalf("Bounds() error = %v", err)
	}
	want := map[int32]PartitionBounds{
		0: {Earliest: 10, Latest: 15},
		1: {Earliest: 20, Latest: 25},
		2: {Earliest: 30, Latest: 35},
	}
	if !reflect.DeepEqual(bounds, want) {
		t.Fatalf("Bounds() = %v, want %v", bounds, want)
	}
	if !reflect.DeepEqual(client.requests, []string{"metadata", "earliest", "latest"}) {
		t.Fatalf("Kafka requests = %v", client.requests)
	}
}

func TestKafkaSourceReadsOldestUnprojectedRecordPerPartition(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	fetches := kgo.Fetches{{
		Topics: []kgo.FetchTopic{{
			Topic: eventcontract.RuntimeProjectionTopicV1,
			Partitions: []kgo.FetchPartition{
				{Partition: 0, Records: []*kgo.Record{{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 0, Offset: 10, Timestamp: now}}},
				{Partition: 2, Records: []*kgo.Record{{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, Offset: 31, Timestamp: now.Add(-time.Second)}}},
			},
		}},
	}}
	client := &fakeProjectionGateKafkaClient{fetches: []kgo.Fetches{fetches}}
	source := &KafkaSource{client: client}
	records, err := source.OldestRecords(context.Background(), map[int32]int64{0: 10, 2: 30})
	if err != nil {
		t.Fatalf("OldestRecords() error = %v", err)
	}
	if records[0].Offset != 10 || !records[0].Timestamp.Equal(now) {
		t.Fatalf("partition 0 record = %+v", records[0])
	}
	if records[2].Offset != 31 {
		t.Fatalf("partition 2 record = %+v, want broker-reported gap for guard classification", records[2])
	}
	if got := client.added[eventcontract.RuntimeProjectionTopicV1][2].EpochOffset(); got.Offset != 30 || got.Epoch != -1 {
		t.Fatalf("partition 2 assignment = %+v", got)
	}
	if !reflect.DeepEqual(client.removed[eventcontract.RuntimeProjectionTopicV1], []int32{0, 2}) {
		t.Fatalf("removed partitions = %v", client.removed)
	}
}

func TestKafkaSourceRejectsInvalidMetadataAndFetches(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		client *fakeProjectionGateKafkaClient
	}{
		{name: "no partitions", client: &fakeProjectionGateKafkaClient{}},
		{name: "duplicate partition", client: &fakeProjectionGateKafkaClient{partitions: []int32{0, 0}}},
		{name: "negative partition", client: &fakeProjectionGateKafkaClient{partitions: []int32{-1}}},
		{name: "topic authorization", client: &fakeProjectionGateKafkaClient{partitions: []int32{0}, metadataError: int16(kerr.TopicAuthorizationFailed.Code)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (&KafkaSource{client: test.client}).Bounds(context.Background()); err == nil {
				t.Fatal("Bounds() error = nil, want invalid metadata error")
			}
		})
	}

	if _, err := (&KafkaSource{client: &fakeProjectionGateKafkaClient{}}).OldestRecords(
		context.Background(),
		map[int32]int64{-1: 0},
	); err == nil {
		t.Fatal("OldestRecords() accepted a negative partition")
	}
	fetches := kgo.Fetches{{
		Topics: []kgo.FetchTopic{{Topic: eventcontract.RuntimeProjectionTopicV1, Partitions: []kgo.FetchPartition{{
			Partition: 0,
			Err:       kerr.TopicAuthorizationFailed,
		}}}},
	}}
	fetchErrorClient := &fakeProjectionGateKafkaClient{fetches: []kgo.Fetches{fetches}}
	if _, err := (&KafkaSource{client: fetchErrorClient}).OldestRecords(
		context.Background(),
		map[int32]int64{0: 0},
	); !errors.Is(err, kerr.TopicAuthorizationFailed) {
		t.Fatalf("OldestRecords() error = %v, want authorization error", err)
	}
}

func TestKafkaSourceRejectsInvalidConfigurationAndCloses(t *testing.T) {
	t.Parallel()

	if _, err := NewKafkaSource(kafkaclient.Config{}); !errors.Is(err, kafkaclient.ErrInvalidConfig) {
		t.Fatalf("NewKafkaSource() error = %v, want invalid config", err)
	}
	client := &fakeProjectionGateKafkaClient{}
	source := &KafkaSource{client: client}
	source.Close()
	if !client.closed {
		t.Fatal("Close() did not close Kafka client")
	}
}
