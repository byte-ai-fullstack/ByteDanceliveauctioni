package gateway

import (
	"context"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type localAuctionHTTPStub struct {
	v1.AuctionServiceHTTPServer
	getLotCalls int
}

func (s *localAuctionHTTPStub) GetLot(context.Context, *v1.GetLotRequest) (*v1.GetLotReply, error) {
	s.getLotCalls++
	return &v1.GetLotReply{Lot: &v1.Lot{Id: "local"}}, nil
}

type commandClientStub struct {
	methods  []string
	metadata metadata.MD
}

func (s *commandClientStub) capture(ctx context.Context, method string) {
	s.methods = append(s.methods, method)
	s.metadata, _ = metadata.FromOutgoingContext(ctx)
}

func (s *commandClientStub) StartLot(ctx context.Context, _ *v1.StartLotRequest, _ ...grpc.CallOption) (*v1.StartLotReply, error) {
	s.capture(ctx, "start")
	return &v1.StartLotReply{}, nil
}

func (s *commandClientStub) PlaceBid(ctx context.Context, _ *v1.PlaceBidRequest, _ ...grpc.CallOption) (*v1.PlaceBidReply, error) {
	s.capture(ctx, "bid")
	return &v1.PlaceBidReply{}, nil
}

func (s *commandClientStub) RevealTrustCard(ctx context.Context, _ *v1.RevealTrustCardRequest, _ ...grpc.CallOption) (*v1.RevealTrustCardReply, error) {
	s.capture(ctx, "reveal")
	return &v1.RevealTrustCardReply{}, nil
}

func (s *commandClientStub) StartDuel(ctx context.Context, _ *v1.StartDuelRequest, _ ...grpc.CallOption) (*v1.StartDuelReply, error) {
	s.capture(ctx, "duel")
	return &v1.StartDuelReply{}, nil
}

func (s *commandClientStub) SettleLot(ctx context.Context, _ *v1.SettleLotRequest, _ ...grpc.CallOption) (*v1.SettleLotReply, error) {
	s.capture(ctx, "settle")
	return &v1.SettleLotReply{}, nil
}

func (s *commandClientStub) CancelLot(ctx context.Context, _ *v1.CancelLotRequest, _ ...grpc.CallOption) (*v1.CancelLotReply, error) {
	s.capture(ctx, "cancel")
	return &v1.CancelLotReply{}, nil
}

func TestAuctionProxyRoutesAllLiveCommands(t *testing.T) {
	local := &localAuctionHTTPStub{}
	commands := &commandClientStub{}
	proxy, err := NewAuctionProxy(local, commands, time.Second)
	if err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	ctx := context.Background()
	if _, err := proxy.StartLot(ctx, &v1.StartLotRequest{}); err != nil {
		t.Fatalf("start lot: %v", err)
	}
	if _, err := proxy.PlaceBid(ctx, &v1.PlaceBidRequest{}); err != nil {
		t.Fatalf("place bid: %v", err)
	}
	if _, err := proxy.RevealTrustCard(ctx, &v1.RevealTrustCardRequest{}); err != nil {
		t.Fatalf("reveal trust card: %v", err)
	}
	if _, err := proxy.StartDuel(ctx, &v1.StartDuelRequest{}); err != nil {
		t.Fatalf("start duel: %v", err)
	}
	if _, err := proxy.SettleLot(ctx, &v1.SettleLotRequest{}); err != nil {
		t.Fatalf("settle lot: %v", err)
	}
	if _, err := proxy.CancelLot(ctx, &v1.CancelLotRequest{}); err != nil {
		t.Fatalf("cancel lot: %v", err)
	}
	if _, err := proxy.GetLot(ctx, &v1.GetLotRequest{}); err != nil {
		t.Fatalf("get lot: %v", err)
	}
	if got := commands.methods; len(got) != 6 || got[0] != "start" || got[1] != "bid" || got[2] != "reveal" ||
		got[3] != "duel" || got[4] != "settle" || got[5] != "cancel" {
		t.Fatalf("unexpected command routes: %v", got)
	}
	if local.getLotCalls != 1 {
		t.Fatalf("read path did not stay local: calls=%d", local.getLotCalls)
	}
}

func TestAuctionProxyForwardsVerifiedIdentityAndRequestMetadata(t *testing.T) {
	manager, err := auth.NewManager(auth.Config{Secret: "proxy-test-secret", Issuer: "proxy-test"})
	if err != nil {
		t.Fatalf("create auth manager: %v", err)
	}
	tokens, err := manager.IssueTokenPair(&v1.User{
		Id:        "buyer-1",
		Username:  "buyer",
		RoleCodes: []string{"buyer"},
		Status:    v1.UserStatus_USER_STATUS_ACTIVE,
	})
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	ctx := manager.WithAuthContextFromBearer(context.Background(), "Bearer "+tokens.AccessToken)
	ctx = requestctx.WithRequestContext(ctx, requestctx.RequestContext{RequestID: "request-1", TraceID: "trace-1"})
	commands := &commandClientStub{}
	proxy, err := NewAuctionProxy(&localAuctionHTTPStub{}, commands, time.Second)
	if err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	if _, err := proxy.PlaceBid(ctx, &v1.PlaceBidRequest{}); err != nil {
		t.Fatalf("place bid: %v", err)
	}
	if got := commands.metadata.Get("authorization"); len(got) != 1 || got[0] != "Bearer "+tokens.AccessToken {
		t.Fatalf("authorization metadata not forwarded: %v", got)
	}
	if got := commands.metadata.Get("x-request-id"); len(got) != 1 || got[0] != "request-1" {
		t.Fatalf("request id metadata not forwarded: %v", got)
	}
	if got := commands.metadata.Get("x-trace-id"); len(got) != 1 || got[0] != "trace-1" {
		t.Fatalf("trace id metadata not forwarded: %v", got)
	}
}

func TestNewAuctionProxyRejectsMissingDependencies(t *testing.T) {
	commands := &commandClientStub{}
	if _, err := NewAuctionProxy(nil, commands, time.Second); err == nil {
		t.Fatal("missing local service was accepted")
	}
	if _, err := NewAuctionProxy(&localAuctionHTTPStub{}, nil, time.Second); err == nil {
		t.Fatal("missing command client was accepted")
	}
}
