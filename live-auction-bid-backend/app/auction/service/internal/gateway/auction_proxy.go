package gateway

import (
	"context"
	"errors"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"

	"google.golang.org/grpc/metadata"
)

const defaultCommandTimeout = 3 * time.Second

// AuctionProxy keeps read and draft-management calls on the supplied HTTP
// service, but routes all live-auction commands through the private
// auction-service RPC.
// A new core command must be explicitly overridden here before it is remote.
type AuctionProxy struct {
	v1.AuctionServiceHTTPServer
	commands v1.AuctionCommandServiceClient
	timeout  time.Duration
}

func NewAuctionProxy(local v1.AuctionServiceHTTPServer, commands v1.AuctionCommandServiceClient, timeout time.Duration) (*AuctionProxy, error) {
	if local == nil {
		return nil, errors.New("local auction HTTP service is required")
	}
	if commands == nil {
		return nil, errors.New("auction command client is required")
	}
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return &AuctionProxy{AuctionServiceHTTPServer: local, commands: commands, timeout: timeout}, nil
}

func (p *AuctionProxy) StartLot(ctx context.Context, req *v1.StartLotRequest) (*v1.StartLotReply, error) {
	ctx, cancel := p.commandContext(ctx)
	defer cancel()
	return p.commands.StartLot(ctx, req)
}

func (p *AuctionProxy) PlaceBid(ctx context.Context, req *v1.PlaceBidRequest) (*v1.PlaceBidReply, error) {
	ctx, cancel := p.commandContext(ctx)
	defer cancel()
	return p.commands.PlaceBid(ctx, req)
}

func (p *AuctionProxy) RevealTrustCard(ctx context.Context, req *v1.RevealTrustCardRequest) (*v1.RevealTrustCardReply, error) {
	ctx, cancel := p.commandContext(ctx)
	defer cancel()
	return p.commands.RevealTrustCard(ctx, req)
}

func (p *AuctionProxy) StartDuel(ctx context.Context, req *v1.StartDuelRequest) (*v1.StartDuelReply, error) {
	ctx, cancel := p.commandContext(ctx)
	defer cancel()
	return p.commands.StartDuel(ctx, req)
}

func (p *AuctionProxy) SettleLot(ctx context.Context, req *v1.SettleLotRequest) (*v1.SettleLotReply, error) {
	ctx, cancel := p.commandContext(ctx)
	defer cancel()
	return p.commands.SettleLot(ctx, req)
}

func (p *AuctionProxy) CancelLot(ctx context.Context, req *v1.CancelLotRequest) (*v1.CancelLotReply, error) {
	ctx, cancel := p.commandContext(ctx)
	defer cancel()
	return p.commands.CancelLot(ctx, req)
}

func (p *AuctionProxy) commandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	pairs := make([]string, 0, 6)
	if authContext, ok := auth.AuthContextFromContext(ctx); ok && authContext.RawToken != "" {
		pairs = append(pairs, "authorization", "Bearer "+authContext.RawToken)
	}
	request := requestctx.Snapshot(ctx)
	if request.RequestID != "" {
		pairs = append(pairs, "x-request-id", request.RequestID)
	}
	if request.TraceID != "" {
		pairs = append(pairs, "x-trace-id", request.TraceID)
	}
	if len(pairs) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
	}
	return context.WithTimeout(ctx, p.timeout)
}
