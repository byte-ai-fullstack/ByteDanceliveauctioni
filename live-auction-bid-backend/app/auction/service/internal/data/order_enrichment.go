package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/biz/shop"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

func (s *Store) orderEnrichmentsByOrderID(ctx context.Context, models []UserOrderModel) (map[string]AuctionOrderEnrichmentModel, error) {
	result := make(map[string]AuctionOrderEnrichmentModel)
	orderIDs := make([]string, 0, len(models))
	for _, model := range models {
		if model.Source == userOrderSourceAuction {
			orderIDs = append(orderIDs, model.ID)
		}
	}
	if len(orderIDs) == 0 {
		return result, nil
	}
	var enrichments []AuctionOrderEnrichmentModel
	if err := s.db.WithContext(ctx).
		Where("order_id IN ?", orderIDs).
		Find(&enrichments).Error; err != nil {
		return nil, fmt.Errorf("load auction order enrichments: %w", err)
	}
	for _, enrichment := range enrichments {
		if _, duplicate := result[enrichment.OrderID]; duplicate {
			return nil, fmt.Errorf("duplicate auction order enrichment for order %s", enrichment.OrderID)
		}
		result[enrichment.OrderID] = enrichment
	}
	return result, nil
}

func applyAuctionOrderEnrichment(order *auction.Order, model AuctionOrderEnrichmentModel, found bool) error {
	if order == nil {
		return errors.New("auction order is required")
	}
	order.EnrichmentStatus = orderenrichment.StatusPending
	order.EnrichmentUpdatedAtMs = 0
	order.ShopName = ""
	order.ShippingAddressID = ""
	order.ShippingAddressSnapshot = nil
	if !found {
		return nil
	}
	address, shopSnapshot, status, err := decodeOrderEnrichment(model)
	if err != nil {
		return err
	}
	order.EnrichmentStatus = status
	order.EnrichmentUpdatedAtMs = model.UpdatedAtUnixMs
	if address != nil {
		order.ShippingAddressID = address.AddressID
		order.ShippingAddressSnapshot = address
	}
	if shopSnapshot != nil {
		order.ShopName = shopSnapshot.ShopName
	}
	return nil
}

func applyUserOrderEnrichment(order *shop.UserOrder, model AuctionOrderEnrichmentModel, found bool) error {
	if order == nil {
		return errors.New("user order is required")
	}
	if order.Source != shop.OrderSourceAuction {
		order.EnrichmentStatus = orderenrichment.StatusReady
		return nil
	}
	order.EnrichmentStatus = orderenrichment.StatusPending
	order.EnrichmentUpdatedAtMs = 0
	order.ShopName = ""
	order.ShippingAddressID = ""
	order.ShippingAddressSnapshot = nil
	order.AddressSnapshot = ""
	if !found {
		return nil
	}
	address, shopSnapshot, status, err := decodeOrderEnrichment(model)
	if err != nil {
		return err
	}
	order.EnrichmentStatus = status
	order.EnrichmentUpdatedAtMs = model.UpdatedAtUnixMs
	if address != nil {
		order.ShippingAddressID = address.AddressID
		order.ShippingAddressSnapshot = address
		order.AddressSnapshot = address.FullAddress
	}
	if shopSnapshot != nil {
		order.ShopName = shopSnapshot.ShopName
	}
	return nil
}

func decodeOrderEnrichment(model AuctionOrderEnrichmentModel) (*shop.DeliveryAddressSnapshot, *orderenrichment.ShopSnapshot, orderenrichment.Status, error) {
	status := orderenrichment.Status(strings.TrimSpace(model.Status))
	if !status.Valid() || model.OrderID == "" || model.SourceMessageID == "" || len(model.PayloadHash) != 64 || model.UpdatedAtUnixMs <= 0 {
		return nil, nil, "", fmt.Errorf("invalid persisted order enrichment metadata for order %s", model.OrderID)
	}
	var address *shop.DeliveryAddressSnapshot
	if raw := strings.TrimSpace(model.AddressSnapshot); raw != "" && raw != "null" {
		var snapshot orderenrichment.AddressSnapshot
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot.AddressID == "" || snapshot.FullAddress == "" {
			return nil, nil, "", fmt.Errorf("invalid persisted address enrichment for order %s", model.OrderID)
		}
		address = &shop.DeliveryAddressSnapshot{
			AddressID: snapshot.AddressID, ReceiverName: snapshot.ReceiverName, Phone: snapshot.Phone,
			Province: snapshot.Province, City: snapshot.City, District: snapshot.District, Street: snapshot.Street,
			Detail: snapshot.Detail, PostalCode: snapshot.PostalCode, FullAddress: snapshot.FullAddress,
		}
	}
	var shopSnapshot *orderenrichment.ShopSnapshot
	if raw := strings.TrimSpace(model.ShopSnapshot); raw != "" && raw != "null" {
		var snapshot orderenrichment.ShopSnapshot
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot.ShopID == "" || strings.TrimSpace(snapshot.ShopName) == "" {
			return nil, nil, "", fmt.Errorf("invalid persisted shop enrichment for order %s", model.OrderID)
		}
		shopSnapshot = &snapshot
	}
	if status == orderenrichment.StatusReady && (address == nil || shopSnapshot == nil) {
		return nil, nil, "", fmt.Errorf("ready order enrichment is incomplete for order %s", model.OrderID)
	}
	return address, shopSnapshot, status, nil
}
