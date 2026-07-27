package projector

import (
	"context"
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestResolveAssignmentOffsetsUsesDatabaseWithinKafkaBounds(t *testing.T) {
	assignments := map[string][]int32{eventcontract.RuntimeProjectionTopicV1: {0, 1}}
	store := &offsetInitializerStub{next: map[int32]int64{0: 12, 1: 25}}
	reader := boundsReaderStub{bounds: map[TopicPartition]PartitionBounds{
		{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 0}: {Earliest: 10, Latest: 20},
		{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 1}: {Earliest: 20, Latest: 30},
	}}
	resolved, err := ResolveAssignmentOffsets(context.Background(), assignments, store, reader)
	if err != nil {
		t.Fatalf("ResolveAssignmentOffsets: %v", err)
	}
	if got := resolved[eventcontract.RuntimeProjectionTopicV1][0].EpochOffset(); got.Offset != 12 || got.Epoch != -1 {
		t.Fatalf("partition 0 offset=%+v", got)
	}
	if got := resolved[eventcontract.RuntimeProjectionTopicV1][1].EpochOffset(); got.Offset != 25 {
		t.Fatalf("partition 1 offset=%+v", got)
	}
	if store.earliest[0] != 10 || store.earliest[1] != 20 {
		t.Fatalf("initialized earliest offsets=%v", store.earliest)
	}
}

func TestResolveAssignmentOffsetsRejectsRetentionCliffAndInvalidAssignments(t *testing.T) {
	assignment := map[string][]int32{eventcontract.RuntimeProjectionTopicV1: {0}}
	reader := boundsReaderStub{bounds: map[TopicPartition]PartitionBounds{
		{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 0}: {Earliest: 10, Latest: 20},
	}}
	for _, next := range []int64{9, 21} {
		_, err := ResolveAssignmentOffsets(context.Background(), assignment, &offsetInitializerStub{next: map[int32]int64{0: next}}, reader)
		if !errors.Is(err, ErrRetentionCliff) {
			t.Fatalf("next=%d error=%v want ErrRetentionCliff", next, err)
		}
	}
	if _, err := ResolveAssignmentOffsets(context.Background(), map[string][]int32{"other": {0}}, &offsetInitializerStub{}, reader); !errors.Is(err, ErrInvalidApplyRecord) {
		t.Fatalf("wrong topic error=%v", err)
	}
	if _, err := ResolveAssignmentOffsets(context.Background(), assignment, nil, reader); err == nil {
		t.Fatal("nil offset store was accepted")
	}
	if resolved, err := ResolveAssignmentOffsets(context.Background(), nil, &offsetInitializerStub{}, reader); err != nil || len(resolved) != 0 {
		t.Fatalf("empty assignment result=%v error=%v", resolved, err)
	}
}

func TestKafkaBoundsReaderReadsEarliestAndLatest(t *testing.T) {
	requester := &listOffsetsRequesterStub{earliest: 7, latest: 19}
	reader := KafkaBoundsReader{Requester: requester}
	key := TopicPartition{Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2}
	bounds, err := reader.ReadBounds(context.Background(), map[string][]int32{key.Topic: {key.Partition}})
	if err != nil {
		t.Fatalf("ReadBounds: %v", err)
	}
	if bounds[key] != (PartitionBounds{Earliest: 7, Latest: 19}) || requester.calls != 2 {
		t.Fatalf("bounds=%v calls=%d", bounds, requester.calls)
	}
	requester.errorCode = int16(kerr.TopicAuthorizationFailed.Code)
	if _, err := reader.ReadBounds(context.Background(), map[string][]int32{key.Topic: {key.Partition}}); !errors.Is(err, kerr.TopicAuthorizationFailed) {
		t.Fatalf("broker error=%v", err)
	}
	if _, err := (KafkaBoundsReader{}).ReadBounds(context.Background(), map[string][]int32{}); err == nil {
		t.Fatal("nil requester was accepted")
	}
}

type offsetInitializerStub struct {
	next     map[int32]int64
	earliest map[int32]int64
	err      error
}

func (stub *offsetInitializerStub) EnsurePartitionOffset(_ context.Context, _ string, partition int32, earliest int64) (int64, error) {
	if stub.err != nil {
		return 0, stub.err
	}
	if stub.earliest == nil {
		stub.earliest = make(map[int32]int64)
	}
	stub.earliest[partition] = earliest
	return stub.next[partition], nil
}

type boundsReaderStub struct {
	bounds map[TopicPartition]PartitionBounds
	err    error
}

func (stub boundsReaderStub) ReadBounds(context.Context, map[string][]int32) (map[TopicPartition]PartitionBounds, error) {
	return stub.bounds, stub.err
}

type listOffsetsRequesterStub struct {
	earliest  int64
	latest    int64
	errorCode int16
	calls     int
}

func (stub *listOffsetsRequesterStub) Request(_ context.Context, request kmsg.Request) (kmsg.Response, error) {
	stub.calls++
	listRequest := request.(*kmsg.ListOffsetsRequest)
	response := kmsg.NewPtrListOffsetsResponse()
	for _, topic := range listRequest.Topics {
		responseTopic := kmsg.NewListOffsetsResponseTopic()
		responseTopic.Topic = topic.Topic
		for _, partition := range topic.Partitions {
			responsePartition := kmsg.NewListOffsetsResponseTopicPartition()
			responsePartition.Partition = partition.Partition
			responsePartition.ErrorCode = stub.errorCode
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
