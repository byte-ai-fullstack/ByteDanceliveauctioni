package gateway

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/connectivity"
)

type grpcConnectionState interface {
	GetState() connectivity.State
	Connect()
	WaitForStateChange(context.Context, connectivity.State) bool
}

// CommandHealthChecker reports whether the gateway has a usable transport to
// auction-service. It never invokes an auction command.
type CommandHealthChecker struct {
	connection grpcConnectionState
}

// NewCommandHealthChecker binds readiness to a gRPC client connection.
func NewCommandHealthChecker(connection grpcConnectionState) (*CommandHealthChecker, error) {
	if connection == nil {
		return nil, errors.New("auction command connection is required")
	}
	return &CommandHealthChecker{connection: connection}, nil
}

func (checker *CommandHealthChecker) Ping(ctx context.Context) error {
	if checker == nil || checker.connection == nil {
		return errors.New("auction command connection is not initialized")
	}
	for {
		state := checker.connection.GetState()
		switch state {
		case connectivity.Ready:
			return nil
		case connectivity.Shutdown:
			return errors.New("auction command connection is shut down")
		case connectivity.Idle:
			checker.connection.Connect()
		}
		if !checker.connection.WaitForStateChange(ctx, state) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("auction command connection remained %s", state)
		}
	}
}
