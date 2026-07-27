package data

import (
	"encoding/json"
	"strconv"
	"strings"

	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/biz/shop"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

type legacyAuctionOrderPayload struct {
	OrderID         string `json:"orderId"`
	MainAccountID   string `json:"mainAccountId"`
	BuyerUserID     string `json:"buyerUserId"`
	BuyerNickname   string `json:"buyerNickname"`
	Title           string `json:"title"`
	ImageURL        string `json:"imageUrl"`
	TotalAmountFen  string `json:"totalAmountFen"`
	Currency        string `json:"currency"`
	CreatedAtUnixMs string `json:"createdAtUnixMs"`
}

const (
	userOrderSourceAuction = "auction"
	userOrderSourceShop    = "shop"
	mockPaymentProvider    = "mock"
)

func auctionOrderStatusToUser(status auction.OrderStatus) shop.OrderStatus {
	switch status {
	case auction.OrderStatusPendingPayment:
		return shop.OrderStatusPendingPayment
	case auction.OrderStatusPaid:
		return shop.OrderStatusPaid
	case auction.OrderStatusCancelled:
		return shop.OrderStatusCancelled
	case auction.OrderStatusExpired:
		return shop.OrderStatusExpired
	case auction.OrderStatusRefunded:
		return shop.OrderStatusRefunded
	default:
		return shop.OrderStatus(strings.ToLower(string(status)))
	}
}

func auctionPaymentStatusToUser(status auction.PaymentStatus) shop.PaymentStatus {
	switch status {
	case auction.PaymentStatusInit:
		return shop.PaymentStatusInit
	case auction.PaymentStatusProcessing:
		return shop.PaymentStatusProcessing
	case auction.PaymentStatusSuccess:
		return shop.PaymentStatusSuccess
	case auction.PaymentStatusFailed:
		return shop.PaymentStatusFailed
	case auction.PaymentStatusClosed:
		return shop.PaymentStatusClosed
	default:
		return shop.PaymentStatus(strings.ToLower(string(status)))
	}
}

func userOrderStatusToAuction(status string) auction.OrderStatus {
	switch shop.OrderStatus(strings.ToLower(status)) {
	case shop.OrderStatusPendingPayment:
		return auction.OrderStatusPendingPayment
	case shop.OrderStatusPaid:
		return auction.OrderStatusPaid
	case shop.OrderStatusCancelled:
		return auction.OrderStatusCancelled
	case shop.OrderStatusExpired:
		return auction.OrderStatusExpired
	case shop.OrderStatusRefunded:
		return auction.OrderStatusRefunded
	default:
		return auction.OrderStatus(strings.ToUpper(status))
	}
}

func userPaymentStatusToAuction(status string) auction.PaymentStatus {
	switch shop.PaymentStatus(strings.ToLower(status)) {
	case shop.PaymentStatusInit:
		return auction.PaymentStatusInit
	case shop.PaymentStatusProcessing:
		return auction.PaymentStatusProcessing
	case shop.PaymentStatusSuccess:
		return auction.PaymentStatusSuccess
	case shop.PaymentStatusFailed:
		return auction.PaymentStatusFailed
	case shop.PaymentStatusClosed:
		return auction.PaymentStatusClosed
	default:
		return auction.PaymentStatus(strings.ToUpper(status))
	}
}

func auctionOrderToUserModels(order auction.Order) (*UserOrderModel, *UserOrderItemModel, error) {
	payload, err := json.Marshal(order)
	if err != nil {
		return nil, nil, err
	}
	addressSnapshot := "null"
	addressText := ""
	if order.ShippingAddressSnapshot != nil {
		raw, err := json.Marshal(order.ShippingAddressSnapshot)
		if err != nil {
			return nil, nil, err
		}
		addressSnapshot = string(raw)
		addressText = order.ShippingAddressSnapshot.FullAddress
	}
	model := &UserOrderModel{
		ID:                      order.ID,
		Source:                  userOrderSourceAuction,
		SourceOrderID:           order.ID,
		OrderNo:                 order.ID,
		MainAccountID:           order.MainAccountID,
		UserID:                  order.BuyerUserID,
		Nickname:                order.BuyerNickname,
		Status:                  string(auctionOrderStatusToUser(order.Status)),
		PaymentStatus:           string(auctionPaymentStatusToUser(order.PaymentStatus)),
		PaymentID:               order.PaymentID,
		Title:                   order.LotTitle,
		ShopName:                "直播竞拍",
		TotalAmount:             order.Amount,
		Currency:                order.Currency,
		ShippingAddressID:       order.ShippingAddressID,
		ShippingAddressSnapshot: addressSnapshot,
		AddressSnapshot:         addressText,
		CreatedAtUnixMs:         order.CreatedAtUnixMs,
		UpdatedAtUnixMs:         order.UpdatedAtUnixMs,
		PaidAtUnixMs:            order.PaidAtUnixMs,
		ExpiresAtUnixMs:         order.ExpiresAtUnixMs,
		Version:                 order.Version,
		SourcePayload:           string(payload),
	}
	item := &UserOrderItemModel{
		ID:           "auction_item_" + order.ID,
		OrderID:      order.ID,
		Source:       userOrderSourceAuction,
		SourceItemID: order.LotID,
		LotID:        order.LotID,
		RoomID:       order.RoomID,
		Title:        order.LotTitle,
		ImageURL:     order.LotImageURL,
		SKUName:      "竞拍拍品",
		Quantity:     1,
		UnitAmount:   order.Amount,
		TotalAmount:  order.Amount,
		Currency:     order.Currency,
	}
	return model, item, nil
}

func userModelToAuctionOrder(model *UserOrderModel) (*auction.Order, error) {
	var order auction.Order
	if strings.TrimSpace(model.SourcePayload) != "" {
		if err := json.Unmarshal([]byte(model.SourcePayload), &order); err != nil {
			legacyOrder, matched, legacyErr := decodeLegacyAuctionOrderPayload(model.SourcePayload)
			if legacyErr != nil {
				return nil, legacyErr
			}
			if !matched {
				return nil, err
			}
			order = legacyOrder
		}
	}
	order.ID = model.ID
	order.MainAccountID = model.MainAccountID
	order.BuyerUserID = model.UserID
	order.BuyerNickname = model.Nickname
	order.Status = userOrderStatusToAuction(model.Status)
	order.PaymentStatus = userPaymentStatusToAuction(model.PaymentStatus)
	order.PaymentID = model.PaymentID
	order.EnrichmentStatus = orderenrichment.StatusPending
	order.LotTitle = model.Title
	order.Amount = model.TotalAmount
	order.Currency = model.Currency
	order.CreatedAtUnixMs = model.CreatedAtUnixMs
	order.UpdatedAtUnixMs = model.UpdatedAtUnixMs
	order.ExpiresAtUnixMs = model.ExpiresAtUnixMs
	order.PaidAtUnixMs = model.PaidAtUnixMs
	order.Version = model.Version
	return &order, nil
}

func decodeLegacyAuctionOrderPayload(raw string) (auction.Order, bool, error) {
	var payload legacyAuctionOrderPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return auction.Order{}, false, err
	}
	if strings.TrimSpace(payload.OrderID) == "" &&
		strings.TrimSpace(payload.TotalAmountFen) == "" &&
		strings.TrimSpace(payload.CreatedAtUnixMs) == "" {
		return auction.Order{}, false, nil
	}
	order := auction.Order{
		ID:            payload.OrderID,
		MainAccountID: payload.MainAccountID,
		BuyerUserID:   payload.BuyerUserID,
		BuyerNickname: payload.BuyerNickname,
		LotTitle:      payload.Title,
		LotImageURL:   payload.ImageURL,
		Currency:      payload.Currency,
	}
	if strings.TrimSpace(payload.TotalAmountFen) != "" {
		amount, err := strconv.ParseInt(payload.TotalAmountFen, 10, 64)
		if err != nil {
			return auction.Order{}, false, err
		}
		order.Amount = amount
	}
	if strings.TrimSpace(payload.CreatedAtUnixMs) != "" {
		createdAtUnixMs, err := strconv.ParseInt(payload.CreatedAtUnixMs, 10, 64)
		if err != nil {
			return auction.Order{}, false, err
		}
		order.CreatedAtUnixMs = createdAtUnixMs
		order.UpdatedAtUnixMs = createdAtUnixMs
	}
	return order, true, nil
}

