package enrichment

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

func TestConsumerProcessesAndCommitsEnrichment(t *testing.T) {
	client := &fakeConsumerClient{}
	store := &fakeRecordApplier{results: []ApplyResult{{Status: orderenrichment.StatusReady}}}
	consumer := newTestConsumer(t, client, store)
	if err := consumer.processRecord(context.Background(), validKafkaRecord(t)); err != nil {
		t.Fatalf("processRecord() error = %v", err)
	}
	if store.calls != 1 || client.commits != 1 || client.deadLetters != 0 {
		t.Fatalf("calls=%d commits=%d dead_letters=%d", store.calls, client.commits, client.deadLetters)
	}
}

func TestConsumerDeadLettersInvalidAndTerminalRecordsBeforeCommit(t *testing.T) {
	tests := map[string]struct {
		source *kgo.Record
		err    error
		class  string
	}{
		"invalid Kafka record": {source: &kgo.Record{Topic: "wrong", Partition: 0, Offset: 1}, class: "invalid_record"},
		"identity conflict":    {source: validKafkaRecord(t), err: ErrMessageIdentityConflict, class: "message_identity_conflict"},
		"source corruption":    {source: validKafkaRecord(t), err: ErrEnrichmentSourceCorrupt, class: "source_corrupt"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := &fakeConsumerClient{}
			store := &fakeRecordApplier{errors: []error{test.err}}
			consumer := newTestConsumer(t, client, store)
			if err := consumer.processRecord(context.Background(), test.source); err != nil {
				t.Fatalf("processRecord() error = %v", err)
			}
			if client.deadLetters != 1 || client.commits != 1 || client.lastErrorClass != test.class {
				t.Fatalf("dead_letters=%d commits=%d class=%q", client.deadLetters, client.commits, client.lastErrorClass)
			}
		})
	}
}

func TestConsumerRetriesMissingOrderThenDeadLetters(t *testing.T) {
	client := &fakeConsumerClient{}
	store := &fakeRecordApplier{errors: []error{ErrOrderNotFound, ErrOrderNotFound, ErrOrderNotFound}}
	consumer := newTestConsumer(t, client, store)
	waits := 0
	consumer.wait = func(context.Context, time.Duration) error { waits++; return nil }
	if err := consumer.processRecord(context.Background(), validKafkaRecord(t)); err != nil {
		t.Fatalf("processRecord() error = %v", err)
	}
	if store.calls != 3 || waits != 2 || client.deadLetters != 1 || client.commits != 1 || client.lastErrorClass != "order_not_found" {
		t.Fatalf("calls=%d waits=%d dlq=%d commits=%d class=%q", store.calls, waits, client.deadLetters, client.commits, client.lastErrorClass)
	}
}

func TestConsumerDoesNotDeadLetterExhaustedTransientDatabaseFailure(t *testing.T) {
	client := &fakeConsumerClient{}
	deadlock := &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	store := &fakeRecordApplier{errors: []error{deadlock, deadlock, deadlock}}
	consumer := newTestConsumer(t, client, store)
	consumer.wait = func(context.Context, time.Duration) error { return nil }
	err := consumer.processRecord(context.Background(), validKafkaRecord(t))
	if err == nil || client.deadLetters != 0 || client.commits != 0 || store.calls != 3 {
		t.Fatalf("error=%v dead_letters=%d commits=%d calls=%d", err, client.deadLetters, client.commits, store.calls)
	}
}

func TestConsumerRequiresDLQAndOffsetAcknowledgements(t *testing.T) {
	client := &fakeConsumerClient{deadLetterErr: errors.New("Kafka unavailable")}
	consumer := newTestConsumer(t, client, &fakeRecordApplier{})
	err := consumer.processRecord(context.Background(), &kgo.Record{Topic: "wrong", Partition: 0, Offset: 1})
	if err == nil || client.commits != 0 {
		t.Fatalf("DLQ error=%v commits=%d", err, client.commits)
	}

	client = &fakeConsumerClient{commitErr: errors.New("commit failed")}
	consumer = newTestConsumer(t, client, &fakeRecordApplier{results: []ApplyResult{{Status: orderenrichment.StatusReady}}})
	if err := consumer.processRecord(context.Background(), validKafkaRecord(t)); err == nil {
		t.Fatal("commit failure was ignored")
	}
}

func TestNewConsumerValidatesConfiguration(t *testing.T) {
	client := &fakeConsumerClient{}
	store := &fakeRecordApplier{}
	if _, err := NewConsumer(nil, store, ConsumerConfig{}); err == nil {
		t.Fatal("nil client was accepted")
	}
	if _, err := NewConsumer(client, store, ConsumerConfig{MaxPollRecords: 1}); err == nil {
		t.Fatal("incomplete config was accepted")
	}
}

func TestConsumerIsReadyWhileEmptyTopicPollBlocks(t *testing.T) {
	client := &fakeConsumerClient{pollStarted: make(chan struct{})}
	consumer := newTestConsumer(t, client, &fakeRecordApplier{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	select {
	case <-client.pollStarted:
	case <-time.After(time.Second):
		t.Fatal("consumer did not enter Kafka poll")
	}
	if !consumer.Ready() {
		t.Fatal("consumer should be ready while an empty topic poll is blocking")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop after cancellation")
	}
	if consumer.Ready() {
		t.Fatal("stopped consumer remained ready")
	}
}

func newTestConsumer(t *testing.T, client *fakeConsumerClient, store *fakeRecordApplier) *Consumer {
	t.Helper()
	consumer, err := NewConsumer(client, store, ConsumerConfig{
		MaxPollRecords: 100, RetryAttempts: 3, RetryBase: time.Millisecond, RetryMax: 5 * time.Millisecond, OperationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	consumer.jitter = func(limit time.Duration) time.Duration { return limit }
	return consumer
}

type fakeRecordApplier struct {
	results []ApplyResult
	errors  []error
	calls   int
}

func (store *fakeRecordApplier) Apply(_ context.Context, _ Record, _ int) (ApplyResult, error) {
	index := store.calls
	store.calls++
	var result ApplyResult
	var err error
	if index < len(store.results) {
		result = store.results[index]
	}
	if index < len(store.errors) {
		err = store.errors[index]
	}
	return result, err
}

type fakeConsumerClient struct {
	commits        int
	deadLetters    int
	lastErrorClass string
	commitErr      error
	deadLetterErr  error
	pollStarted    chan struct{}
	pollOnce       sync.Once
}

func (client *fakeConsumerClient) PollRecords(ctx context.Context, _ int) kgo.Fetches {
	if client.pollStarted == nil {
		return nil
	}
	client.pollOnce.Do(func() { close(client.pollStarted) })
	<-ctx.Done()
	return nil
}
func (*fakeConsumerClient) AllowRebalance() {}

func (client *fakeConsumerClient) CommitRecord(context.Context, *kgo.Record) error {
	client.commits++
	return client.commitErr
}

func (client *fakeConsumerClient) ProduceDeadLetter(_ context.Context, _ *kgo.Record, errorClass string, _ error, _ time.Time) error {
	client.deadLetters++
	client.lastErrorClass = errorClass
	return client.deadLetterErr
}
