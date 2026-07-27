package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/realtime"
)

type lifecycleServerStub struct {
	started  chan struct{}
	stopped  chan struct{}
	once     sync.Once
	startErr error
}

func newLifecycleServerStub() *lifecycleServerStub {
	return &lifecycleServerStub{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (server *lifecycleServerStub) Start(ctx context.Context) error {
	close(server.started)
	if server.startErr != nil {
		return server.startErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-server.stopped:
		return nil
	}
}

func (server *lifecycleServerStub) Stop(context.Context) error {
	server.once.Do(func() { close(server.stopped) })
	return nil
}

func TestRunGatewayHTTPServerDrainsBeforeStopping(t *testing.T) {
	httpServer := newLifecycleServerStub()
	hub := realtime.NewHub(nil)
	shutdown := make(chan struct{})
	go func() {
		<-httpServer.started
		close(shutdown)
	}()

	err := runGatewayHTTPServer(
		context.Background(),
		shutdown,
		httpServer,
		hub,
		realtime.DrainConfig{BatchSize: 1},
		time.Second,
		time.Second,
	)
	if err != nil {
		t.Fatalf("graceful server shutdown: %v", err)
	}
	if !hub.IsDraining() {
		t.Fatal("server shutdown did not close realtime admission")
	}
	select {
	case <-httpServer.stopped:
	default:
		t.Fatal("HTTP server was not stopped")
	}
}

func TestRunGatewayHTTPServerPropagatesStartupFailure(t *testing.T) {
	want := errors.New("listen failed")
	httpServer := newLifecycleServerStub()
	httpServer.startErr = want
	err := runGatewayHTTPServer(
		context.Background(),
		make(chan struct{}),
		httpServer,
		realtime.NewHub(nil),
		realtime.DrainConfig{BatchSize: 1},
		time.Second,
		time.Second,
	)
	if !errors.Is(err, want) {
		t.Fatalf("expected startup failure %v, got %v", want, err)
	}
}

func TestRealtimePublisherRequiresNATSOnlyInProduction(t *testing.T) {
	t.Setenv("AUCTION_NATS_URLS", "")
	t.Setenv("AUCTION_ENV", "dev")
	publisher, closePublisher, err := newRealtimePublisherFromEnv(context.Background(), realtime.NewHub(nil), "node-a")
	if err != nil || publisher == nil || closePublisher == nil {
		t.Fatalf("development local publisher: publisher=%v close_nil=%t error=%v", publisher, closePublisher == nil, err)
	}
	closePublisher()

	t.Setenv("AUCTION_ENV", "production")
	if _, _, err := newRealtimePublisherFromEnv(context.Background(), realtime.NewHub(nil), "node-a"); err == nil {
		t.Fatal("production started without AUCTION_NATS_URLS")
	}
}

type auctionHTTPServiceStub struct{ v1.AuctionServiceHTTPServer }

func TestGatewayRequiresAuctionCommandTarget(t *testing.T) {
	t.Setenv("AUCTION_COMMAND_GRPC_TARGET", "")
	if _, _, _, err := newAuctionHTTPServiceFromEnv(auctionHTTPServiceStub{}); err == nil {
		t.Fatal("gateway accepted local auction command execution")
	}
}

func TestSearchMonitoringRequirementsFollowConfiguredRetrievers(t *testing.T) {
	values := map[string]string{
		"AUCTION_SEARCH_ES_URL":   "http://elasticsearch:9200",
		"AUCTION_SEARCH_PG_DSN":   "postgres://pgvector/search",
		"AUCTION_SEARCH_PROVIDER": "pgvector",
	}
	requirements := searchMonitoringRequirementsFromEnv(func(key string) string { return values[key] })
	if !requirements.elasticsearch || !requirements.pgvector {
		t.Fatalf("requirements=%+v", requirements)
	}
	values["AUCTION_SEARCH_PROVIDER"] = "unsupported"
	requirements = searchMonitoringRequirementsFromEnv(func(key string) string { return values[key] })
	if !requirements.elasticsearch || requirements.pgvector {
		t.Fatalf("unsupported provider requirements=%+v", requirements)
	}
	if requirements := searchMonitoringRequirementsFromEnv(nil); requirements.elasticsearch || requirements.pgvector {
		t.Fatalf("nil environment requirements=%+v", requirements)
	}
}
