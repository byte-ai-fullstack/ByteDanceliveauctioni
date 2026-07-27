package runtimegeneration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrFrozen           = errors.New("runtime generation is frozen")
	ErrGenerationChange = errors.New("runtime Redis generation changed")
	ErrVerificationWait = errors.New("runtime generation verification is pending")
)

const VerifiedGenerationKey = "auction:runtime:generation:verified"

type Backend interface {
	PrimaryIdentity(ctx context.Context) (string, error)
	VerifiedGeneration(ctx context.Context) (string, error)
	SetVerifiedGeneration(ctx context.Context, generation string) error
}

type VerifyFunc func(ctx context.Context) error

type Config struct {
	PollInterval   time.Duration
	VerifyInterval time.Duration
	Verify         VerifyFunc
}

type Status struct {
	Ready          bool      `json:"ready"`
	Generation     string    `json:"generation,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	LastCheckedAt  time.Time `json:"last_checked_at,omitempty"`
	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`
}

// Guard freezes every runtime writer until the current Redis primary generation
// has been reconciled and recorded in Redis. A nil Verify function creates a
// follower guard, suitable for Redis-only workers that trust the coordinator's
// shared verification marker.
type Guard struct {
	backend        Backend
	verify         VerifyFunc
	pollInterval   time.Duration
	verifyInterval time.Duration

	refreshMu sync.Mutex
	mu        sync.RWMutex
	status    Status
	suspected string
	wake      chan struct{}
}

func NewGuard(backend Backend, cfg Config) (*Guard, error) {
	if backend == nil {
		return nil, errors.New("runtime generation backend is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Verify != nil && cfg.VerifyInterval <= 0 {
		cfg.VerifyInterval = 30 * time.Second
	}
	return &Guard{
		backend:        backend,
		verify:         cfg.Verify,
		pollInterval:   cfg.PollInterval,
		verifyInterval: cfg.VerifyInterval,
		status:         Status{Reason: "generation has not been verified"},
		wake:           make(chan struct{}, 1),
	}, nil
}

// Initialize performs the first blocking safety check. Callers may still start
// health endpoints after an error; the guard remains frozen and Run keeps retrying.
func (guard *Guard) Initialize(ctx context.Context) error {
	return guard.Refresh(ctx)
}

func (guard *Guard) Run(ctx context.Context) {
	if guard == nil {
		return
	}
	ticker := time.NewTicker(guard.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			guard.freeze("generation monitor stopped")
			return
		case <-ticker.C:
			_ = guard.Refresh(ctx)
		case <-guard.wake:
			_ = guard.Refresh(ctx)
		}
	}
}

// SignalSwitchMaster is the fast path for Sentinel's +switch-master event. It
// freezes synchronously and will not unfreeze until a different run_id is seen.
func (guard *Guard) SignalSwitchMaster() {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	guard.suspected = guard.status.Generation
	guard.status.Ready = false
	guard.status.Reason = "Sentinel reported a primary switch"
	guard.mu.Unlock()
	select {
	case guard.wake <- struct{}{}:
	default:
	}
}

