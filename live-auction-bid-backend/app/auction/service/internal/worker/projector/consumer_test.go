package projector

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestConsumerProcessesPartitionsConcurrentlyAndRecordsSequentially(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &projectionConsumerClientStub{cancel: cancel}
	first := runtimeRecordFixture(t)
	second := runtimeRecordFixture(t)
	second.Offset = first.Offset + 1
	third := runtimeRecordFixture(t)
	third.Partition = 5
	third.Offset = 7
	client.fetches = []kgo.Fetches{fetchRecords(first, second, third)}
	store := &projectionApplierStub{}
	consumer, err := NewConsumer(client, store, ConsumerConfig{MaxPollRecords: 100})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	store.mu.Lock()
	partitionFour := append([]int64(nil), store.applied[4]...)
	partitionFive := append([]int64(nil), store.applied[5]...)
	store.mu.Unlock()
	if !equalOffsets(partitionFour, []int64{first.Offset, second.Offset}) || !equalOffsets(partitionFive, []int64{third.Offset}) {
		t.Fatalf("applied offsets p4=%v p5=%v", partitionFour, partitionFive)
	}
	client.mu.Lock()
	committed := append([]int64(nil), client.committed...)
	allowCalls := client.allowCalls
	client.mu.Unlock()
	sort.Slice(committed, func(i, j int) bool { return committed[i] < committed[j] })
	if !equalOffsets(committed, []int64{8, 100, 101}) || allowCalls < 2 {
		t.Fatalf("committed=%v allow calls=%d", committed, allowCalls)
	}
}

func TestConsumerPausesOnlyFailedPartitionAndPersistsGapFinding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &projectionConsumerClientStub{cancel: cancel}
	failed := runtimeRecordFixture(t)
	failed.Partition = 1
	failed.Offset = 10
	succeeded := runtimeRecordFixture(t)
	succeeded.Partition = 2
	succeeded.Offset = 20
	client.fetches = []kgo.Fetches{fetchRecords(failed, succeeded)}
	store := &projectionApplierStub{failPartition: 1, applyErr: ErrRuntimeProjectionGap}
	consumer, _ := NewConsumer(client, store, ConsumerConfig{MaxPollRecords: 10})
	if consumer.Ready() {
		t.Fatal("consumer must not be ready before its first successful Kafka poll")
	}
	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	client.mu.Lock()
	paused := append([]TopicPartition(nil), client.paused...)
	client.mu.Unlock()
	if len(paused) != 1 || paused[0].Partition != 1 {
		t.Fatalf("paused=%v", paused)
	}
	pausedStatus := consumer.PausedPartitions()
	if consumer.Ready() || pausedStatus[TopicPartition{Topic: failed.Topic, Partition: 1}] != "version_gap" {
		t.Fatalf("ready=%v paused status=%v", consumer.Ready(), pausedStatus)
	}
	store.mu.Lock()
	findings := append([]findingCall(nil), store.findings...)
	store.mu.Unlock()
	if len(findings) != 1 || findings[0].kind != FindingRuntimeVersionGap || findings[0].severity != FindingSeverityP1 || findings[0].freeze {
		t.Fatalf("findings=%v", findings)
	}
}

func TestConsumerReadyWhileFirstPollBlocksOnEmptyTopic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &blockingProjectionConsumerClient{entered: make(chan struct{})}
	consumer, err := NewConsumer(client, &projectionApplierStub{}, ConsumerConfig{MaxPollRecords: 10})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("consumer did not start polling")
	}
	if !consumer.Ready() {
		t.Fatal("consumer should report ready once the Kafka poll loop is live")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop")
	}
}

func TestConsumerReturnsFetchErrorAndValidatesConfiguration(t *testing.T) {
	fetchErr := errors.New("authorization failed")
	client := &projectionConsumerClientStub{fetches: []kgo.Fetches{
		{{Topics: []kgo.FetchTopic{{Topic: "topic", Partitions: []kgo.FetchPartition{{Partition: 3, Err: fetchErr}}}}}},
	}}
	consumer, _ := NewConsumer(client, &projectionApplierStub{}, ConsumerConfig{MaxPollRecords: 10})
	if err := consumer.Run(context.Background()); !errors.Is(err, fetchErr) {
		t.Fatalf("Run error=%v", err)
	}
	if _, err := NewConsumer(nil, &projectionApplierStub{}, ConsumerConfig{MaxPollRecords: 1}); err == nil {
		t.Fatal("nil client was accepted")
	}
	if _, err := NewConsumer(client, &projectionApplierStub{}, ConsumerConfig{}); err == nil {
		t.Fatal("invalid max poll records was accepted")
	}
	var nilConsumer *Consumer
	if err := nilConsumer.Run(context.Background()); err == nil {
		t.Fatal("nil consumer was accepted")
	}
	if nilConsumer.Ready() || nilConsumer.PausedPartitions() != nil {
		t.Fatal("nil consumer status should be not-ready and empty")
	}
}

