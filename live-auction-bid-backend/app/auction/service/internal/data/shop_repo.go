package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"live-auction-bid/backend/app/auction/service/internal/biz/shop"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func (s *Store) ListProducts(ctx context.Context, query shop.ProductQuery) (shop.ProductList, error) {
	query.Page, query.PageSize = shop.NormalizePagination(query.Page, query.PageSize)
	db := s.db.WithContext(ctx).Model(&ShopProductModel{}).Where("status = ?", "active")
	if category := strings.TrimSpace(query.Category); category != "" && category != "精选" && category != "推荐" {
		db = db.Where("category = ?", category)
	}
	if keyword := strings.TrimSpace(query.Query); keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where("title LIKE ? OR subtitle LIKE ? OR category LIKE ? OR tags LIKE ?", like, like, like, like)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return shop.ProductList{}, err
	}
	var models []ShopProductModel
	if err := db.
		Order("live DESC").
		Order("updated_at_unix_ms DESC").
		Order("id ASC").
		Offset(shop.PageOffset(query.Page, query.PageSize)).
		Limit(query.PageSize).
		Find(&models).Error; err != nil {
		return shop.ProductList{}, err
	}
	products, err := s.productsFromModels(ctx, models)
	if err != nil {
		return shop.ProductList{}, err
	}
	return shop.ProductList{Products: products, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Store) FindProductByID(ctx context.Context, productID string) (*shop.Product, error) {
	var model ShopProductModel
	if err := s.db.WithContext(ctx).
		Where("id = ? AND status = ?", strings.TrimSpace(productID), "active").
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound
		}
		return nil, err
	}
	products, err := s.productsFromModels(ctx, []ShopProductModel{model})
	if err != nil {
		return nil, err
	}
	if len(products) == 0 {
		return nil, apperr.ErrNotFound
	}
	return &products[0], nil
}

func (s *Store) ListDeliveryAddresses(ctx context.Context, userID string) ([]shop.DeliveryAddress, error) {
	var models []UserDeliveryAddressModel
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", strings.TrimSpace(userID), string(shop.DeliveryAddressStatusActive)).
		Order("is_default DESC").
		Order("updated_at_unix_ms DESC").
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	addresses := make([]shop.DeliveryAddress, 0, len(models))
	for _, model := range models {
		addresses = append(addresses, deliveryAddressFromModel(model))
	}
	return addresses, nil
}

func (s *Store) FindDeliveryAddress(ctx context.Context, userID, addressID string) (*shop.DeliveryAddress, error) {
	address, err := s.findDeliveryAddress(ctx, s.db.WithContext(ctx), userID, addressID, false)
	if err != nil {
		return nil, err
	}
	return address, nil
}

func (s *Store) CreateDeliveryAddress(ctx context.Context, userID string, input shop.DeliveryAddressInput) (*shop.DeliveryAddress, error) {
	input = shop.NormalizeDeliveryAddressInput(input)
	if err := shop.ValidateDeliveryAddressInput(input); err != nil {
		return nil, err
	}
	nowMs := time.Now().UnixMilli()
	var created shop.DeliveryAddress
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&UserDeliveryAddressModel{}).
			Where("user_id = ? AND status = ?", userID, string(shop.DeliveryAddressStatusActive)).
			Count(&count).Error; err != nil {
			return err
		}
		isDefault := input.IsDefault || count == 0
		if isDefault {
			if err := tx.Model(&UserDeliveryAddressModel{}).
				Where("user_id = ? AND status = ?", userID, string(shop.DeliveryAddressStatusActive)).
				Updates(map[string]any{"is_default": false, "updated_at_unix_ms": nowMs}).Error; err != nil {
				return err
			}
		}
		model := UserDeliveryAddressModel{
			ID:              "addr_" + randomHex(10),
			UserID:          userID,
			ReceiverName:    input.ReceiverName,
			Phone:           input.Phone,
			Province:        input.Province,
			City:            input.City,
			District:        input.District,
			Street:          input.Street,
			Detail:          input.Detail,
			PostalCode:      input.PostalCode,
			Tag:             input.Tag,
			IsDefault:       isDefault,
			Status:          string(shop.DeliveryAddressStatusActive),
			CreatedAtUnixMs: nowMs,
			UpdatedAtUnixMs: nowMs,
		}
		if err := tx.Create(&model).Error; err != nil {
			return err
		}
		created = deliveryAddressFromModel(model)
		return nil
	}); err != nil {
		return nil, err
	}
	return &created, nil
}

