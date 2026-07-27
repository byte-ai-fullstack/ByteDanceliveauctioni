package projector

import (
	"context"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
)

func TestNewKafkaConsumerRejectsUnsafeConfigurationBeforeConnecting(t *testing.T) {
	validKafka := kafkaclient.Config{
		Brokers:          []string{"127.0.0.1:19092"},
		ClientID:         "projector-test",
		SecurityProtocol: kafkaclient.SecurityProtocolPlaintext,
	}
	validConsumer := KafkaConsumerConfig{
		GroupID:           "auction-projector-v1",
		SessionTimeout:    30 * time.Second,
		HeartbeatInterval: 3 * time.Second,
		MaxPollRecords:    100,
	}
	if _, err := NewKafkaConsumer(context.Background(), validKafka, nil, validConsumer); err == nil {
		t.Fatal("nil offset initializer was accepted")
	}
	invalidGroup := validConsumer
	invalidGroup.GroupID = ""
	if _, err := NewKafkaConsumer(context.Background(), validKafka, &offsetInitializerStub{}, invalidGroup); err == nil {
		t.Fatal("empty group ID was accepted")
	}
	invalidHeartbeat := validConsumer
	invalidHeartbeat.HeartbeatInterval = invalidHeartbeat.SessionTimeout
	if _, err := NewKafkaConsumer(context.Background(), validKafka, &offsetInitializerStub{}, invalidHeartbeat); err == nil {
		t.Fatal("invalid heartbeat was accepted")
	}
	invalidKafka := validKafka
	invalidKafka.Brokers = nil
	if _, err := NewKafkaConsumer(context.Background(), invalidKafka, &offsetInitializerStub{}, validConsumer); err == nil {
		t.Fatal("invalid shared Kafka config was accepted")
	}
}

func TestKafkaConsumerNilSafeNoOpMethods(t *testing.T) {
	var consumer *KafkaConsumer
	consumer.Close()
	(&KafkaConsumer{}).CommitProjected(nil, -1)
}
