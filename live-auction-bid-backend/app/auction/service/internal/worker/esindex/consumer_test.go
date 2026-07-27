package esindex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

func TestConsumerCommitsOnlyAfterElasticsearchApply(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{result: searchindex.ElasticsearchApplyResult{Applied: true}}
	consumer := newTestConsumer(t, client, processor, &fakeFindingStore{})
	if err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if processor.calls != 1 || client.commits != 1 || client.deadLetters != 0 {
		t.Fatalf("calls=%d commits=%d dlq=%d", processor.calls, client.commits, client.deadLetters)
	}
}

func TestConsumerDoesNotCommitInfrastructureFailure(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{err: errors.New("Elasticsearch unavailable")}
	consumer := newTestConsumer(t, client, processor, &fakeFindingStore{})
	consumer.wait = func(context.Context, time.Duration) error { return nil }
	err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t))
	if err == nil || client.deadLetters != 0 || client.commits != 0 || processor.calls != 3 {
		t.Fatalf("error=%v dlq=%d commits=%d calls=%d", err, client.deadLetters, client.commits, processor.calls)
	}
}

func TestConsumerDeadLettersIdentityConflictBeforeCommit(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{err: searchindex.ErrElasticsearchVersionConflict}
	findings := &fakeFindingStore{}
	consumer := newTestConsumer(t, client, processor, findings)
	if err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t)); err != nil {
		t.Fatal(err)
	}
	if processor.calls != 1 || findings.calls != 1 || client.deadLetters != 1 || client.commits != 1 || client.errorClass != "document_identity_conflict" {
		t.Fatalf("calls=%d findings=%d dlq=%d commits=%d class=%q", processor.calls, findings.calls, client.deadLetters, client.commits, client.errorClass)
	}
}

func TestConsumerDoesNotAcknowledgeConflictWhenFindingFails(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{err: searchindex.ErrElasticsearchVersionConflict}
	findings := &fakeFindingStore{err: errors.New("MySQL unavailable")}
	consumer := newTestConsumer(t, client, processor, findings)
	if err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t)); err == nil {
		t.Fatal("finding failure was accepted")
	}
	if findings.calls != 1 || client.deadLetters != 0 || client.commits != 0 {
		t.Fatalf("findings=%d dlq=%d commits=%d", findings.calls, client.deadLetters, client.commits)
	}
}

func TestConsumerRequiresDLQACKBeforeCommit(t *testing.T) {
	client := &fakeConsumerClient{deadLetterErr: errors.New("Kafka unavailable")}
	consumer := newTestConsumer(t, client, &fakeRecordProcessor{}, &fakeFindingStore{})
	err := consumer.processRecord(context.Background(), &kgo.Record{Topic: "wrong", Partition: 0, Offset: 1})
	if err == nil || client.commits != 0 {
		t.Fatalf("error=%v commits=%d", err, client.commits)
	}
}

func TestConsumerValidationAndBackoffBounds(t *testing.T) {
	if _, err := NewConsumer(nil, nil, nil, ConsumerConfig{}); err == nil {
		t.Fatal("nil consumer dependencies were accepted")
	}
	consumer := newTestConsumer(t, &fakeConsumerClient{}, &fakeRecordProcessor{}, &fakeFindingStore{})
	if consumer.Ready() {
		t.Fatal("consumer was ready before Run")
	}
	consumer.config.RetryBase = 3 * time.Millisecond
	consumer.config.RetryMax = 5 * time.Millisecond
	if delay := consumer.retryDelay(4); delay != 5*time.Millisecond {
		t.Fatalf("delay=%s", delay)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(canceled, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func newTestConsumer(t *testing.T, client *fakeConsumerClient, processor *fakeRecordProcessor, findings *fakeFindingStore) *Consumer {
	t.Helper()
	consumer, err := NewConsumer(client, processor, findings, ConsumerConfig{
		MaxPollRecords: 100, RetryAttempts: 3, RetryBase: time.Millisecond, RetryMax: 5 * time.Millisecond, OperationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	consumer.jitter = func(value time.Duration) time.Duration { return value }
	return consumer
}

type fakeFindingStore struct {
	calls int
	err   error
}

func (store *fakeFindingStore) RecordIdentityConflict(context.Context, searchstate.Record, error) error {
	store.calls++
	return store.err
}

type fakeRecordProcessor struct {
	result searchindex.ElasticsearchApplyResult
	err    error
	calls  int
}

func (processor *fakeRecordProcessor) Apply(context.Context, searchstate.Record) (searchindex.ElasticsearchApplyResult, error) {
	processor.calls++
	return processor.result, processor.err
}

type fakeConsumerClient struct {
	commits       int
	deadLetters   int
	errorClass    string
	commitErr     error
	deadLetterErr error
}

func (*fakeConsumerClient) PollRecords(context.Context, int) kgo.Fetches { return nil }
func (*fakeConsumerClient) AllowRebalance()                              {}
func (client *fakeConsumerClient) CommitRecord(context.Context, *kgo.Record) error {
	client.commits++
	return client.commitErr
}
func (client *fakeConsumerClient) ProduceDeadLetter(_ context.Context, _ *kgo.Record, errorClass string, _ error, _ time.Time) error {
	client.deadLetters++
	client.errorClass = errorClass
	return client.deadLetterErr
}
