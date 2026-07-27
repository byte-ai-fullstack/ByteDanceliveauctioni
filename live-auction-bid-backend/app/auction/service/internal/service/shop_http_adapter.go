package service

import (
	"context"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	auctionbiz "live-auction-bid/backend/app/auction/service/internal/biz/auction"
	shopbiz "live-auction-bid/backend/app/auction/service/internal/biz/shop"
)

type ShopServiceHTTPAdapter struct {
	shop    *ShopService
	orders  *OrderService
	auction *AuctionService
}

func NewShopServiceHTTPAdapter(shop *ShopService, orders *OrderService, auction *AuctionService) *ShopServiceHTTPAdapter {
	return &ShopServiceHTTPAdapter{shop: shop, orders: orders, auction: auction}
}

func (s *ShopServiceHTTPAdapter) ListProducts(ctx context.Context, req *v1.ListProductsRequest) (*v1.ListProductsReply, error) {
	list, err := s.shop.ListProducts(ctx, shopbiz.ProductQuery{
		Query:    req.GetQ(),
		Category: req.GetCategory(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return &v1.ListProductsReply{Result: ErrorResult(ctx, err), Products: []*v1.Product{}}, nil
	}
	out := &v1.ListProductsReply{
		Result:   okResult(ctx),
		Products: make([]*v1.Product, 0, len(list.Products)),
		Total:    list.Total,
		Page:     int32(list.Page),
		PageSize: int32(list.PageSize),
	}
	for _, product := range list.Products {
		out.Products = append(out.Products, shopProductToProto(product))
	}
	return out, nil
}

func (s *ShopServiceHTTPAdapter) GetProduct(ctx context.Context, req *v1.GetProductRequest) (*v1.GetProductReply, error) {
	product, err := s.shop.GetProduct(ctx, req.GetProductId())
	if err != nil {
		return &v1.GetProductReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.GetProductReply{Result: okResult(ctx), Product: shopProductToProto(*product)}, nil
}

func (s *ShopServiceHTTPAdapter) ListDeliveryAddresses(ctx context.Context, _ *v1.ListDeliveryAddressesRequest) (*v1.ListDeliveryAddressesReply, error) {
	addresses, err := s.shop.ListDeliveryAddresses(ctx)
	if err != nil {
		return &v1.ListDeliveryAddressesReply{Result: ErrorResult(ctx, err), Addresses: []*v1.DeliveryAddress{}}, nil
	}
	return &v1.ListDeliveryAddressesReply{Result: okResult(ctx), Addresses: deliveryAddressesToProto(addresses)}, nil
}

func (s *ShopServiceHTTPAdapter) CreateDeliveryAddress(ctx context.Context, req *v1.CreateDeliveryAddressRequest) (*v1.CreateDeliveryAddressReply, error) {
	address, err := s.shop.CreateDeliveryAddress(ctx, deliveryAddressInputFromProto(req.GetAddress()))
	if err != nil {
		return &v1.CreateDeliveryAddressReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.CreateDeliveryAddressReply{Result: okResult(ctx), Address: deliveryAddressToProto(*address)}, nil
}

func (s *ShopServiceHTTPAdapter) UpdateDeliveryAddress(ctx context.Context, req *v1.UpdateDeliveryAddressRequest) (*v1.UpdateDeliveryAddressReply, error) {
	address, err := s.shop.UpdateDeliveryAddress(ctx, req.GetAddressId(), deliveryAddressInputFromProto(req.GetAddress()))
	if err != nil {
		return &v1.UpdateDeliveryAddressReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.UpdateDeliveryAddressReply{Result: okResult(ctx), Address: deliveryAddressToProto(*address)}, nil
}

func (s *ShopServiceHTTPAdapter) DeleteDeliveryAddress(ctx context.Context, req *v1.DeleteDeliveryAddressRequest) (*v1.DeleteDeliveryAddressReply, error) {
	if err := s.shop.DeleteDeliveryAddress(ctx, req.GetAddressId()); err != nil {
		return &v1.DeleteDeliveryAddressReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.DeleteDeliveryAddressReply{Result: okResult(ctx)}, nil
}

func (s *ShopServiceHTTPAdapter) SetDefaultDeliveryAddress(ctx context.Context, req *v1.SetDefaultDeliveryAddressRequest) (*v1.ListDeliveryAddressesReply, error) {
	addresses, err := s.shop.SetDefaultDeliveryAddress(ctx, req.GetAddressId())
	if err != nil {
		return &v1.ListDeliveryAddressesReply{Result: ErrorResult(ctx, err), Addresses: []*v1.DeliveryAddress{}}, nil
	}
	return &v1.ListDeliveryAddressesReply{Result: okResult(ctx), Addresses: deliveryAddressesToProto(addresses)}, nil
}

func (s *ShopServiceHTTPAdapter) CreateDepositHold(ctx context.Context, req *v1.CreateDepositHoldRequest) (*v1.CreateDepositHoldReply, error) {
	result, err := s.auction.CreateDepositHold(ctx, req.GetLotId(), auctionbiz.CreateDepositHoldRequest{
		AddressID:      req.GetAddressId(),
		IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return &v1.CreateDepositHoldReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.CreateDepositHoldReply{
		Result:      okResult(ctx),
		DepositHold: depositHoldToProto(result.DepositHold),
		Paid:        result.Paid,
	}, nil
}

func (s *ShopServiceHTTPAdapter) GetMyDepositHold(ctx context.Context, req *v1.GetMyDepositHoldRequest) (*v1.GetMyDepositHoldReply, error) {
	hold, found, err := s.auction.GetMyDepositHold(ctx, req.GetLotId())
	if err != nil {
		return &v1.GetMyDepositHoldReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.GetMyDepositHoldReply{
		Result:      okResult(ctx),
		DepositHold: depositHoldPtrToProto(hold),
		Found:       found,
		Paid:        found && hold.Status == auctionbiz.DepositStatusHeld,
	}, nil
}

func (s *ShopServiceHTTPAdapter) CreateShopOrder(ctx context.Context, req *v1.CreateShopOrderRequest) (*v1.CreateShopOrderReply, error) {
	order, err := s.shop.CreateOrder(ctx, shopbiz.CreateOrderRequest{
		SKUID:           req.GetSkuId(),
		Quantity:        req.GetQuantity(),
		AddressID:       req.GetAddressId(),
		AddressSnapshot: req.GetAddressSnapshot(),
		IdempotencyKey:  req.GetIdempotencyKey(),
	})
	if err != nil {
		return &v1.CreateShopOrderReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.CreateShopOrderReply{Result: okResult(ctx), Order: shopOrderToProto(*order)}, nil
}

func (s *ShopServiceHTTPAdapter) ListMyShopOrders(ctx context.Context, req *v1.ListMyShopOrdersRequest) (*v1.ListMyShopOrdersReply, error) {
	list, err := s.shop.ListMyOrders(ctx, shopbiz.OrderQuery{
		Status:   shopbiz.OrderStatus(req.GetStatus()),
		Query:    req.GetQ(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return &v1.ListMyShopOrdersReply{Result: ErrorResult(ctx, err), Orders: []*v1.ShopOrder{}}, nil
	}
	out := &v1.ListMyShopOrdersReply{
		Result:   okResult(ctx),
		Orders:   make([]*v1.ShopOrder, 0, len(list.Orders)),
		Total:    list.Total,
		Page:     int32(list.Page),
		PageSize: int32(list.PageSize),
	}
	for _, order := range list.Orders {
		out.Orders = append(out.Orders, shopOrderToProto(order))
	}
	return out, nil
}

func (s *ShopServiceHTTPAdapter) MockPayShopOrder(ctx context.Context, req *v1.MockPayShopOrderRequest) (*v1.MockPayShopOrderReply, error) {
	result, err := s.shop.MockPayOrder(ctx, req.GetOrderId(), shopbiz.MockPayRequest{IdempotencyKey: req.GetIdempotencyKey()})
	if err != nil {
		return &v1.MockPayShopOrderReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.MockPayShopOrderReply{
		Result:  okResult(ctx),
		Order:   shopOrderToProto(result.Order),
		Payment: shopPaymentToProto(result.Payment),
		Paid:    result.Paid,
	}, nil
}

func (s *ShopServiceHTTPAdapter) ListMyUnifiedOrders(ctx context.Context, req *v1.ListMyUnifiedOrdersRequest) (*v1.ListMyUnifiedOrdersReply, error) {
	list, err := s.orders.ListMyOrders(ctx, shopbiz.OrderQuery{
		Source:   shopbiz.OrderSource(req.GetSource()),
		Status:   shopbiz.OrderStatus(req.GetStatus()),
		Query:    req.GetQ(),
		LotID:    req.GetLotId(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return &v1.ListMyUnifiedOrdersReply{Result: ErrorResult(ctx, err), Orders: []*v1.UnifiedOrder{}}, nil
	}
	out := &v1.ListMyUnifiedOrdersReply{
		Result:   okResult(ctx),
		Orders:   make([]*v1.UnifiedOrder, 0, len(list.Orders)),
		Total:    list.Total,
		Page:     int32(list.Page),
		PageSize: int32(list.PageSize),
	}
	for _, order := range list.Orders {
		out.Orders = append(out.Orders, unifiedOrderToProto(order))
	}
	return out, nil
}

func (s *ShopServiceHTTPAdapter) ListMyFrequentStores(ctx context.Context, req *v1.ListMyFrequentStoresRequest) (*v1.ListMyFrequentStoresReply, error) {
	list, err := s.orders.ListMyFrequentStores(ctx, int(req.GetLimit()))
	if err != nil {
		return &v1.ListMyFrequentStoresReply{Result: ErrorResult(ctx, err), Stores: []*v1.FrequentStore{}}, nil
	}
	out := &v1.ListMyFrequentStoresReply{
		Result: okResult(ctx),
		Stores: make([]*v1.FrequentStore, 0, len(list.Stores)),
		Total:  list.Total,
		Limit:  int32(list.Limit),
	}
	for _, store := range list.Stores {
		out.Stores = append(out.Stores, frequentStoreToProto(store))
	}
	return out, nil
}

func (s *ShopServiceHTTPAdapter) GetMyUnifiedOrder(ctx context.Context, req *v1.GetMyUnifiedOrderRequest) (*v1.GetMyUnifiedOrderReply, error) {
	order, err := s.orders.GetMyOrder(ctx, req.GetOrderId())
	if err != nil {
		return &v1.GetMyUnifiedOrderReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.GetMyUnifiedOrderReply{Result: okResult(ctx), Order: unifiedOrderToProto(*order)}, nil
}

func (s *ShopServiceHTTPAdapter) MockPayUnifiedOrder(ctx context.Context, req *v1.MockPayUnifiedOrderRequest) (*v1.MockPayUnifiedOrderReply, error) {
	result, err := s.orders.MockPayOrder(ctx, req.GetOrderId(), auctionbiz.MockPayRequest{
		IdempotencyKey: req.GetIdempotencyKey(),
		Amount:         req.GetAmount(),
		Currency:       req.GetCurrency(),
	})
	if err != nil {
		return &v1.MockPayUnifiedOrderReply{Result: ErrorResult(ctx, err)}, nil
	}
	return &v1.MockPayUnifiedOrderReply{
		Result:  okResult(ctx),
		Order:   unifiedOrderToProto(result.Order),
		Payment: unifiedPaymentToProto(result.Payment),
		Paid:    result.Paid,
	}, nil
}

func shopProductToProto(product shopbiz.Product) *v1.Product {
	out := &v1.Product{
		Id:                  product.ID,
		Title:               product.Title,
		Subtitle:            product.Subtitle,
		Description:         product.Description,
		Category:            product.Category,
		ShopName:            product.ShopName,
		MainImageUrl:        product.MainImageURL,
		DetailImageUrls:     append([]string(nil), product.DetailImageURLs...),
		Tags:                append([]string(nil), product.Tags...),
		Badges:              append([]string(nil), product.Badges...),
		PriceAmount:         product.PriceAmount,
		OriginalPriceAmount: product.OriginalPriceAmount,
		Currency:            product.Currency,
		SoldLabel:           product.SoldLabel,
		Live:                product.Live,
		Status:              product.Status,
		CreatedAtUnixMs:     product.CreatedAtUnixMs,
		UpdatedAtUnixMs:     product.UpdatedAtUnixMs,
		Skus:                make([]*v1.SKU, 0, len(product.SKUs)),
	}
	for _, sku := range product.SKUs {
		out.Skus = append(out.Skus, &v1.SKU{
			Id:          sku.ID,
			ProductId:   sku.ProductID,
			Name:        sku.Name,
			PriceAmount: sku.PriceAmount,
			Currency:    sku.Currency,
			Stock:       sku.Stock,
		})
	}
	return out
}

func deliveryAddressInputFromProto(input *v1.DeliveryAddressInput) shopbiz.DeliveryAddressInput {
	if input == nil {
		return shopbiz.DeliveryAddressInput{}
	}
	return shopbiz.DeliveryAddressInput{
		ReceiverName: input.GetReceiverName(),
		Phone:        input.GetPhone(),
		Province:     input.GetProvince(),
		City:         input.GetCity(),
		District:     input.GetDistrict(),
		Street:       input.GetStreet(),
		Detail:       input.GetDetail(),
		PostalCode:   input.GetPostalCode(),
		Tag:          input.GetTag(),
		IsDefault:    input.GetIsDefault(),
	}
}

func deliveryAddressesToProto(addresses []shopbiz.DeliveryAddress) []*v1.DeliveryAddress {
	out := make([]*v1.DeliveryAddress, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, deliveryAddressToProto(address))
	}
	return out
}

func deliveryAddressToProto(address shopbiz.DeliveryAddress) *v1.DeliveryAddress {
	return &v1.DeliveryAddress{
		Id:              address.ID,
		UserId:          address.UserID,
		ReceiverName:    address.ReceiverName,
		Phone:           address.Phone,
		Province:        address.Province,
		City:            address.City,
		District:        address.District,
		Street:          address.Street,
		Detail:          address.Detail,
		PostalCode:      address.PostalCode,
		Tag:             address.Tag,
		IsDefault:       address.IsDefault,
		Status:          string(address.Status),
		CreatedAtUnixMs: address.CreatedAtUnixMs,
		UpdatedAtUnixMs: address.UpdatedAtUnixMs,
	}
}

func deliveryAddressSnapshotToProto(snapshot *shopbiz.DeliveryAddressSnapshot) *v1.DeliveryAddressSnapshot {
	if snapshot == nil {
		return nil
	}
	return &v1.DeliveryAddressSnapshot{
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

func depositHoldPtrToProto(hold *auctionbiz.DepositHold) *v1.DepositHold {
	if hold == nil {
		return nil
	}
	return depositHoldToProto(*hold)
}

func depositHoldToProto(hold auctionbiz.DepositHold) *v1.DepositHold {
	return &v1.DepositHold{
		Id:              hold.ID,
		LotId:           hold.LotID,
		BuyerUserId:     hold.BuyerUserID,
		Status:          string(hold.Status),
		Amount:          &v1.Money{Amount: hold.Amount, Currency: hold.Currency},
		PaymentProvider: hold.PaymentProvider,
		PaymentId:       hold.PaymentID,
		IdempotencyKey:  hold.IdempotencyKey,
		AddressId:       hold.AddressID,
		AddressSnapshot: deliveryAddressSnapshotToProto(&hold.AddressSnapshot),
		CreatedAtUnixMs: hold.CreatedAtUnixMs,
		HeldAtUnixMs:    hold.HeldAtUnixMs,
	}
}

func shopOrderToProto(order shopbiz.Order) *v1.ShopOrder {
	out := &v1.ShopOrder{
		Id:                      order.ID,
		OrderNo:                 order.OrderNo,
		UserId:                  order.UserID,
		Nickname:                order.Nickname,
		Status:                  string(order.Status),
		PaymentStatus:           string(order.PaymentStatus),
		PaymentId:               order.PaymentID,
		ShopName:                order.ShopName,
		TotalAmount:             order.TotalAmount,
		Currency:                order.Currency,
		ShippingAddressId:       order.ShippingAddressID,
		ShippingAddressSnapshot: deliveryAddressSnapshotToProto(order.ShippingAddressSnapshot),
		AddressSnapshot:         order.AddressSnapshot,
		CreatedAtUnixMs:         order.CreatedAtUnixMs,
		UpdatedAtUnixMs:         order.UpdatedAtUnixMs,
		PaidAtUnixMs:            order.PaidAtUnixMs,
		Items:                   make([]*v1.ShopOrderItem, 0, len(order.Items)),
	}
	for _, item := range order.Items {
		out.Items = append(out.Items, &v1.ShopOrderItem{
			Id:          item.ID,
			OrderId:     item.OrderID,
			ProductId:   item.ProductID,
			SkuId:       item.SKUID,
			Title:       item.Title,
			ImageUrl:    item.ImageURL,
			SkuName:     item.SKUName,
			Quantity:    item.Quantity,
			UnitAmount:  item.UnitAmount,
			TotalAmount: item.TotalAmount,
			Currency:    item.Currency,
		})
	}
	return out
}

func shopPaymentToProto(payment shopbiz.Payment) *v1.ShopPayment {
	return &v1.ShopPayment{
		Id:                payment.ID,
		OrderId:           payment.OrderID,
		UserId:            payment.UserID,
		Status:            string(payment.Status),
		Amount:            payment.Amount,
		Currency:          payment.Currency,
		IdempotencyKey:    payment.IdempotencyKey,
		CreatedAtUnixMs:   payment.CreatedAtUnixMs,
		SucceededAtUnixMs: payment.SucceededAtMs,
	}
}

func unifiedOrderToProto(order shopbiz.UserOrder) *v1.UnifiedOrder {
	out := &v1.UnifiedOrder{
		Id:                        order.ID,
		Source:                    string(order.Source),
		SourceOrderId:             order.SourceOrderID,
		OrderNo:                   order.OrderNo,
		MainAccountId:             order.MainAccountID,
		UserId:                    order.UserID,
		Nickname:                  order.Nickname,
		Status:                    string(order.Status),
		PaymentStatus:             string(order.PaymentStatus),
		PaymentId:                 order.PaymentID,
		Title:                     order.Title,
		ShopName:                  order.ShopName,
		EnrichmentStatus:          string(order.EnrichmentStatus),
		EnrichmentUpdatedAtUnixMs: order.EnrichmentUpdatedAtMs,
		TotalAmount:               order.TotalAmount,
		Currency:                  order.Currency,
		ShippingAddressId:         order.ShippingAddressID,
		ShippingAddressSnapshot:   deliveryAddressSnapshotToProto(order.ShippingAddressSnapshot),
		AddressSnapshot:           order.AddressSnapshot,
		CreatedAtUnixMs:           order.CreatedAtUnixMs,
		UpdatedAtUnixMs:           order.UpdatedAtUnixMs,
		PaidAtUnixMs:              order.PaidAtUnixMs,
		ExpiresAtUnixMs:           order.ExpiresAtUnixMs,
		Items:                     make([]*v1.UnifiedOrderItem, 0, len(order.Items)),
	}
	for _, item := range order.Items {
		out.Items = append(out.Items, &v1.UnifiedOrderItem{
			Id:           item.ID,
			OrderId:      item.OrderID,
			Source:       string(item.Source),
			SourceItemId: item.SourceItemID,
			ProductId:    item.ProductID,
			SkuId:        item.SKUID,
			LotId:        item.LotID,
			RoomId:       item.RoomID,
			Title:        item.Title,
			ImageUrl:     item.ImageURL,
			SkuName:      item.SKUName,
			Quantity:     item.Quantity,
			UnitAmount:   item.UnitAmount,
			TotalAmount:  item.TotalAmount,
			Currency:     item.Currency,
		})
	}
	return out
}

func frequentStoreToProto(store shopbiz.FrequentStore) *v1.FrequentStore {
	return &v1.FrequentStore{
		StoreKey:          store.StoreKey,
		StoreName:         store.StoreName,
		Source:            string(store.Source),
		OrderCount:        store.OrderCount,
		LastOrderAtUnixMs: store.LastOrderAtUnixMs,
		ImageUrl:          store.ImageURL,
		TargetUrl:         store.TargetURL,
	}
}

func unifiedPaymentToProto(payment shopbiz.UserOrderPayment) *v1.UnifiedOrderPayment {
	return &v1.UnifiedOrderPayment{
		Id:                payment.ID,
		OrderId:           payment.OrderID,
		Source:            string(payment.Source),
		Provider:          payment.Provider,
		UserId:            payment.UserID,
		Status:            string(payment.Status),
		Amount:            payment.Amount,
		Currency:          payment.Currency,
		IdempotencyKey:    payment.IdempotencyKey,
		CreatedAtUnixMs:   payment.CreatedAtUnixMs,
		UpdatedAtUnixMs:   payment.UpdatedAtUnixMs,
		SucceededAtUnixMs: payment.SucceededAtMs,
	}
}