func (s *Store) UpdateDeliveryAddress(ctx context.Context, userID, addressID string, input shop.DeliveryAddressInput) (*shop.DeliveryAddress, error) {
	input = shop.NormalizeDeliveryAddressInput(input)
	if err := shop.ValidateDeliveryAddressInput(input); err != nil {
		return nil, err
	}
	nowMs := time.Now().UnixMilli()
	var updated shop.DeliveryAddress
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := s.findDeliveryAddress(ctx, tx, userID, addressID, true)
		if err != nil {
			return err
		}
		isDefault := input.IsDefault || current.IsDefault
		if isDefault {
			if err := tx.Model(&UserDeliveryAddressModel{}).
				Where("user_id = ? AND status = ? AND id <> ?", userID, string(shop.DeliveryAddressStatusActive), addressID).
				Updates(map[string]any{"is_default": false, "updated_at_unix_ms": nowMs}).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{
			"receiver_name":      input.ReceiverName,
			"phone":              input.Phone,
			"province":           input.Province,
			"city":               input.City,
			"district":           input.District,
			"street":             input.Street,
			"detail":             input.Detail,
			"postal_code":        input.PostalCode,
			"tag":                input.Tag,
			"is_default":         isDefault,
			"updated_at_unix_ms": nowMs,
		}
		if err := tx.Model(&UserDeliveryAddressModel{}).
			Where("id = ? AND user_id = ? AND status = ?", addressID, userID, string(shop.DeliveryAddressStatusActive)).
			Updates(updates).Error; err != nil {
			return err
		}
		next, err := s.findDeliveryAddress(ctx, tx, userID, addressID, false)
		if err != nil {
			return err
		}
		updated = *next
		return nil
	}); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *Store) DeleteDeliveryAddress(ctx context.Context, userID, addressID string) error {
	nowMs := time.Now().UnixMilli()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := s.findDeliveryAddress(ctx, tx, userID, addressID, true)
		if err != nil {
			return err
		}
		result := tx.Model(&UserDeliveryAddressModel{}).
			Where("id = ? AND user_id = ? AND status = ?", addressID, userID, string(shop.DeliveryAddressStatusActive)).
			Updates(map[string]any{
				"status":             string(shop.DeliveryAddressStatusDeleted),
				"is_default":         false,
				"deleted_at_unix_ms": nowMs,
				"updated_at_unix_ms": nowMs,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.ErrAddressNotFound
		}
		if current.IsDefault {
			var next UserDeliveryAddressModel
			if err := tx.Where("user_id = ? AND status = ?", userID, string(shop.DeliveryAddressStatusActive)).
				Order("updated_at_unix_ms DESC").
				First(&next).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			return tx.Model(&UserDeliveryAddressModel{}).
				Where("id = ?", next.ID).
				Updates(map[string]any{"is_default": true, "updated_at_unix_ms": nowMs}).Error
		}
		return nil
	})
}

func (s *Store) SetDefaultDeliveryAddress(ctx context.Context, userID, addressID string) ([]shop.DeliveryAddress, error) {
	nowMs := time.Now().UnixMilli()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.findDeliveryAddress(ctx, tx, userID, addressID, true); err != nil {
			return err
		}
		if err := tx.Model(&UserDeliveryAddressModel{}).
			Where("user_id = ? AND status = ?", userID, string(shop.DeliveryAddressStatusActive)).
			Updates(map[string]any{"is_default": false, "updated_at_unix_ms": nowMs}).Error; err != nil {
			return err
		}
		return tx.Model(&UserDeliveryAddressModel{}).
			Where("id = ? AND user_id = ? AND status = ?", addressID, userID, string(shop.DeliveryAddressStatusActive)).
			Updates(map[string]any{"is_default": true, "updated_at_unix_ms": nowMs}).Error
	}); err != nil {
		return nil, err
	}
	return s.ListDeliveryAddresses(ctx, userID)
}

