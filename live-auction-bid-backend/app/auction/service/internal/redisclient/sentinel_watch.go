package redisclient

import (
	"context"
	"crypto/tls"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const sentinelSwitchMasterChannel = "+switch-master"

// RunSentinelSwitchMasterWatcher listens to every configured Sentinel. Polling
// run_id remains the correctness path; this listener only shortens freeze latency.
func RunSentinelSwitchMasterWatcher(ctx context.Context, cfg Config, notify func(event string)) {
	if ctx == nil || notify == nil || strings.TrimSpace(cfg.MasterName) == "" || len(cfg.Addrs) == 0 {
		return
	}
	tlsConfig, err := newTLSConfigForWatcher(cfg)
	if err != nil {
		return
	}
	var dedupeMu sync.Mutex
	lastEvent := ""
	notifyOnce := func(event string) {
		dedupeMu.Lock()
		if event == lastEvent {
			dedupeMu.Unlock()
			return
		}
		lastEvent = event
		dedupeMu.Unlock()
		notify(event)
	}
	for _, address := range deduplicate(cfg.Addrs) {
		address := address
		go watchSentinel(ctx, address, cfg, tlsConfig, notifyOnce)
	}
	<-ctx.Done()
}

func watchSentinel(ctx context.Context, address string, cfg Config, tlsConfig *tls.Config, notify func(string)) {
	for ctx.Err() == nil {
		client := redis.NewSentinelClient(&redis.Options{
			Addr:                  address,
			Username:              cfg.SentinelUsername,
			Password:              cfg.SentinelPassword,
			DialTimeout:           cfg.DialTimeout,
			ReadTimeout:           cfg.ReadTimeout,
			WriteTimeout:          cfg.WriteTimeout,
			ContextTimeoutEnabled: true,
			TLSConfig:             tlsConfig,
		})
		pubsub := client.Subscribe(ctx, sentinelSwitchMasterChannel)
		if _, err := pubsub.Receive(ctx); err == nil {
			channel := pubsub.Channel()
			watching := true
			for watching {
				select {
				case <-ctx.Done():
					watching = false
				case message, open := <-channel:
					if !open {
						watching = false
						continue
					}
					if event, ok := parseSwitchMasterEvent(message.Payload, cfg.MasterName); ok {
						notify(event)
					}
				}
			}
		}
		_ = pubsub.Close()
		_ = client.Close()
		if !waitSentinelRetry(ctx, time.Second) {
			return
		}
	}
}

func parseSwitchMasterEvent(payload, masterName string) (string, bool) {
	fields := strings.Fields(payload)
	if len(fields) < 5 || fields[0] != strings.TrimSpace(masterName) || fields[1] == "" || fields[3] == "" {
		return "", false
	}
	oldPort, oldErr := strconv.Atoi(fields[2])
	newPort, newErr := strconv.Atoi(fields[4])
	if oldErr != nil || newErr != nil || oldPort <= 0 || oldPort > 65535 || newPort <= 0 || newPort > 65535 {
		return "", false
	}
	return strings.Join(fields[:5], " "), true
}

func waitSentinelRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func newTLSConfigForWatcher(cfg Config) (*tls.Config, error) {
	if !cfg.TLS {
		return nil, nil
	}
	return newTLSConfig(cfg.TLSCAFile, cfg.TLSServerName)
}
