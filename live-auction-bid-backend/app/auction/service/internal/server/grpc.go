package server

import (
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	grpctransport "github.com/go-kratos/kratos/v2/transport/grpc"
)

// NewAuctionGRPCServer exposes the synchronous auction boundary used by the
// edge gateway. Business rules remain in AuctionService and its usecase; this
// transport only handles RPC middleware and service registration.
func NewAuctionGRPCServer(addr string, commands v1.AuctionCommandServiceServer, authMiddleware middleware.Middleware) *grpctransport.Server {
	middlewares := []middleware.Middleware{recovery.Recovery(), requestctx.RPCMiddleware()}
	if authMiddleware != nil {
		middlewares = append(middlewares, authMiddleware)
	}
	srv := grpctransport.NewServer(
		grpctransport.Address(addr),
		grpctransport.Middleware(middlewares...),
	)
	v1.RegisterAuctionCommandServiceServer(srv, commands)
	return srv
}