func (s *Store) CreateOrder(ctx context.Context, user shop.UserRef, req shop.CreateOrderRequest) (*shop.Order, error) {
	if err := shop.ValidateCreateOrderRequest(req); err != nil {
		return nil, err
	}
	nowMs := time.Now().UnixMilli()
	var created shop.Order
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var skuModel ShopSKUModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", strings.TrimSpace(req.SKUID)).
			First(&skuModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return err
		}
		if skuModel.Stock < req.Quantity {
			return fmt.Errorf("%w: stock is not enough", apperr.ErrInvalidArgument)
		}
		var productModel ShopProductModel
		if err := tx.Where("id = ? AND status = ?", skuModel.ProductID, "active").First(&productModel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return err
		}
		address, err := s.findDeliveryAddress(ctx, tx, user.ID, strings.TrimSpace(req.AddressID), true)
		if err != nil {
			return err
		}
		addressSnapshot := address.Snapshot()
		addressSnapshotJSON, err := json.Marshal(addressSnapshot)
		if err != nil {
			return err
		}

		orderID := "shop_order_" + randomHex(10)
		total := skuModel.PriceAmount * req.Quantity
		order := UserOrderModel{
			ID:                      orderID,
			Source:                  userOrderSourceShop,
			SourceOrderID:           orderID,
			OrderNo:                 fmt.Sprintf("SO%d%s", nowMs, strings.ToUpper(randomHex(3))),
			UserID:                  user.ID,
			Nickname:                user.Nickname,
			Status:                  string(shop.OrderStatusPendingPayment),
			PaymentStatus:           string(shop.PaymentStatusInit),
			Title:                   productModel.Title,
			ShopName:                productModel.ShopName,
			TotalAmount:             total,
			Currency:                skuModel.Currency,
			ShippingAddressID:       address.ID,
			ShippingAddressSnapshot: string(addressSnapshotJSON),
			AddressSnapshot:         addressSnapshot.FullAddress,
			CreatedAtUnixMs:         nowMs,
			UpdatedAtUnixMs:         nowMs,
			Version:                 1,
			SourcePayload:           "{}",
		}
		item := UserOrderItemModel{
			ID:           "shop_item_" + randomHex(10),
			OrderID:      orderID,
			Source:       userOrderSourceShop,
			SourceItemID: skuModel.ID,
			ProductID:    productModel.ID,
			SKUID:        skuModel.ID,
			Title:        productModel.Title,
			ImageURL:     productModel.MainImageURL,
			SKUName:      skuModel.Name,
			Quantity:     req.Quantity,
			UnitAmount:   skuModel.PriceAmount,
			TotalAmount:  total,
			Currency:     skuModel.Currency,
		}

		if err := tx.Model(&ShopSKUModel{}).Where("id = ?", skuModel.ID).Update("stock", skuModel.Stock-req.Quantity).Error; err != nil {
			return err
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		created = shopOrderFromUserModels(order, []UserOrderItemModel{item})
		return nil
	}); err != nil {
		return nil, err
	}
	return &created, nil
}