func (guard *Guard) Refresh(ctx context.Context) error {
	if guard == nil {
		return fmt.Errorf("%w: guard is not initialized", ErrFrozen)
	}
	guard.refreshMu.Lock()
	defer guard.refreshMu.Unlock()

	generation, err := guard.backend.PrimaryIdentity(ctx)
	if err != nil {
		guard.freeze("read Redis primary identity: " + boundedReason(err))
		return fmt.Errorf("%w: read primary identity: %v", ErrFrozen, err)
	}
	generation = strings.TrimSpace(generation)
	if generation == "" {
		guard.freeze("Redis primary identity is empty")
		return fmt.Errorf("%w: empty primary identity", ErrFrozen)
	}

	guard.mu.Lock()
	guard.status.LastCheckedAt = time.Now().UTC()
	previous := guard.status.Generation
	suspected := guard.suspected
	if suspected != "" && generation == suspected {
		guard.status.Ready = false
		guard.status.Reason = "waiting to observe the new Redis primary generation"
		guard.mu.Unlock()
		return fmt.Errorf("%w: still observing generation %s", ErrVerificationWait, generation)
	}
	changed := previous != "" && generation != previous
	if changed || previous == "" || suspected != "" {
		guard.status.Ready = false
		guard.status.Reason = "reconciling Redis primary generation"
	}
	guard.mu.Unlock()

	verified, verifyErr := guard.backend.VerifiedGeneration(ctx)
	if verifyErr != nil {
		guard.freeze("read verified generation: " + boundedReason(verifyErr))
		return fmt.Errorf("%w: read verified generation: %v", ErrFrozen, verifyErr)
	}
	guard.mu.RLock()
	lastVerifiedAt := guard.status.LastVerifiedAt
	guard.mu.RUnlock()
	verificationFresh := guard.verify == nil || (guard.verifyInterval > 0 && time.Since(lastVerifiedAt) < guard.verifyInterval)
	if previous == generation && suspected == "" && verified == generation && verificationFresh {
		guard.markReady(generation)
		return nil
	}

	if guard.verify == nil {
		if suspected == "" && verified == generation {
			guard.markReady(generation)
			return nil
		}
		guard.freeze("waiting for generation coordinator verification")
		return fmt.Errorf("%w: current=%s verified=%s", ErrVerificationWait, generation, verified)
	}
	if err := guard.verify(ctx); err != nil {
		guard.freeze("runtime reconciliation failed: " + boundedReason(err))
		return fmt.Errorf("%w: reconcile generation %s: %v", ErrVerificationWait, generation, err)
	}
	confirmed, err := guard.backend.PrimaryIdentity(ctx)
	if err != nil || confirmed != generation {
		guard.freeze("Redis primary changed during reconciliation")
		return fmt.Errorf("%w: before=%s after=%s error=%v", ErrGenerationChange, generation, confirmed, err)
	}
	if err := guard.backend.SetVerifiedGeneration(ctx, generation); err != nil {
		guard.freeze("publish verified generation: " + boundedReason(err))
		return fmt.Errorf("%w: publish generation: %v", ErrFrozen, err)
	}
	confirmed, err = guard.backend.PrimaryIdentity(ctx)
	if err != nil || confirmed != generation {
		guard.freeze("Redis primary changed while publishing verification")
		return fmt.Errorf("%w: before=%s after=%s error=%v", ErrGenerationChange, generation, confirmed, err)
	}
	verified, err = guard.backend.VerifiedGeneration(ctx)
	if err != nil || verified != generation {
		guard.freeze("verified generation marker did not persist")
		return fmt.Errorf("%w: current=%s verified=%s error=%v", ErrFrozen, generation, verified, err)
	}
	guard.markReady(generation)
	return nil
}

func (guard *Guard) AllowWrite() (string, error) {
	if guard == nil {
		return "", fmt.Errorf("%w: guard is not initialized", ErrFrozen)
	}
	guard.mu.RLock()
	status := guard.status
	guard.mu.RUnlock()
	if !status.Ready || status.Generation == "" {
		return "", fmt.Errorf("%w: %s", ErrFrozen, status.Reason)
	}
	return status.Generation, nil
}

func (guard *Guard) Ping(context.Context) error {
	_, err := guard.AllowWrite()
	return err
}

func (guard *Guard) Status() Status {
	if guard == nil {
		return Status{Reason: "guard is not initialized"}
	}
	guard.mu.RLock()
	defer guard.mu.RUnlock()
	return guard.status
}

func (guard *Guard) freeze(reason string) {
	guard.mu.Lock()
	guard.status.Ready = false
	guard.status.Reason = boundedText(reason, 512)
	guard.status.LastCheckedAt = time.Now().UTC()
	guard.mu.Unlock()
}

func (guard *Guard) markReady(generation string) {
	now := time.Now().UTC()
	guard.mu.Lock()
	guard.status.Ready = true
	guard.status.Generation = generation
	guard.status.Reason = ""
	guard.status.LastCheckedAt = now
	guard.status.LastVerifiedAt = now
	guard.suspected = ""
	guard.mu.Unlock()
}

func boundedReason(err error) string {
	if err == nil {
		return ""
	}
	return boundedText(err.Error(), 384)
}

func boundedText(value string, limit int) string {
	value = strings.Map(func(char rune) rune {
		if char == '\r' || char == '\n' || char == '\x00' {
			return ' '
		}
		return char
	}, value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
