package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/connectivity"
)

type commandConnectionStub struct {
	state        connectivity.State
	connectCalls int
	nextState    connectivity.State
	waitForCtx   bool
}

func (stub *commandConnectionStub) GetState() connectivity.State { return stub.state }

func (stub *commandConnectionStub) Connect() {
	stub.connectCalls++
	stub.state = connectivity.Connecting
}

func (stub *commandConnectionStub) WaitForStateChange(ctx context.Context, _ connectivity.State) bool {
	if stub.waitForCtx {
		<-ctx.Done()
		return false
	}
	stub.state = stub.nextState
	return true
}

func TestCommandHealthCheckerConnectsIdleTransport(t *testing.T) {
	connection := &commandConnectionStub{state: connectivity.Idle, nextState: connectivity.Ready}
	checker, err := NewCommandHealthChecker(connection)
	if err != nil {
		t.Fatalf("NewCommandHealthChecker: %v", err)
	}
	if err := checker.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if connection.connectCalls != 1 {
		t.Fatalf("Connect calls = %d", connection.connectCalls)
	}
}

func TestCommandHealthCheckerFailsClosed(t *testing.T) {
	if _, err := NewCommandHealthChecker(nil); err == nil {
		t.Fatal("nil connection was accepted")
	}
	checker, err := NewCommandHealthChecker(&commandConnectionStub{state: connectivity.Shutdown})
	if err != nil {
		t.Fatalf("NewCommandHealthChecker: %v", err)
	}
	if err := checker.Ping(context.Background()); err == nil {
		t.Fatal("shutdown connection was ready")
	}

	checker, err = NewCommandHealthChecker(&commandConnectionStub{state: connectivity.TransientFailure, waitForCtx: true})
	if err != nil {
		t.Fatalf("NewCommandHealthChecker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := checker.Ping(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ping timeout error = %v", err)
	}
}