func (s *Store) ListShopOrders(ctx context.Context, query shop.OrderQuery) (shop.OrderList, error) {
	query.Page, query.PageSize = shop.NormalizePagination(query.Page, query.PageSize)
	db := s.db.WithContext(ctx).Model(&UserOrderModel{}).
		Where("source = ? AND user_id = ?", userOrderSourceShop, query.UserID)
	if query.Status != "" {
		db = db.Where("status = ?", string(query.Status))
	}
	if query.LotID != "" {
		db = db.Where(
			"EXISTS (SELECT 1 FROM user_order_items WHERE user_order_items.order_id = user_orders.id AND user_order_items.lot_id = ?)",
			query.LotID,
		)
	}
	if keyword := strings.TrimSpace(query.Query); keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where(
			"order_no LIKE ? OR title LIKE ? OR shop_name LIKE ? OR EXISTS (SELECT 1 FROM user_order_items WHERE user_order_items.order_id = user_orders.id AND user_order_items.title LIKE ?)",
			like,
			like,
			like,
			like,
		)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return shop.OrderList{}, err
	}
	var models []UserOrderModel
	if err := db.
		Order("created_at_unix_ms DESC").
		Order("id ASC").
		Offset(shop.PageOffset(query.Page, query.PageSize)).
		Limit(query.PageSize).
		Find(&models).Error; err != nil {
		return shop.OrderList{}, err
	}
	orders, err := s.shopOrdersFromUserModels(ctx, models)
	if err != nil {
		return shop.OrderList{}, err
	}
	return shop.OrderList{Orders: orders, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Store) MockPayOrder(ctx context.Context, userID, orderID string, req shop.MockPayRequest) (*shop.MockPayResult, error) {
	nowMs := time.Now().UnixMilli()
	var result shop.MockPayResult
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order UserOrderModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND source = ?", orderID, userID, userOrderSourceShop).
			First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return err
		}
		var items []UserOrderItemModel
		if err := tx.Where("order_id = ?", order.ID).Order("id ASC").Find(&items).Error; err != nil {
			return err
		}

		if order.PaymentStatus == string(shop.PaymentStatusSuccess) {
			payment := UserOrderPaymentModel{
				ID:              order.PaymentID,
				OrderID:         order.ID,
				Source:          userOrderSourceShop,
				Provider:        mockPaymentProvider,
				UserID:          order.UserID,
				Status:          string(shop.PaymentStatusSuccess),
				Amount:          order.TotalAmount,
				Currency:        order.Currency,
				IdempotencyKey:  order.PaymentIdempotencyKey,
				CreatedAtUnixMs: order.PaidAtUnixMs,
				UpdatedAtUnixMs: order.PaidAtUnixMs,
				SucceededAtMs:   order.PaidAtUnixMs,
				SourcePayload:   "{}",
			}
			result = shop.MockPayResult{Order: shopOrderFromUserModels(order, items), Payment: shopPaymentFromUserModel(payment), Paid: true}
			return nil
		}
		if order.Status != string(shop.OrderStatusPendingPayment) {
			return fmt.Errorf("%w: only pending payment order can be paid", apperr.ErrInvalidArgument)
		}
		var existing UserOrderPaymentModel
		if err := tx.Where("source = ? AND order_id = ? AND idempotency_key = ?", userOrderSourceShop, order.ID, req.IdempotencyKey).
			First(&existing).Error; err == nil {
			result = shop.MockPayResult{Order: shopOrderFromUserModels(order, items), Payment: shopPaymentFromUserModel(existing), Paid: existing.Status == string(shop.PaymentStatusSuccess)}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		payment := UserOrderPaymentModel{
			ID:              "shop_pay_" + randomHex(10),
			OrderID:         order.ID,
			Source:          userOrderSourceShop,
			Provider:        mockPaymentProvider,
			UserID:          order.UserID,
			Status:          string(shop.PaymentStatusSuccess),
			Amount:          order.TotalAmount,
			Currency:        order.Currency,
			IdempotencyKey:  req.IdempotencyKey,
			CreatedAtUnixMs: nowMs,
			SucceededAtMs:   nowMs,
			SourcePayload:   "{}",
		}
		if err := tx.Create(&payment).Error; err != nil {
			return err
		}
		order.Status = string(shop.OrderStatusPaid)
		order.PaymentStatus = string(shop.PaymentStatusSuccess)
		order.PaymentID = payment.ID
		order.PaymentIdempotencyKey = req.IdempotencyKey
		order.PaidAtUnixMs = nowMs
		order.UpdatedAtUnixMs = nowMs
		if err := tx.Model(&UserOrderModel{}).
			Where("id = ? AND source = ?", order.ID, userOrderSourceShop).
			Select("status", "payment_status", "payment_id", "payment_idempotency_key", "paid_at_unix_ms", "updated_at_unix_ms").
			Updates(order).Error; err != nil {
			return err
		}
		result = shop.MockPayResult{Order: shopOrderFromUserModels(order, items), Payment: shopPaymentFromUserModel(payment), Paid: true}
		return nil
	}); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) FindUserOrder(ctx context.Context, userID, orderID string) (*shop.UserOrder, error) {
	userID = strings.TrimSpace(userID)
	orderID = strings.TrimSpace(orderID)
	if userID == "" {
		return nil, apperr.ErrUnauthenticated
	}
	if orderID == "" {
		return nil, fmt.Errorf("%w: order id is required", apperr.ErrInvalidArgument)
	}
	if err := s.closeExpiredUserOrder(ctx, userID, orderID, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	var model UserOrderModel
	if err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", orderID, userID).
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound
		}
		return nil, err
	}
	orders, err := s.userOrdersFromModels(ctx, []UserOrderModel{model})
	if err != nil {
		return nil, err
	}
	if len(orders) != 1 {
		return nil, errors.New("user order composition returned an unexpected result count")
	}
	return &orders[0], nil
}

