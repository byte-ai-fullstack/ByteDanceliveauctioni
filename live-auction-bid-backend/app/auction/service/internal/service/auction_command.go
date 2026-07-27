package service

import (
	"context"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

// AuctionCommandService narrows the internal gRPC surface to live-auction
// commands while reusing the public service adapter's authorization,
// validation, and response contract. Draft and query APIs stay in gateway.
type AuctionCommandService struct {
	v1.UnimplementedAuctionCommandServiceServer
	auction *AuctionService
}

func NewAuctionCommandService(auction *AuctionService) *AuctionCommandService {
	return &AuctionCommandService{auction: auction}
}

func (s *AuctionCommandService) StartLot(ctx context.Context, req *v1.StartLotRequest) (*v1.StartLotReply, error) {
	return s.auction.StartLot(ctx, req)
}

func (s *AuctionCommandService) PlaceBid(ctx context.Context, req *v1.PlaceBidRequest) (*v1.PlaceBidReply, error) {
	return s.auction.PlaceBid(ctx, req)
}

func (s *AuctionCommandService) RevealTrustCard(ctx context.Context, req *v1.RevealTrustCardRequest) (*v1.RevealTrustCardReply, error) {
	return s.auction.RevealTrustCard(ctx, req)
}

func (s *AuctionCommandService) StartDuel(ctx context.Context, req *v1.StartDuelRequest) (*v1.StartDuelReply, error) {
	return s.auction.StartDuel(ctx, req)
}

func (s *AuctionCommandService) SettleLot(ctx context.Context, req *v1.SettleLotRequest) (*v1.SettleLotReply, error) {
	return s.auction.SettleLot(ctx, req)
}

func (s *AuctionCommandService) CancelLot(ctx context.Context, req *v1.CancelLotRequest) (*v1.CancelLotReply, error) {
	return s.auction.CancelLot(ctx, req)
}
