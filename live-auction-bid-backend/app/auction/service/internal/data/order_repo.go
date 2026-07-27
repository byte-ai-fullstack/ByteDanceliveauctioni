package data

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func (s *Store) FindOrderByID(ctx context.Context, orderID string) (*auction.Order, error) {
	if orderID == "" {
		return nil, errors.New("order id is required")
	}
	var model UserOrderModel
	if err := s.db.WithContext(ctx).
		Where("id = ? AND source = ?", orderID, userOrderSourceAuction).
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound
		}
		return nil, err
	}
	orders, err := s.auctionOrdersFromUserModels(ctx, []UserOrderModel{model})
	if err != nil {
		return nil, err
	}
	if len(orders) != 1 {
		return nil, errors.New("auction order composition returned an unexpected result count")
	}
	return &orders[0], nil
}

func (s *Store) FindOrderByLot(ctx context.Context, lotID string) (*auction.Order, bool, error) {
	if lotID == "" {
		return nil, false, errors.New("lot id is required")
	}
	var item UserOrderItemModel
	if err := s.db.WithContext(ctx).
		Where("source = ? AND lot_id = ?", userOrderSourceAuction, lotID).
		First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	order, err := s.FindOrderByID(ctx, item.OrderID)
	return order, err == nil, err
}

func (s *Store) ListOrdersByBuyer(ctx context.Context, buyerUserID string) ([]auction.Order, error) {
	if buyerUserID == "" {
		return nil, errors.New("buyer user id is required")
	}
	var models []UserOrderModel
	if err := s.db.WithContext(ctx).
		Where("source = ? AND user_id = ?", userOrderSourceAuction, buyerUserID).
		Order("created_at_unix_ms DESC").
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	return s.auctionOrdersFromUserModels(ctx, models)
}

func (s *Store) ListOrders(ctx context.Context, query auction.OrderQuery) (auction.OrderList, error) {
	query.Page, query.PageSize = auction.NormalizePagination(query.Page, query.PageSize)
	db := s.db.WithContext(ctx).Model(&UserOrderModel{}).Where("source = ?", userOrderSourceAuction)
	if query.MainAccountID != "" {
		db = db.Where("main_account_id = ?", query.MainAccountID)
	}
	if query.BuyerUserID != "" {
		db = db.Where("user_id = ?", query.BuyerUserID)
	}
	if query.Status != "" {
		db = db.Where("status = ?", string(auctionOrderStatusToUser(query.Status)))
	}
	if query.PaymentStatus != "" {
		db = db.Where("payment_status = ?", string(auctionPaymentStatusToUser(query.PaymentStatus)))
	}
	if query.LotID != "" {
		db = db.Where(
			"EXISTS (SELECT 1 FROM user_order_items WHERE user_order_items.order_id = user_orders.id AND user_order_items.source = ? AND user_order_items.lot_id = ?)",
			userOrderSourceAuction,
			query.LotID,
		)
	}
	if buyer := strings.TrimSpace(query.Buyer); buyer != "" {
		like := "%" + buyer + "%"
		db = db.Where("user_id LIKE ? OR nickname LIKE ?", like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return auction.OrderList{}, err
	}
	var models []UserOrderModel
	if err := db.
		Order("created_at_unix_ms DESC").
		Order("id ASC").
		Offset(auction.PageOffset(query.Page, query.PageSize)).
		Limit(query.PageSize).
		Find(&models).Error; err != nil {
		return auction.OrderList{}, err
	}
	orders, err := s.auctionOrdersFromUserModels(ctx, models)
	if err != nil {
		return auction.OrderList{}, err
	}
	summaries := make([]auction.OrderSummary, 0, len(orders))
	for _, order := range orders {
		summaries = append(summaries, order.Summary())
	}
	return auction.OrderList{Orders: summaries, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Store) FindPaymentByIdempotencyKey(ctx context.Context, orderID, key string) (*auction.Payment, bool, error) {
	if orderID == "" {
		return nil, false, errors.New("order id is required")
	}
	if key == "" {
		return nil, false, errors.New("payment idempotency key is required")
	}
	var model UserOrderPaymentModel
	if err := s.db.WithContext(ctx).
		Where("source = ? AND order_id = ? AND idempotency_key = ?", userOrderSourceAuction, orderID, key).
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	payment, err := userModelToAuctionPayment(&model)
	return payment, err == nil, err
}

func (s *Store) CommitPaymentSuccess(ctx context.Context, payment auction.Payment, order auction.Order, expectedOrderVersion int64, events []*v1.AuctionEvent) error {
	if expectedOrderVersion <= 0 {
		return errors.New("order expected version is required")
	}
	paymentModel, err := auctionPaymentToUserModel(payment)
	if err != nil {
		return err
	}
	orderModel, _, err := auctionOrderToUserModels(order)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(paymentModel).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"status":                  orderModel.Status,
			"payment_status":          orderModel.PaymentStatus,
			"payment_id":              orderModel.PaymentID,
			"payment_idempotency_key": payment.IdempotencyKey,
			"paid_at_unix_ms":         orderModel.PaidAtUnixMs,
			"updated_at_unix_ms":      orderModel.UpdatedAtUnixMs,
			"version":                 orderModel.Version,
			"source_payload":          orderModel.SourcePayload,
		}
		result := tx.Model(&UserOrderModel{}).
			Where("id = ? AND source = ? AND version = ?", order.ID, userOrderSourceAuction, expectedOrderVersion).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.ErrLotVersionConflict
		}
		return createEventModels(ctx, tx, events)
	})
}

func (s *Store) auctionOrdersFromUserModels(ctx context.Context, models []UserOrderModel) ([]auction.Order, error) {
	if len(models) == 0 {
		return []auction.Order{}, nil
	}
	orderIDs := make([]string, 0, len(models))
	for _, model := range models {
		orderIDs = append(orderIDs, model.ID)
	}
	itemsByOrder, err := s.userOrderItemsByOrderID(ctx, orderIDs)
	if err != nil {
		return nil, err
	}
	enrichments, err := s.orderEnrichmentsByOrderID(ctx, models)
	if err != nil {
		return nil, err
	}
	orders := make([]auction.Order, 0, len(models))
	for i := range models {
		order, err := userModelToAuctionOrderWithItem(&models[i], itemsByOrder[models[i].ID])
		if err != nil {
			return nil, err
		}
		enrichment, found := enrichments[models[i].ID]
		if err := applyAuctionOrderEnrichment(order, enrichment, found); err != nil {
			return nil, err
		}
		orders = append(orders, *order)
	}
	return orders, nil
}

func (s *Store) userOrderItemsByOrderID(ctx context.Context, orderIDs []string) (map[string][]UserOrderItemModel, error) {
	itemsByOrder := make(map[string][]UserOrderItemModel, len(orderIDs))
	if len(orderIDs) == 0 {
		return itemsByOrder, nil
	}
	var itemModels []UserOrderItemModel
	if err := s.db.WithContext(ctx).
		Where("order_id IN ?", orderIDs).
		Order("id ASC").
		Find(&itemModels).Error; err != nil {
		return nil, err
	}
	for _, item := range itemModels {
		itemsByOrder[item.OrderID] = append(itemsByOrder[item.OrderID], item)
	}
	return itemsByOrder, nil
}