func (s *Store) ListUserOrders(ctx context.Context, query shop.OrderQuery) (shop.UserOrderList, error) {
	query.Page, query.PageSize = shop.NormalizePagination(query.Page, query.PageSize)
	userID := strings.TrimSpace(query.UserID)
	if userID == "" {
		return shop.UserOrderList{}, apperr.ErrUnauthenticated
	}
	if err := s.closeExpiredUserOrders(ctx, userID, time.Now().UnixMilli()); err != nil {
		return shop.UserOrderList{}, err
	}
	db := s.db.WithContext(ctx).Model(&UserOrderModel{}).Where("user_id = ?", userID)
	if query.Source != "" {
		db = db.Where("source = ?", string(query.Source))
	}
	if query.Status != "" {
		db = db.Where("status = ?", string(query.Status))
	}
	if keyword := strings.TrimSpace(query.Query); keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where(
			"order_no LIKE ? OR title LIKE ? OR shop_name LIKE ? OR EXISTS (SELECT 1 FROM user_order_items WHERE user_order_items.order_id = user_orders.id AND user_order_items.title LIKE ?) OR EXISTS (SELECT 1 FROM auction_order_enrichments WHERE auction_order_enrichments.order_id = user_orders.id AND JSON_UNQUOTE(JSON_EXTRACT(auction_order_enrichments.shop_snapshot, '$.shopName')) LIKE ?)",
			like,
			like,
			like,
			like,
			like,
		)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return shop.UserOrderList{}, err
	}
	var models []UserOrderModel
	if err := db.
		Order("created_at_unix_ms DESC").
		Order("id ASC").
		Offset(shop.PageOffset(query.Page, query.PageSize)).
		Limit(query.PageSize).
		Find(&models).Error; err != nil {
		return shop.UserOrderList{}, err
	}
	orders, err := s.userOrdersFromModels(ctx, models)
	if err != nil {
		return shop.UserOrderList{}, err
	}
	return shop.UserOrderList{Orders: orders, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Store) closeExpiredUserOrder(ctx context.Context, userID, orderID string, nowMs int64) error {
	return s.closeExpiredUserOrdersWithScope(ctx, userID, nowMs, "id = ?", orderID)
}

func (s *Store) closeExpiredUserOrders(ctx context.Context, userID string, nowMs int64) error {
	return s.closeExpiredUserOrdersWithScope(ctx, userID, nowMs, "")
}

