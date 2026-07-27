package runtimegeneration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	generationA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	generationB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestCoordinatorGuardFreezesThenVerifiesEachObservedGeneration(t *testing.T) {
	backend := &fakeBackend{identity: generationA}
	verifyCalls := 0
	guard, err := NewGuard(backend, Config{Verify: func(context.Context) error {
		verifyCalls++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.AllowWrite(); !errors.Is(err, ErrFrozen) {
		t.Fatalf("uninitialized allow error=%v", err)
	}
	if err := guard.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if generation, err := guard.AllowWrite(); err != nil || generation != generationA || verifyCalls != 1 {
		t.Fatalf("generation=%q verify_calls=%d error=%v", generation, verifyCalls, err)
	}

	backend.setIdentity(generationB)
	backend.setVerified(generationA)
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh changed generation: %v", err)
	}
	if generation, err := guard.AllowWrite(); err != nil || generation != generationB || verifyCalls != 2 {
		t.Fatalf("generation=%q verify_calls=%d error=%v", generation, verifyCalls, err)
	}
}

func TestGuardStaysFrozenOnVerificationOrIdentityFailure(t *testing.T) {
	verifyErr := errors.New("projection is behind")
	backend := &fakeBackend{identity: generationA}
	guard, err := NewGuard(backend, Config{Verify: func(context.Context) error { return verifyErr }})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Initialize(context.Background()); !errors.Is(err, ErrVerificationWait) {
		t.Fatalf("initialize error=%v", err)
	}
	if _, err := guard.AllowWrite(); !errors.Is(err, ErrFrozen) {
		t.Fatalf("allow error=%v", err)
	}

	backend.setError(errors.New("Redis unavailable"))
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrFrozen) {
		t.Fatalf("refresh error=%v", err)
	}
	status := guard.Status()
	if status.Ready || !strings.Contains(status.Reason, "Redis unavailable") {
		t.Fatalf("status=%+v", status)
	}
}

func TestSentinelSignalRequiresAChangedIdentityBeforeUnfreeze(t *testing.T) {
	backend := &fakeBackend{identity: generationA}
	guard, err := NewGuard(backend, Config{Verify: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	guard.SignalSwitchMaster()
	if _, err := guard.AllowWrite(); !errors.Is(err, ErrFrozen) {
		t.Fatalf("signal did not freeze: %v", err)
	}
	if err := guard.Refresh(context.Background()); !errors.Is(err, ErrVerificationWait) {
		t.Fatalf("unchanged identity error=%v", err)
	}
	backend.setIdentity(generationB)
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatalf("changed identity refresh: %v", err)
	}
}

func TestFollowerGuardWaitsForCoordinatorMarker(t *testing.T) {
	backend := &fakeBackend{identity: generationA, verified: generationB}
	guard, err := NewGuard(backend, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Initialize(context.Background()); !errors.Is(err, ErrVerificationWait) {
		t.Fatalf("initialize error=%v", err)
	}
	backend.setVerified(generationA)
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after coordinator: %v", err)
	}
	if _, err := guard.AllowWrite(); err != nil {
		t.Fatalf("allow after coordinator: %v", err)
	}
}

func TestCoordinatorRunsResidentVerificationOnAStableGeneration(t *testing.T) {
	backend := &fakeBackend{identity: generationA}
	verifyCalls := 0
	guard, err := NewGuard(backend, Config{
		VerifyInterval: time.Nanosecond,
		Verify: func(context.Context) error {
			verifyCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if verifyCalls != 2 {
		t.Fatalf("verify calls=%d want=2", verifyCalls)
	}
}

func TestGuardRunPollsUntilCancellationAndPingReflectsReadiness(t *testing.T) {
	backend := &fakeBackend{identity: generationA, verified: generationA}
	guard, err := NewGuard(backend, Config{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := guard.Ping(context.Background()); err != nil {
		t.Fatalf("ready Ping: %v", err)
	}
	before := backend.primaryCalls.Load()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		guard.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for backend.primaryCalls.Load() <= before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backend.primaryCalls.Load() <= before {
		t.Fatal("Run did not poll the backend")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if err := guard.Ping(context.Background()); !errors.Is(err, ErrFrozen) {
		t.Fatalf("stopped Ping error=%v want ErrFrozen", err)
	}

	var nilGuard *Guard
	nilGuard.Run(context.Background())
	if err := nilGuard.Ping(context.Background()); !errors.Is(err, ErrFrozen) {
		t.Fatalf("nil Ping error=%v want ErrFrozen", err)
	}
}

func TestParsePrimaryIdentityRequiresMasterAndHexRunID(t *testing.T) {
	info := "# Server\r\nrun_id:" + generationA + "\r\n# Replication\r\nrole:master\r\n"
	if got, err := ParsePrimaryIdentity(info); err != nil || got != generationA {
		t.Fatalf("identity=%q error=%v", got, err)
	}
	for _, invalid := range []string{
		"run_id:" + generationA + "\nrole:slave\n",
		"run_id:not-hex\nrole:master\n",
		"run_id:" + generationA + "\n",
	} {
		if _, err := ParsePrimaryIdentity(invalid); err == nil {
			t.Fatalf("invalid INFO accepted: %q", invalid)
		}
	}
}

type fakeBackend struct {
	mu           sync.Mutex
	identity     string
	verified     string
	err          error
	primaryCalls atomic.Int64
}

func (backend *fakeBackend) PrimaryIdentity(context.Context) (string, error) {
	backend.primaryCalls.Add(1)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.identity, backend.err
}

func (backend *fakeBackend) VerifiedGeneration(context.Context) (string, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.verified, backend.err
}

func (backend *fakeBackend) SetVerifiedGeneration(_ context.Context, generation string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.err != nil {
		return backend.err
	}
	backend.verified = generation
	return nil
}

func (backend *fakeBackend) setIdentity(identity string) {
	backend.mu.Lock()
	backend.identity = identity
	backend.mu.Unlock()
}

func (backend *fakeBackend) setVerified(verified string) {
	backend.mu.Lock()
	backend.verified = verified
	backend.mu.Unlock()
}

func (backend *fakeBackend) setError(err error) {
	backend.mu.Lock()
	backend.err = err
	backend.mu.Unlock()
}