func userModelToAuctionOrderWithItem(model *UserOrderModel, items []UserOrderItemModel) (*auction.Order, error) {
	order, err := userModelToAuctionOrder(model)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		order.LotID = items[0].LotID
		order.RoomID = items[0].RoomID
		order.LotTitle = items[0].Title
		order.LotImageURL = items[0].ImageURL
	}
	return order, nil
}

func auctionPaymentToUserModel(payment auction.Payment) (*UserOrderPaymentModel, error) {
	payload, err := json.Marshal(payment)
	if err != nil {
		return nil, err
	}
	return &UserOrderPaymentModel{
		ID:              payment.ID,
		OrderID:         payment.OrderID,
		Source:          userOrderSourceAuction,
		Provider:        mockPaymentProvider,
		MainAccountID:   payment.MainAccountID,
		LotID:           payment.LotID,
		UserID:          payment.BuyerUserID,
		Status:          string(auctionPaymentStatusToUser(payment.Status)),
		Amount:          payment.Amount,
		Currency:        payment.Currency,
		IdempotencyKey:  payment.IdempotencyKey,
		CreatedAtUnixMs: payment.CreatedAtUnixMs,
		UpdatedAtUnixMs: payment.UpdatedAtUnixMs,
		SucceededAtMs:   payment.SucceededAtMs,
		SourcePayload:   string(payload),
	}, nil
}