func (s *Store) closeExpiredUserOrdersWithScope(ctx context.Context, userID string, nowMs int64, extraWhere string, extraArgs ...any) error {
	if strings.TrimSpace(userID) == "" || nowMs <= 0 {
		return nil
	}
	db := s.db.WithContext(ctx).
		Model(&UserOrderModel{}).
		Where("user_id = ?", strings.TrimSpace(userID)).
		Where("status = ? AND payment_status <> ?", string(shop.OrderStatusPendingPayment), string(shop.PaymentStatusSuccess)).
		Where("expires_at_unix_ms > 0 AND expires_at_unix_ms <= ?", nowMs)
	if strings.TrimSpace(extraWhere) != "" {
		db = db.Where(extraWhere, extraArgs...)
	}
	return db.Updates(map[string]any{
		"status":             string(shop.OrderStatusExpired),
		"payment_status":     string(shop.PaymentStatusClosed),
		"updated_at_unix_ms": nowMs,
	}).Error
}

func (s *Store) ListFrequentStores(ctx context.Context, query shop.FrequentStoreQuery) (shop.FrequentStoreList, error) {
	query.Limit = shop.NormalizeFrequentStoreLimit(query.Limit)
	userID := strings.TrimSpace(query.UserID)
	if userID == "" {
		return shop.FrequentStoreList{}, apperr.ErrUnauthenticated
	}
	paidStatuses := []string{
		string(shop.OrderStatusPaid),
		string(shop.OrderStatusShipped),
		string(shop.OrderStatusCompleted),
	}
	type frequentStoreAggregate struct {
		Source            string
		MainAccountID     string
		ShopName          string
		OrderCount        int64
		LastOrderAtUnixMs int64
	}
	var aggregates []frequentStoreAggregate
	if err := s.db.WithContext(ctx).
		Model(&UserOrderModel{}).
		Select("source, main_account_id, shop_name, COUNT(*) AS order_count, MAX(updated_at_unix_ms) AS last_order_at_unix_ms").
		Where("user_id = ? AND status IN ?", userID, paidStatuses).
		Group("source, main_account_id, shop_name").
		Order("order_count DESC").
		Order("last_order_at_unix_ms DESC").
		Limit(query.Limit).
		Scan(&aggregates).Error; err != nil {
		return shop.FrequentStoreList{}, err
	}

	stores := make([]shop.FrequentStore, 0, len(aggregates))
	for _, aggregate := range aggregates {
		storeName := frequentStoreName(aggregate.Source, aggregate.ShopName)
		item, err := s.latestFrequentStoreItem(ctx, userID, aggregate.Source, aggregate.MainAccountID, aggregate.ShopName, paidStatuses)
		if err != nil {
			return shop.FrequentStoreList{}, err
		}
		stores = append(stores, shop.FrequentStore{
			StoreKey:          frequentStoreKey(aggregate.Source, aggregate.MainAccountID, storeName),
			StoreName:         storeName,
			Source:            shop.OrderSource(aggregate.Source),
			OrderCount:        aggregate.OrderCount,
			LastOrderAtUnixMs: aggregate.LastOrderAtUnixMs,
			ImageURL:          item.ImageURL,
			TargetURL:         frequentStoreTargetURL(item.ProductID),
		})
	}
	return shop.FrequentStoreList{Stores: stores, Total: int64(len(stores)), Limit: query.Limit}, nil
}

func (s *Store) latestFrequentStoreItem(ctx context.Context, userID, source, mainAccountID, shopName string, statuses []string) (UserOrderItemModel, error) {
	var item UserOrderItemModel
	db := s.db.WithContext(ctx).
		Model(&UserOrderItemModel{}).
		Select("user_order_items.*").
		Joins("JOIN user_orders ON user_orders.id = user_order_items.order_id").
		Where("user_orders.user_id = ? AND user_orders.source = ? AND user_orders.shop_name = ? AND user_orders.status IN ?", userID, source, shopName, statuses)
	if strings.TrimSpace(mainAccountID) != "" {
		db = db.Where("user_orders.main_account_id = ?", strings.TrimSpace(mainAccountID))
	}
	err := db.
		Order("user_orders.updated_at_unix_ms DESC").
		Order("user_order_items.id ASC").
		Limit(1).
		Take(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return UserOrderItemModel{}, nil
	}
	return item, err
}

