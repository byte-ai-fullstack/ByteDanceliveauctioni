package server

import (
	"context"
	"net/url"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type authenticatedAuctionCommandService struct {
	v1.UnimplementedAuctionCommandServiceServer
}

func (authenticatedAuctionCommandService) StartLot(ctx context.Context, _ *v1.StartLotRequest) (*v1.StartLotReply, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return &v1.StartLotReply{}, nil
	}
	return &v1.StartLotReply{
		Lot:    &v1.Lot{Id: claims.UserID},
		Result: &v1.ReplyResult{TraceId: requestctx.TraceID(ctx)},
	}, nil
}

func TestAuctionGRPCServerAuthenticatesForwardedBearerToken(t *testing.T) {
	manager, err := auth.NewManager(auth.Config{Secret: "grpc-test-secret", Issuer: "grpc-test"})
	if err != nil {
		t.Fatalf("create auth manager: %v", err)
	}
	tokens, err := manager.IssueTokenPair(&v1.User{
		Id:              "buyer-1",
		Username:        "buyer",
		RoleCodes:       []string{"buyer"},
		PermissionCodes: []string{"bid.place"},
		Status:          v1.UserStatus_USER_STATUS_ACTIVE,
	})
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	srv := NewAuctionGRPCServer("127.0.0.1:0", authenticatedAuctionCommandService{}, manager.Middleware())
	endpoint, err := srv.Endpoint()
	if err != nil {
		t.Fatalf("resolve gRPC endpoint: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Start(context.Background()) }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Stop(stopCtx); err != nil {
			t.Errorf("stop gRPC server: %v", err)
		}
		select {
		case <-serveErr:
		case <-time.After(time.Second):
			t.Error("gRPC server did not stop")
		}
	})

	conn, err := grpc.NewClient(grpcDialTarget(endpoint), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gRPC server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(
		ctx,
		"authorization", "Bearer "+tokens.AccessToken,
		"x-request-id", "request-1",
		"x-trace-id", "trace-1",
	)
	reply, err := v1.NewAuctionCommandServiceClient(conn).StartLot(ctx, &v1.StartLotRequest{LotId: "lot-1"})
	if err != nil {
		t.Fatalf("call StartLot: %v", err)
	}
	if reply.GetLot().GetId() != "buyer-1" {
		t.Fatalf("gRPC auth claims were not propagated, lot=%v", reply.GetLot())
	}
	if reply.GetResult().GetTraceId() != "trace-1" {
		t.Fatalf("gRPC trace id was not propagated, result=%v", reply.GetResult())
	}
}

func grpcDialTarget(endpoint *url.URL) string {
	if endpoint == nil {
		return ""
	}
	return endpoint.Host
}
