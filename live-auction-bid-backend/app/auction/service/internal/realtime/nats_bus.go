package realtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

const (
	defaultNATSURL             = nats.DefaultURL
	defaultNATSReconnectWait   = 500 * time.Millisecond
	defaultNATSReconnectJitter = 500 * time.Millisecond
	defaultNATSFlushTimeout    = 250 * time.Millisecond
	defaultNATSDispatchTimeout = 2 * time.Second
)

type NATSBusConfig struct {
	URL             string
	Name            string
	Origin          string
	ReconnectWait   time.Duration
	ReconnectJitter time.Duration
	FlushTimeout    time.Duration
	DispatchTimeout time.Duration
}

type NATSBus struct {
	url    string
	name   string
	origin string

	reconnectWait   time.Duration
	reconnectJitter time.Duration
	flushTimeout    time.Duration
	dispatchTimeout time.Duration

	mu     sync.Mutex
	conn   *nats.Conn
	sink   EventPublisher
	done   <-chan struct{}
	subs   map[string]*nats.Subscription
	closed bool
}

func NewNATSBus(cfg NATSBusConfig) (*NATSBus, error) {
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Origin = strings.TrimSpace(cfg.Origin)
	if cfg.URL == "" {
		cfg.URL = defaultNATSURL
	}
	if cfg.Origin == "" {
		cfg.Origin = cfg.Name
	}
	if cfg.Origin == "" {
		return nil, errors.New("nats realtime bus origin is required")
	}
	if cfg.Name == "" {
		cfg.Name = "live-auction-" + cfg.Origin
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = defaultNATSReconnectWait
	}
	if cfg.ReconnectJitter < 0 {
		return nil, errors.New("nats reconnect jitter cannot be negative")
	}
	if cfg.ReconnectJitter == 0 {
		cfg.ReconnectJitter = defaultNATSReconnectJitter
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = defaultNATSFlushTimeout
	}
	if cfg.DispatchTimeout <= 0 {
		cfg.DispatchTimeout = defaultNATSDispatchTimeout
	}
	return &NATSBus{
		url:             cfg.URL,
		name:            cfg.Name,
		origin:          cfg.Origin,
		reconnectWait:   cfg.ReconnectWait,
		reconnectJitter: cfg.ReconnectJitter,
		flushTimeout:    cfg.FlushTimeout,
		dispatchTimeout: cfg.DispatchTimeout,
		subs:            make(map[string]*nats.Subscription),
	}, nil
}

func (b *NATSBus) Start(ctx context.Context, sink EventPublisher) error {
	if sink == nil {
		return errors.New("nats realtime bus sink is required")
	}
	return b.start(ctx, sink)
}

// StartPublisher opens a publish-only connection for auction-service. It does
// not subscribe to room subjects and therefore cannot own WebSocket fanout.
func (b *NATSBus) StartPublisher(ctx context.Context) error {
	return b.start(ctx, nil)
}

func (b *NATSBus) start(ctx context.Context, sink EventPublisher) error {
	if b == nil {
		return errors.New("nats realtime bus is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := nats.Connect(
		b.url,
		nats.Name(b.name),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		// Core NATS is only the realtime acceleration path. Buffering while
		// disconnected could replay stale prices after Redis has advanced.
		nats.ReconnectBufSize(-1),
		nats.ReconnectWait(b.reconnectWait),
		nats.ReconnectJitter(b.reconnectJitter, b.reconnectJitter),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			observability.RecordNATSConnectionEvent("disconnected")
			slog.Warn("NATS realtime bus disconnected; Redis snapshot refresh remains active", "error", err)
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			observability.RecordNATSConnectionEvent("reconnected")
			slog.Info("NATS realtime bus reconnected", "url", conn.ConnectedUrl())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, subscription *nats.Subscription, err error) {
			observability.RecordNATSConnectionEvent("async_error")
			subject := ""
			if subscription != nil {
				subject = subscription.Subject
			}
			slog.Warn("NATS realtime bus asynchronous error", "subject", subject, "error", err)
		}),
	)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.conn != nil {
		b.mu.Unlock()
		conn.Close()
		return errors.New("nats realtime bus is already started")
	}
	b.conn = conn
	b.sink = sink
	b.done = ctx.Done()
	b.closed = false
	b.mu.Unlock()
	return nil
}

func (b *NATSBus) Publish(_ context.Context, event *v1.AuctionEvent) error {
	if b == nil {
		return errors.New("nats realtime bus is not initialized")
	}
	payload, err := encodeNATSEventEnvelope(b.origin, event)
	if err != nil {
		return err
	}
	subject, err := RoomSubject(event.GetRoomId())
	if err != nil {
		return err
	}
	b.mu.Lock()
	conn := b.conn
	closed := b.closed
	b.mu.Unlock()
	if conn == nil || closed || !conn.IsConnected() {
		observability.RecordNATSPublish("dropped_disconnected")
		return nil
	}
	if err := conn.Publish(subject, []byte(payload)); err != nil {
		if isTransientNATSError(err) {
			observability.RecordNATSPublish("dropped_disconnected")
			return nil
		}
		observability.RecordNATSPublish("error")
		return err
	}
	observability.RecordNATSPublish("published")
	return nil
}

// RetainRoom subscribes this gateway to the exact room subject. It intentionally
// does not use a queue group: every gateway with local viewers must receive the
// room event.
func (b *NATSBus) RetainRoom(_ context.Context, roomID string) error {
	if b == nil {
		return errors.New("nats realtime bus is not initialized")
	}
	subject, err := RoomSubject(roomID)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil || b.sink == nil || b.closed {
		return errors.New("nats realtime bus is not started")
	}
	if _, exists := b.subs[roomID]; exists {
		return nil
	}
	done := b.done
	sink := b.sink
	dispatchTimeout := b.dispatchTimeout
	sub, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		select {
		case <-done:
			return
		default:
		}
		dispatchCtx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
		defer cancel()
		_, _ = b.dispatchPayload(dispatchCtx, sink, string(msg.Data))
	})
	if err != nil {
		return err
	}
	if b.conn.IsConnected() {
		err = b.conn.FlushTimeout(b.flushTimeout)
	}
	if err != nil && !isTransientNATSError(err) {
		_ = sub.Unsubscribe()
		return err
	}
	b.subs[roomID] = sub
	observability.SetNATSSubscriptions(len(b.subs))
	return nil
}

// ReleaseRoom removes the exact room subscription after the last local viewer
// leaves. Repeated releases are safe.
func (b *NATSBus) ReleaseRoom(roomID string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := b.subs[roomID]
	if sub == nil {
		return nil
	}
	delete(b.subs, roomID)
	observability.SetNATSSubscriptions(len(b.subs))
	return sub.Unsubscribe()
}

func (b *NATSBus) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	conn := b.conn
	subs := b.subs
	b.subs = make(map[string]*nats.Subscription)
	b.conn = nil
	b.sink = nil
	b.done = nil
	b.closed = true
	b.mu.Unlock()
	observability.SetNATSSubscriptions(0)
	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	if conn != nil {
		conn.Close()
	}
	return nil
}

func isTransientNATSError(err error) bool {
	return errors.Is(err, nats.ErrConnectionReconnecting) ||
		errors.Is(err, nats.ErrDisconnected) ||
		errors.Is(err, nats.ErrReconnectBufExceeded) ||
		errors.Is(err, nats.ErrTimeout) ||
		errors.Is(err, nats.ErrNoServers)
}

func (b *NATSBus) dispatchPayload(ctx context.Context, sink EventPublisher, payload string) (bool, error) {
	return dispatchNATSEvent(ctx, b.origin, sink, payload)
}
