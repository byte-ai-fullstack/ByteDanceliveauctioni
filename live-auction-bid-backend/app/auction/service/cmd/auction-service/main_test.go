package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type lifecycleServerStub struct {
	started  chan struct{}
	stopped  chan struct{}
	startErr error
	once     sync.Once
}

func newLifecycleServerStub() *lifecycleServerStub {
	return &lifecycleServerStub{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (s *lifecycleServerStub) Start(ctx context.Context) error {
	close(s.started)
	if s.startErr != nil {
		return s.startErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stopped:
		return nil
	}
}

func (s *lifecycleServerStub) Stop(context.Context) error {
	s.once.Do(func() { close(s.stopped) })
	return nil
}

func TestRunServersStopsBothOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	grpcServer := newLifecycleServerStub()
	operationsServer := newLifecycleServerStub()
	go func() {
		<-grpcServer.started
		<-operationsServer.started
		cancel()
	}()
	if err := runServers(ctx, grpcServer, operationsServer, time.Second); err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
	for name, stopped := range map[string]<-chan struct{}{
		"grpc": grpcServer.stopped, "operations": operationsServer.stopped,
	} {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("%s server was not stopped", name)
		}
	}
}

func TestRunServersPropagatesStartupFailure(t *testing.T) {
	want := errors.New("listen failed")
	grpcServer := newLifecycleServerStub()
	grpcServer.startErr = want
	operationsServer := newLifecycleServerStub()
	if err := runServers(context.Background(), grpcServer, operationsServer, time.Second); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
	select {
	case <-operationsServer.stopped:
	case <-time.After(time.Second):
		t.Fatal("operations server was not stopped after gRPC failure")
	}
}

func TestCommandPublisherRequiresNATSInProduction(t *testing.T) {
	getenv := mapEnvironment(map[string]string{"AUCTION_ENV": "production"})
	if _, _, err := newCommandPublisher(context.Background(), getenv, "node-a"); err == nil {
		t.Fatal("production auction-service accepted missing NATS URLs")
	}
	getenv = mapEnvironment(map[string]string{"AUCTION_ENV": "dev"})
	publisher, closePublisher, err := newCommandPublisher(context.Background(), getenv, "node-a")
	if err != nil || publisher == nil || closePublisher == nil {
		t.Fatalf("development publisher fallback: publisher=%v close_nil=%t error=%v", publisher, closePublisher == nil, err)
	}
	closePublisher()
}

func TestSettingsRejectInvalidValues(t *testing.T) {
	getenv := mapEnvironment(map[string]string{
		"BAD_DURATION": "soon",
		"BAD_INTEGER":  "-1",
		"BAD_BOOLEAN":  "perhaps",
	})
	if _, err := durationSetting(getenv, "BAD_DURATION", time.Second); err == nil {
		t.Fatal("invalid duration was accepted")
	}
	if _, err := intSetting(getenv, "BAD_INTEGER", 1, 0); err == nil {
		t.Fatal("invalid integer was accepted")
	}
	if _, err := parseBoolSetting(getenv, "BAD_BOOLEAN", false); err == nil {
		t.Fatal("invalid boolean was accepted")
	}
}

func TestDataConfigRequiresRedisPrimary(t *testing.T) {
	if _, err := dataConfigFromEnv(mapEnvironment(nil), nil); err == nil {
		t.Fatal("nil Redis primary was accepted")
	}
}

func TestProjectionGateSettingsAreFailClosedInProduction(t *testing.T) {
	t.Parallel()

	settings, err := projectionGateSettingsFromEnv(mapEnvironment(map[string]string{
		"AUCTION_ENV": "production",
	}))
	if err != nil {
		t.Fatalf("production projection settings error = %v", err)
	}
	if !settings.Enabled || settings.Config.MaxLagRecords != 1000 || settings.Config.HealthyPollsToOpen != 3 {
		t.Fatalf("production projection settings = %+v", settings)
	}
	if _, err := projectionGateSettingsFromEnv(mapEnvironment(map[string]string{
		"AUCTION_ENV":                     "production",
		"AUCTION_PROJECTION_GATE_ENABLED": "false",
	})); err == nil {
		t.Fatal("production accepted a disabled projection gate")
	}
}

func TestProjectionGateSettingsAllowControlledDevelopmentOptIn(t *testing.T) {
	t.Parallel()

	disabled, err := projectionGateSettingsFromEnv(mapEnvironment(map[string]string{"AUCTION_ENV": "dev"}))
	if err != nil || disabled.Enabled {
		t.Fatalf("development default = %+v, error = %v", disabled, err)
	}
	settings, err := projectionGateSettingsFromEnv(mapEnvironment(map[string]string{
		"AUCTION_ENV":                                     "dev",
		"AUCTION_PROJECTION_GATE_ENABLED":                 "true",
		"AUCTION_PROJECTION_GATE_REFRESH_INTERVAL":        "4s",
		"AUCTION_PROJECTION_GATE_REFRESH_TIMEOUT":         "2s",
		"AUCTION_PROJECTION_GATE_MAX_STALENESS":           "8s",
		"AUCTION_PROJECTION_GATE_MAX_LAG_RECORDS":         "250",
		"AUCTION_PROJECTION_GATE_MAX_OLDEST_AGE":          "45s",
		"AUCTION_PROJECTION_GATE_RUNTIME_TOPIC_RETENTION": "720h",
		"AUCTION_PROJECTION_GATE_MIN_RETENTION_HEADROOM":  "24h",
		"AUCTION_PROJECTION_GATE_HEALTHY_POLLS_TO_OPEN":   "4",
	}))
	if err != nil {
		t.Fatalf("development projection settings error = %v", err)
	}
	if !settings.Enabled || settings.Config.RefreshInterval != 4*time.Second ||
		settings.Config.RefreshTimeout != 2*time.Second || settings.Config.MaxStaleness != 8*time.Second ||
		settings.Config.MaxLagRecords != 250 || settings.Config.MaxOldestAge != 45*time.Second ||
		settings.Config.RuntimeTopicRetention != 720*time.Hour || settings.Config.MinRetentionHeadroom != 24*time.Hour ||
		settings.Config.HealthyPollsToOpen != 4 {
		t.Fatalf("custom projection settings = %+v", settings)
	}
}

func TestProjectionGateSettingsRejectUnsafeThresholds(t *testing.T) {
	t.Parallel()

	for key, value := range map[string]string{
		"AUCTION_PROJECTION_GATE_MAX_LAG_RECORDS":        "0",
		"AUCTION_PROJECTION_GATE_REFRESH_TIMEOUT":        "2s",
		"AUCTION_PROJECTION_GATE_HEALTHY_POLLS_TO_OPEN":  "101",
		"AUCTION_PROJECTION_GATE_MIN_RETENTION_HEADROOM": "2160h",
	} {
		t.Run(key, func(t *testing.T) {
			values := map[string]string{
				"AUCTION_ENV":                     "dev",
				"AUCTION_PROJECTION_GATE_ENABLED": "true",
				key:                               value,
			}
			if _, err := projectionGateSettingsFromEnv(mapEnvironment(values)); err == nil {
				t.Fatalf("unsafe %s=%s was accepted", key, value)
			}
		})
	}
	if _, err := projectionGateSettingsFromEnv(nil); err == nil {
		t.Fatal("nil environment reader was accepted")
	}
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
