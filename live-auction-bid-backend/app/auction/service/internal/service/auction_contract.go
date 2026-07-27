package service

import (
	"context"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/aiassistant"
	auctionbiz "live-auction-bid/backend/app/auction/service/internal/biz/auction"
	shopbiz "live-auction-bid/backend/app/auction/service/internal/biz/shop"
)

func (s *AuctionService) GetLotResultView(ctx context.Context, req *v1.GetLotResultRequest) (*v1.GetLotResultReply, error) {
	result, err := s.auction.GetLotResult(ctx, req.GetLotId(), lotResultViewerFromContext(ctx))
	if err != nil {
		return &v1.GetLotResultReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.GetLotResultReply{
		Result:       okResult(ctx),
		Lot:          result.Lot,
		AuctionState: string(result.AuctionState),
		Order:        auctionOrderSummaryToProto(result.Order),
	}, nil
}

func (s *AuctionService) ListMyAuctionOrders(ctx context.Context, req *v1.ListAuctionOrdersRequest) (*v1.ListAuctionOrdersReply, error) {
	list, err := s.ListMyOrdersPage(ctx, auctionOrderQueryFromProto(req))
	if err != nil {
		return &v1.ListAuctionOrdersReply{Result: ErrorResult(ctx, err), Orders: []*v1.AuctionOrderSummary{}}, nil
	}
	return auctionOrdersReply(ctx, list), nil
}

func (s *AuctionService) ListAdminAuctionOrders(ctx context.Context, req *v1.ListAuctionOrdersRequest) (*v1.ListAuctionOrdersReply, error) {
	list, err := s.ListOrders(ctx, auctionOrderQueryFromProto(req))
	if err != nil {
		return &v1.ListAuctionOrdersReply{Result: ErrorResult(ctx, err), Orders: []*v1.AuctionOrderSummary{}}, nil
	}
	return auctionOrdersReply(ctx, list), nil
}

func (s *AuctionService) ListMyBidRecords(ctx context.Context, req *v1.ListBidRecordsRequest) (*v1.ListBidRecordsReply, error) {
	list, err := s.ListMyBids(ctx, auctionbiz.BidRecordQuery{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		LotID:    req.GetLotId(),
	})
	if err != nil {
		return &v1.ListBidRecordsReply{Result: ErrorResult(ctx, err), Bids: []*v1.AuctionBidRecord{}}, nil
	}
	out := &v1.ListBidRecordsReply{
		Result:   okResult(ctx),
		Bids:     make([]*v1.AuctionBidRecord, 0, len(list.Bids)),
		Total:    list.Total,
		Page:     int32(list.Page),
		PageSize: int32(list.PageSize),
	}
	for _, bid := range list.Bids {
		out.Bids = append(out.Bids, auctionBidRecordToProto(bid))
	}
	return out, nil
}

func (s *AuctionService) ListAdminLotPage(ctx context.Context, req *v1.ListAdminLotPageRequest) (*v1.ListAdminLotPageReply, error) {
	list, err := s.ListAdminLots(ctx, auctionbiz.LotQuery{
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
		Status:   req.GetStatus(),
		View:     req.GetView(),
		Keyword:  req.GetKeyword(),
		RoomID:   req.GetRoomId(),
	})
	if err != nil {
		return &v1.ListAdminLotPageReply{Result: ErrorResult(ctx, err), Lots: []*v1.Lot{}}, nil
	}
	return &v1.ListAdminLotPageReply{
		Result:   okResult(ctx),
		Lots:     list.Lots,
		Total:    list.Total,
		Page:     int32(list.Page),
		PageSize: int32(list.PageSize),
	}, nil
}

func (s *AuctionService) ListAdminRoomList(ctx context.Context, _ *v1.ListAdminRoomsRequest) (*v1.ListRoomsReply, error) {
	rooms, err := s.ListAdminRooms(ctx)
	if err != nil {
		return &v1.ListRoomsReply{Result: ErrorResult(ctx, err), Rooms: []*v1.AuctionRoom{}}, nil
	}
	return &v1.ListRoomsReply{Result: okResult(ctx), Rooms: auctionRoomsToProto(rooms)}, nil
}

func (s *AuctionService) ListPublicRoomList(ctx context.Context, _ *v1.ListPublicRoomsRequest) (*v1.ListPublicRoomsReply, error) {
	rooms, err := s.ListPublicRooms(ctx)
	if err != nil {
		return &v1.ListPublicRoomsReply{Result: ErrorResult(ctx, err), Rooms: []*v1.AuctionRoom{}}, nil
	}
	return &v1.ListPublicRoomsReply{Result: okResult(ctx), Rooms: auctionRoomsToProto(rooms)}, nil
}

func (s *AuctionService) ListBuyerSuggestions(ctx context.Context, req *v1.ListBuyerSuggestionsRequest) (*v1.ListBuyerSuggestionsReply, error) {
	reply, err := s.BuyerSuggestions(ctx, int(req.GetLimit()))
	if err != nil {
		return &v1.ListBuyerSuggestionsReply{Result: ErrorResult(ctx, err), Suggestions: []*v1.BuyerSuggestion{}}, nil
	}
	out := &v1.ListBuyerSuggestionsReply{
		Result:       okResult(ctx),
		Suggestions:  make([]*v1.BuyerSuggestion, 0, len(reply.Suggestions)),
		FallbackUsed: reply.FallbackUsed,
	}
	for _, suggestion := range reply.Suggestions {
		out.Suggestions = append(out.Suggestions, buyerSuggestionToProto(suggestion))
	}
	return out, nil
}

func auctionOrderQueryFromProto(req *v1.ListAuctionOrdersRequest) auctionbiz.OrderQuery {
	if req == nil {
		return auctionbiz.OrderQuery{}
	}
	return auctionbiz.OrderQuery{
		Page:          int(req.GetPage()),
		PageSize:      int(req.GetPageSize()),
		Status:        auctionbiz.OrderStatus(req.GetStatus()),
		PaymentStatus: auctionbiz.PaymentStatus(req.GetPaymentStatus()),
		LotID:         req.GetLotId(),
		Buyer:         req.GetBuyer(),
	}
}

func auctionOrdersReply(ctx context.Context, list auctionbiz.OrderList) *v1.ListAuctionOrdersReply {
	out := &v1.ListAuctionOrdersReply{
		Result:   okResult(ctx),
		Orders:   make([]*v1.AuctionOrderSummary, 0, len(list.Orders)),
		Total:    list.Total,
		Page:     int32(list.Page),
		PageSize: int32(list.PageSize),
	}
	for _, order := range list.Orders {
		out.Orders = append(out.Orders, auctionOrderSummaryToProto(&order))
	}
	return out
}

func auctionOrderSummaryToProto(order *auctionbiz.OrderSummary) *v1.AuctionOrderSummary {
	if order == nil {
		return nil
	}
	return &v1.AuctionOrderSummary{
		Id:                        order.ID,
		MainAccountId:             order.MainAccountID,
		LotId:                     order.LotID,
		RoomId:                    order.RoomID,
		LotTitle:                  order.LotTitle,
		LotImageUrl:               order.LotImageURL,
		BuyerUserId:               order.BuyerUserID,
		BuyerNickname:             order.BuyerNickname,
		Status:                    string(order.Status),
		PaymentStatus:             string(order.PaymentStatus),
		PaymentId:                 order.PaymentID,
		ShopName:                  order.ShopName,
		EnrichmentStatus:          string(order.EnrichmentStatus),
		EnrichmentUpdatedAtUnixMs: order.EnrichmentUpdatedAtMs,
		ShippingAddressId:         order.ShippingAddressID,
		ShippingAddressSnapshot:   auctionAddressSnapshotToProto(order.ShippingAddressSnapshot),
		Amount:                    order.Amount,
		Currency:                  order.Currency,
		CreatedAtUnixMs:           order.CreatedAtUnixMs,
		UpdatedAtUnixMs:           order.UpdatedAtUnixMs,
		ExpiresAtUnixMs:           order.ExpiresAtUnixMs,
		PaidAtUnixMs:              order.PaidAtUnixMs,
	}
}

func auctionAddressSnapshotToProto(snapshot *shopbiz.DeliveryAddressSnapshot) *v1.AuctionDeliveryAddressSnapshot {
	if snapshot == nil {
		return nil
	}
	return &v1.AuctionDeliveryAddressSnapshot{
		AddressId:    snapshot.AddressID,
		ReceiverName: snapshot.ReceiverName,
		Phone:        snapshot.Phone,
		Province:     snapshot.Province,
		City:         snapshot.City,
		District:     snapshot.District,
		Street:       snapshot.Street,
		Detail:       snapshot.Detail,
		PostalCode:   snapshot.PostalCode,
		FullAddress:  snapshot.FullAddress,
	}
}

func auctionBidRecordToProto(bid auctionbiz.BidRecord) *v1.AuctionBidRecord {
	return &v1.AuctionBidRecord{
		Id:              bid.ID,
		LotId:           bid.LotID,
		RoomId:          bid.RoomID,
		LotTitle:        bid.LotTitle,
		LotImageUrl:     bid.LotImageURL,
		UserId:          bid.UserID,
		Nickname:        bid.Nickname,
		Amount:          bid.Amount,
		Currency:        bid.Currency,
		CreatedAtUnixMs: bid.CreatedAtUnixMs,
		LotStatus:       bid.LotStatus,
		AuctionState:    string(bid.AuctionState),
		Won:             bid.Won,
	}
}

func auctionRoomsToProto(rooms []auctionbiz.Room) []*v1.AuctionRoom {
	out := make([]*v1.AuctionRoom, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, &v1.AuctionRoom{
			Id:                  room.ID,
			MainAccountId:       room.MainAccountID,
			Name:                room.Name,
			Platform:            room.Platform,
			PlatformRoomId:      room.PlatformRoomID,
			LiveSourceUrl:       room.LiveSourceURL,
			LiveStartedAtUnixMs: room.LiveStartedAtUnixMs,
			Status:              string(room.Status),
			CreatedByUserId:     room.CreatedByUserID,
			CreatedAtUnixMs:     room.CreatedAtUnixMs,
			UpdatedAtUnixMs:     room.UpdatedAtUnixMs,
		})
	}
	return out
}

func buyerSuggestionToProto(suggestion aiassistant.BuyerSuggestion) *v1.BuyerSuggestion {
	return &v1.BuyerSuggestion{
		Text:   suggestion.Text,
		Reason: suggestion.Reason,
	}
}
