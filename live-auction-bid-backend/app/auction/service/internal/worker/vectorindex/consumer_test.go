package vectorindex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

func TestConsumerCommitsOnlyAfterVectorApply(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{result: searchindex.VectorApplyResult{Applied: true}}
	consumer := newTestConsumer(t, client, processor)
	if err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if processor.calls != 1 || client.commits != 1 || client.deadLetters != 0 {
		t.Fatalf("calls=%d commits=%d dlq=%d", processor.calls, client.commits, client.deadLetters)
	}
}

func TestConsumerRetriesThenDeadLettersEmbeddingFailure(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{err: ErrEmbeddingFailure}
	consumer := newTestConsumer(t, client, processor)
	consumer.wait = func(context.Context, time.Duration) error { return nil }
	if err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if processor.calls != 3 || client.deadLetters != 1 || client.commits != 1 || client.errorClass != "embedding_failed" {
		t.Fatalf("calls=%d dlq=%d commits=%d class=%q", processor.calls, client.deadLetters, client.commits, client.errorClass)
	}
}

func TestConsumerDoesNotCommitInfrastructureFailure(t *testing.T) {
	client := &fakeConsumerClient{}
	processor := &fakeRecordProcessor{err: errors.New("postgres unavailable")}
	consumer := newTestConsumer(t, client, processor)
	consumer.wait = func(context.Context, time.Duration) error { return nil }
	err := consumer.processRecord(context.Background(), validLotStateKafkaRecord(t))
	if err == nil || client.deadLetters != 0 || client.commits != 0 || processor.calls != 3 {
		t.Fatalf("error=%v dlq=%d commits=%d calls=%d", err, client.deadLetters, client.commits, processor.calls)
	}
}

func TestConsumerRequiresDLQACKBeforeCommit(t *testing.T) {
	client := &fakeConsumerClient{deadLetterErr: errors.New("Kafka unavailable")}
	consumer := newTestConsumer(t, client, &fakeRecordProcessor{})
	err := consumer.processRecord(context.Background(), &kgo.Record{Topic: "wrong", Partition: 0, Offset: 1})
	if err == nil || client.commits != 0 {
		t.Fatalf("error=%v commits=%d", err, client.commits)
	}
}

func newTestConsumer(t *testing.T, client *fakeConsumerClient, processor *fakeRecordProcessor) *Consumer {
	t.Helper()
	consumer, err := NewConsumer(client, processor, ConsumerConfig{
		MaxPollRecords: 100, RetryAttempts: 3, RetryBase: time.Millisecond, RetryMax: 5 * time.Millisecond, OperationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	consumer.jitter = func(value time.Duration) time.Duration { return value }
	return consumer
}

type fakeRecordProcessor struct {
	result searchindex.VectorApplyResult
	err    error
	calls  int
}

func (processor *fakeRecordProcessor) Apply(context.Context, Record) (searchindex.VectorApplyResult, error) {
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