func userModelToAuctionPayment(model *UserOrderPaymentModel) (*auction.Payment, error) {
	var payment auction.Payment
	if strings.TrimSpace(model.SourcePayload) != "" {
		if err := json.Unmarshal([]byte(model.SourcePayload), &payment); err != nil {
			return nil, err
		}
	}
	payment.ID = model.ID
	payment.MainAccountID = model.MainAccountID
	payment.OrderID = model.OrderID
	payment.LotID = model.LotID
	payment.BuyerUserID = model.UserID
	payment.Status = userPaymentStatusToAuction(model.Status)
	payment.Amount = model.Amount
	payment.Currency = model.Currency
	payment.IdempotencyKey = model.IdempotencyKey
	payment.CreatedAtUnixMs = model.CreatedAtUnixMs
	payment.UpdatedAtUnixMs = model.UpdatedAtUnixMs
	payment.SucceededAtMs = model.SucceededAtMs
	return &payment, nil
}

func userOrderFromModels(model UserOrderModel, itemModels []UserOrderItemModel) shop.UserOrder {
	var shippingAddressSnapshot *shop.DeliveryAddressSnapshot
	if strings.TrimSpace(model.ShippingAddressSnapshot) != "" {
		var snapshot shop.DeliveryAddressSnapshot
		if err := json.Unmarshal([]byte(model.ShippingAddressSnapshot), &snapshot); err == nil && snapshot.AddressID != "" {
			shippingAddressSnapshot = &snapshot
		}
	}
	items := make([]shop.UserOrderItem, 0, len(itemModels))
	for _, item := range itemModels {
		items = append(items, shop.UserOrderItem{
			ID:           item.ID,
			OrderID:      item.OrderID,
			Source:       shop.OrderSource(item.Source),
			SourceItemID: item.SourceItemID,
			ProductID:    item.ProductID,
			SKUID:        item.SKUID,
			LotID:        item.LotID,
			RoomID:       item.RoomID,
			Title:        item.Title,
			ImageURL:     item.ImageURL,
			SKUName:      item.SKUName,
			Quantity:     item.Quantity,
			UnitAmount:   item.UnitAmount,
			TotalAmount:  item.TotalAmount,
			Currency:     item.Currency,
		})
	}
	order := shop.UserOrder{
		ID:                      model.ID,
		Source:                  shop.OrderSource(model.Source),
		SourceOrderID:           model.SourceOrderID,
		OrderNo:                 model.OrderNo,
		MainAccountID:           model.MainAccountID,
		UserID:                  model.UserID,
		Nickname:                model.Nickname,
		Status:                  shop.OrderStatus(model.Status),
		PaymentStatus:           shop.PaymentStatus(model.PaymentStatus),
		PaymentID:               model.PaymentID,
		Title:                   model.Title,
		ShopName:                model.ShopName,
		TotalAmount:             model.TotalAmount,
		Currency:                model.Currency,
		ShippingAddressID:       model.ShippingAddressID,
		ShippingAddressSnapshot: shippingAddressSnapshot,
		AddressSnapshot:         model.AddressSnapshot,
		CreatedAtUnixMs:         model.CreatedAtUnixMs,
		UpdatedAtUnixMs:         model.UpdatedAtUnixMs,
		PaidAtUnixMs:            model.PaidAtUnixMs,
		ExpiresAtUnixMs:         model.ExpiresAtUnixMs,
		Version:                 model.Version,
		PaymentIdempotencyKey:   model.PaymentIdempotencyKey,
		Items:                   items,
	}
	if model.Source == userOrderSourceAuction {
		order.ShopName = ""
		order.ShippingAddressID = ""
		order.ShippingAddressSnapshot = nil
		order.AddressSnapshot = ""
		order.EnrichmentStatus = orderenrichment.StatusPending
	} else {
		order.EnrichmentStatus = orderenrichment.StatusReady
	}
	return order
}