func fetchRecords(records ...*kgo.Record) kgo.Fetches {
	byPartition := make(map[TopicPartition][]*kgo.Record)
	for _, record := range records {
		key := TopicPartition{Topic: record.Topic, Partition: record.Partition}
		byPartition[key] = append(byPartition[key], record)
	}
	topics := make(map[string][]kgo.FetchPartition)
	for key, partitionRecords := range byPartition {
		topics[key.Topic] = append(topics[key.Topic], kgo.FetchPartition{Partition: key.Partition, Records: partitionRecords})
	}
	fetchTopics := make([]kgo.FetchTopic, 0, len(topics))
	for topic, partitions := range topics {
		fetchTopics = append(fetchTopics, kgo.FetchTopic{Topic: topic, Partitions: partitions})
	}
	return kgo.Fetches{{Topics: fetchTopics}}
}

type projectionConsumerClientStub struct {
	mu         sync.Mutex
	fetches    []kgo.Fetches
	cancel     context.CancelFunc
	allowCalls int
	paused     []TopicPartition
	committed  []int64
}

type blockingProjectionConsumerClient struct {
	entered chan struct{}
	once    sync.Once
}

func (stub *blockingProjectionConsumerClient) PollRecords(ctx context.Context, _ int) kgo.Fetches {
	stub.once.Do(func() { close(stub.entered) })
	<-ctx.Done()
	return nil
}

func (*blockingProjectionConsumerClient) AllowRebalance() {}

func (*blockingProjectionConsumerClient) PauseFetchPartitions(map[string][]int32) map[string][]int32 {
	return nil
}

func (*blockingProjectionConsumerClient) CommitProjected(*kgo.Record, int64) {}

func (stub *projectionConsumerClientStub) PollRecords(ctx context.Context, _ int) kgo.Fetches {
	stub.mu.Lock()
	if len(stub.fetches) > 0 {
		fetches := stub.fetches[0]
		stub.fetches = stub.fetches[1:]
		stub.mu.Unlock()
		return fetches
	}
	cancel := stub.cancel
	stub.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-ctx.Done()
	return nil
}

func (stub *projectionConsumerClientStub) AllowRebalance() {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.allowCalls++
}

func (stub *projectionConsumerClientStub) PauseFetchPartitions(values map[string][]int32) map[string][]int32 {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for topic, partitions := range values {
		for _, partition := range partitions {
			stub.paused = append(stub.paused, TopicPartition{Topic: topic, Partition: partition})
		}
	}
	return nil
}

func (stub *projectionConsumerClientStub) CommitProjected(_ *kgo.Record, nextOffset int64) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.committed = append(stub.committed, nextOffset)
}

type projectionApplierStub struct {
	mu            sync.Mutex
	applied       map[int32][]int64
	failPartition int32
	applyErr      error
	findings      []findingCall
}

type findingCall struct {
	kind     string
	severity string
	freeze   bool
}

func (stub *projectionApplierStub) Apply(_ context.Context, record DecodedRecord) (ApplyResult, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.applied == nil {
		stub.applied = make(map[int32][]int64)
	}
	stub.applied[record.Partition] = append(stub.applied[record.Partition], record.Offset)
	if stub.applyErr != nil && record.Partition == stub.failPartition {
		return ApplyResult{}, stub.applyErr
	}
	return ApplyResult{NextOffset: record.Offset + 1}, nil
}

func (stub *projectionApplierStub) RecordFinding(_ context.Context, _ DecodedRecord, kind, severity string, freeze bool, _ error) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.findings = append(stub.findings, findingCall{kind: kind, severity: severity, freeze: freeze})
	return nil
}

func equalOffsets(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