func frequentStoreKey(source, mainAccountID, storeName string) string {
	if id := strings.TrimSpace(mainAccountID); id != "" {
		return strings.TrimSpace(source) + ":" + id
	}
	return strings.TrimSpace(source) + ":" + strings.TrimSpace(storeName)
}

func frequentStoreName(source, shopName string) string {
	name := strings.TrimSpace(shopName)
	if name != "" {
		return name
	}
	if source == string(shop.OrderSourceAuction) {
		return "直播竞拍"
	}
	return "商城订单"
}

func frequentStoreTargetURL(productID string) string {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return "/shop"
	}
	return "/shop/detail?id=" + url.QueryEscape(productID)
}

func (s *Store) productsFromModels(ctx context.Context, models []ShopProductModel) ([]shop.Product, error) {
	if len(models) == 0 {
		return []shop.Product{}, nil
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	var skuModels []ShopSKUModel
	if err := s.db.WithContext(ctx).Where("product_id IN ?", ids).Order("id ASC").Find(&skuModels).Error; err != nil {
		return nil, err
	}
	skusByProduct := map[string][]shop.SKU{}
	for _, model := range skuModels {
		skusByProduct[model.ProductID] = append(skusByProduct[model.ProductID], shopSKUFromModel(model))
	}
	products := make([]shop.Product, 0, len(models))
	for _, model := range models {
		product, err := shopProductFromModel(model)
		if err != nil {
			return nil, err
		}
		product.SKUs = skusByProduct[product.ID]
		products = append(products, product)
	}
	return products, nil
}

func (s *Store) userOrdersFromModels(ctx context.Context, models []UserOrderModel) ([]shop.UserOrder, error) {
	if len(models) == 0 {
		return []shop.UserOrder{}, nil
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	itemsByOrder, err := s.userOrderItemsByOrderID(ctx, ids)
	if err != nil {
		return nil, err
	}
	enrichments, err := s.orderEnrichmentsByOrderID(ctx, models)
	if err != nil {
		return nil, err
	}
	orders := make([]shop.UserOrder, 0, len(models))
	for _, model := range models {
		order := userOrderFromModels(model, itemsByOrder[model.ID])
		enrichment, found := enrichments[model.ID]
		if err := applyUserOrderEnrichment(&order, enrichment, found); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *Store) shopOrdersFromUserModels(ctx context.Context, models []UserOrderModel) ([]shop.Order, error) {
	userOrders, err := s.userOrdersFromModels(ctx, models)
	if err != nil {
		return nil, err
	}
	orders := make([]shop.Order, 0, len(userOrders))
	for _, order := range userOrders {
		orders = append(orders, shopOrderFromUserOrder(order))
	}
	return orders, nil
}

func shopOrderFromUserModels(model UserOrderModel, itemModels []UserOrderItemModel) shop.Order {
	return shopOrderFromUserOrder(userOrderFromModels(model, itemModels))
}

func shopOrderFromUserOrder(order shop.UserOrder) shop.Order {
	items := make([]shop.OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, shop.OrderItem{
			ID:          item.ID,
			OrderID:     item.OrderID,
			ProductID:   item.ProductID,
			SKUID:       item.SKUID,
			Title:       item.Title,
			ImageURL:    item.ImageURL,
			SKUName:     item.SKUName,
			Quantity:    item.Quantity,
			UnitAmount:  item.UnitAmount,
			TotalAmount: item.TotalAmount,
			Currency:    item.Currency,
		})
	}
	return shop.Order{
		ID:                      order.ID,
		OrderNo:                 order.OrderNo,
		UserID:                  order.UserID,
		Nickname:                order.Nickname,
		Status:                  order.Status,
		PaymentStatus:           order.PaymentStatus,
		PaymentID:               order.PaymentID,
		ShopName:                order.ShopName,
		TotalAmount:             order.TotalAmount,
		Currency:                order.Currency,
		ShippingAddressID:       order.ShippingAddressID,
		ShippingAddressSnapshot: order.ShippingAddressSnapshot,
		AddressSnapshot:         order.AddressSnapshot,
		CreatedAtUnixMs:         order.CreatedAtUnixMs,
		UpdatedAtUnixMs:         order.UpdatedAtUnixMs,
		PaidAtUnixMs:            order.PaidAtUnixMs,
		PaymentIdempotencyKey:   order.PaymentIdempotencyKey,
		Items:                   items,
	}
}

func shopPaymentFromUserModel(model UserOrderPaymentModel) shop.Payment {
	return shop.Payment{
		ID:              model.ID,
		OrderID:         model.OrderID,
		UserID:          model.UserID,
		Status:          shop.PaymentStatus(model.Status),
		Amount:          model.Amount,
		Currency:        model.Currency,
		IdempotencyKey:  model.IdempotencyKey,
		CreatedAtUnixMs: model.CreatedAtUnixMs,
		SucceededAtMs:   model.SucceededAtMs,
	}
}

func shopProductFromModel(model ShopProductModel) (shop.Product, error) {
	var detailImageURLs []string
	var tags []string
	var badges []string
	if err := json.Unmarshal([]byte(model.DetailImageURLs), &detailImageURLs); err != nil {
		return shop.Product{}, err
	}
	if err := json.Unmarshal([]byte(model.Tags), &tags); err != nil {
		return shop.Product{}, err
	}
	if err := json.Unmarshal([]byte(model.Badges), &badges); err != nil {
		return shop.Product{}, err
	}
	return shop.Product{
		ID:                  model.ID,
		Title:               model.Title,
		Subtitle:            model.Subtitle,
		Description:         model.Description,
		Category:            model.Category,
		ShopName:            model.ShopName,
		MainImageURL:        model.MainImageURL,
		DetailImageURLs:     detailImageURLs,
		Tags:                tags,
		Badges:              badges,
		PriceAmount:         model.PriceAmount,
		OriginalPriceAmount: model.OriginalPriceAmount,
		Currency:            model.Currency,
		SoldLabel:           model.SoldLabel,
		Live:                model.Live,
		Status:              model.Status,
		CreatedAtUnixMs:     model.CreatedAtUnixMs,
		UpdatedAtUnixMs:     model.UpdatedAtUnixMs,
	}, nil
}

func shopSKUFromModel(model ShopSKUModel) shop.SKU {
	return shop.SKU{
		ID:          model.ID,
		ProductID:   model.ProductID,
		Name:        model.Name,
		PriceAmount: model.PriceAmount,
		Currency:    model.Currency,
		Stock:       model.Stock,
	}
}

func (s *Store) findDeliveryAddress(ctx context.Context, db *gorm.DB, userID, addressID string, lock bool) (*shop.DeliveryAddress, error) {
	userID = strings.TrimSpace(userID)
	addressID = strings.TrimSpace(addressID)
	if userID == "" {
		return nil, apperr.ErrUnauthenticated
	}
	if addressID == "" {
		return nil, apperr.ErrAddressRequired
	}
	query := db.WithContext(ctx).Where("id = ? AND user_id = ? AND status = ?", addressID, userID, string(shop.DeliveryAddressStatusActive))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var model UserDeliveryAddressModel
	if err := query.First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrAddressNotFound
		}
		return nil, err
	}
	address := deliveryAddressFromModel(model)
	return &address, nil
}

func deliveryAddressFromModel(model UserDeliveryAddressModel) shop.DeliveryAddress {
	return shop.DeliveryAddress{
		ID:              model.ID,
		UserID:          model.UserID,
		ReceiverName:    model.ReceiverName,
		Phone:           model.Phone,
		Province:        model.Province,
		City:            model.City,
		District:        model.District,
		Street:          model.Street,
		Detail:          model.Detail,
		PostalCode:      model.PostalCode,
		Tag:             model.Tag,
		IsDefault:       model.IsDefault,
		Status:          shop.DeliveryAddressStatus(model.Status),
		CreatedAtUnixMs: model.CreatedAtUnixMs,
		UpdatedAtUnixMs: model.UpdatedAtUnixMs,
		DeletedAtUnixMs: model.DeletedAtUnixMs,
	}
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func jsonText(value []string) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(payload)
}
