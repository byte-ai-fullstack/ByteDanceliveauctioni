package data

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
)

const bidIdempotencyCacheTTL = 24 * time.Hour

func (s *Store) ListByLot(ctx context.Context, lotID string) ([]*v1.Bid, error) {
	if lotID == "" {
		return nil, errors.New("lot id is required")
	}
	var models []AuctionBidModel
	if err := s.db.WithContext(ctx).
		Where("lot_id = ?", lotID).
		Order("created_at_unix_ms ASC").
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}

	bids := make([]*v1.Bid, 0, len(models))
	for i := range models {
		bid := &v1.Bid{}
		if err := protojson.Unmarshal([]byte(models[i].Payload), bid); err != nil {
			return nil, err
		}
		bids = append(bids, bid)
	}
	return bids, nil
}

func (s *Store) ListBidRecordsByBuyer(ctx context.Context, buyerUserID string, query auction.BidRecordQuery) (auction.BidRecordList, error) {
	if buyerUserID == "" {
		return auction.BidRecordList{}, errors.New("buyer user id is required")
	}
	query.Page, query.PageSize = auction.NormalizePagination(query.Page, query.PageSize)
	db := s.db.WithContext(ctx).Model(&AuctionBidModel{}).Where("user_id = ?", buyerUserID)
	if query.LotID != "" {
		db = db.Where("lot_id = ?", query.LotID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return auction.BidRecordList{}, err
	}
	var models []AuctionBidModel
	if err := db.
		Order("created_at_unix_ms DESC").
		Order("id ASC").
		Offset(auction.PageOffset(query.Page, query.PageSize)).
		Limit(query.PageSize).
		Find(&models).Error; err != nil {
		return auction.BidRecordList{}, err
	}
	lotIDs := make([]string, 0, len(models))
	seenLot := make(map[string]bool, len(models))
	for _, model := range models {
		if model.LotID != "" && !seenLot[model.LotID] {
			lotIDs = append(lotIDs, model.LotID)
			seenLot[model.LotID] = true
		}
	}
	lotsByID := make(map[string]*v1.Lot, len(lotIDs))
	if len(lotIDs) > 0 {
		var lotModels []AuctionLotModel
		if err := s.db.WithContext(ctx).Where("id IN ?", lotIDs).Find(&lotModels).Error; err != nil {
			return auction.BidRecordList{}, err
		}
		for i := range lotModels {
			lot, err := modelToLot(&lotModels[i])
			if err != nil {
				return auction.BidRecordList{}, err
			}
			lotsByID[lot.Id] = lot
		}
	}
	records := make([]auction.BidRecord, 0, len(models))
	for _, model := range models {
		lot := lotsByID[model.LotID]
		record := auction.BidRecord{
			ID:              model.ID,
			LotID:           model.LotID,
			UserID:          model.UserID,
			Nickname:        model.Nickname,
			Amount:          model.Amount,
			Currency:        model.Currency,
			CreatedAtUnixMs: model.CreatedAtUnixMs,
		}
		if lot != nil {
			record.RoomID = lot.RoomId
			record.LotTitle = lot.Title
			record.LotImageURL = lot.ImageUrl
			record.LotStatus = lot.Status.String()
			record.AuctionState = auction.AuctionStateOf(lot)
			record.Won = lot.WinnerUserId != "" && lot.WinnerUserId == buyerUserID
		}
		records = append(records, record)
	}
	return auction.BidRecordList{Bids: records, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Store) FindByIdempotencyKey(ctx context.Context, lotID, userID, key string) (*v1.Bid, bool, error) {
	if lotID == "" {
		return nil, false, errors.New("lot id is required")
	}
	if userID == "" {
		return nil, false, errors.New("user id is required")
	}
	if key == "" {
		return nil, false, errors.New("idempotency key is required")
	}
	payload, err := s.redis.Get(ctx, idempotencyKey(lotID, userID, key)).Bytes()
	if err != nil {
		return s.findByIdempotencyKeyInDB(ctx, lotID, userID, key)
	}
	bid := &v1.Bid{}
	if err := protojson.Unmarshal(payload, bid); err != nil {
		return s.findByIdempotencyKeyInDB(ctx, lotID, userID, key)
	}
	return bid, true, nil
}

func (s *Store) findByIdempotencyKeyInDB(ctx context.Context, lotID, userID, key string) (*v1.Bid, bool, error) {
	var model AuctionBidModel
	if err := s.db.WithContext(ctx).
		Where("lot_id = ? AND user_id = ? AND idempotency_key = ?", lotID, userID, key).
		First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	bid := &v1.Bid{}
	if err := protojson.Unmarshal([]byte(model.Payload), bid); err != nil {
		return nil, false, err
	}
	return bid, true, nil
}

func (s *Store) CacheIdempotencyKey(ctx context.Context, lotID, userID, key string, bid *v1.Bid) {
	if lotID == "" || userID == "" || key == "" || bid == nil {
		return
	}
	payload, err := protojson.Marshal(bid)
	if err != nil {
		return
	}
	_ = s.redis.SetNX(ctx, idempotencyKey(lotID, userID, key), payload, bidIdempotencyCacheTTL).Err()
}

func idempotencyKey(lotID, userID, key string) string {
	return "auction:idem:" + lotID + ":" + userID + ":" + key
}
