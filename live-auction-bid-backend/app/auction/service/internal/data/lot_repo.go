package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

const missingLotCreatedAtFallback = 24 * time.Hour

func (s *Store) Create(ctx context.Context, lot *v1.Lot, ownerUserID string, events []*v1.AuctionEvent) error {
	if lot == nil {
		return errors.New("lot is required")
	}
	model, err := lotToModel(lot)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(model).Error; err != nil {
			return err
		}
		lot.CreatedAtUnixMs = modelTimeUnixMsOr(model.CreatedAt, time.Now().Add(-missingLotCreatedAtFallback).UnixMilli())
		lot.UpdatedAtUnixMs = modelTimeUnixMsOr(model.UpdatedAt, lot.CreatedAtUnixMs)
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&AuctionLotStatsModel{
			LotID:           lot.Id,
			MainAccountID:   lot.GetMainAccountId(),
			RoomID:          lot.RoomId,
			UpdatedAtUnixMs: 0,
		}).Error; err != nil {
			return err
		}
		if err := attachAssetFilesByURL(tx, ownerUserID, lot.Id, lotAssetURLs(lot)); err != nil {
			return err
		}
		if err := appendPreStartLotStateOutbox(ctx, tx, lot, lot.UpdatedAtUnixMs); err != nil {
			return err
		}
		return createEventModels(ctx, tx, events)
	})
}

func lotAssetURLs(lot *v1.Lot) []string {
	if lot == nil {
		return nil
	}
	urls := []string{lot.ImageUrl}
	urls = append(urls, lot.GetGalleryImageUrls()...)
	for _, card := range lot.TrustCards {
		if card != nil && card.ImageUrl != "" {
			urls = append(urls, card.ImageUrl)
		}
	}
	return urls
}

func (s *Store) Save(ctx context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error {
	if err := validatePreStartLotSaveRequest(lot, expectedVersion); err != nil {
		return err
	}
	model, err := lotToModel(lot)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current AuctionLotModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", lot.Id).
			First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return err
		}
		if current.Version != expectedVersion {
			return apperr.ErrLotVersionConflict
		}
		if err := s.ensureRuntimeStateAbsentForPreStartUpdate(ctx, lot.GetId()); err != nil {
			return err
		}
		if err := validatePreStartLotUpdate(&current, model, expectedVersion); err != nil {
			return err
		}
		result := tx.
			Model(&AuctionLotModel{}).
			Where("id = ? AND version = ?", lot.Id, expectedVersion).
			Updates(preStartLotUpdateColumns(model))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.ErrLotVersionConflict
		}
		if err := tx.Model(&AuctionLotStatsModel{}).
			Where("lot_id = ?", lot.GetId()).
			Updates(map[string]any{"main_account_id": lot.GetMainAccountId(), "room_id": lot.GetRoomId()}).Error; err != nil {
			return err
		}
		lot.UpdatedAtUnixMs = time.Now().UnixMilli()
		if err := appendPreStartLotStateOutbox(ctx, tx, lot, lot.UpdatedAtUnixMs); err != nil {
			return err
		}
		return createEventModels(ctx, tx, events)
	})
}

func (s *Store) ensureRuntimeStateAbsentForPreStartUpdate(ctx context.Context, lotID string) error {
	if s == nil || s.redis == nil {
		return errors.New("pre-start save requires Redis runtime verification")
	}
	if s.runtimeGenerationGuard != nil {
		if _, err := s.runtimeGenerationGuard.AllowWrite(); err != nil {
			return fmt.Errorf("%w: Redis runtime generation is not verified: %v", apperr.ErrRuntimeProjectionGap, err)
		}
	}
	exists, err := s.redis.Exists(ctx, runtimeStateKey(lotID)).Result()
	if err != nil {
		return fmt.Errorf("verify pre-start runtime absence: %w", err)
	}
	if exists != 0 {
		return fmt.Errorf("%w: lot has already entered the Redis runtime lifecycle", apperr.ErrInvalidArgument)
	}
	return nil
}

func validatePreStartLotSaveRequest(lot *v1.Lot, expectedVersion int64) error {
	if lot == nil {
		return errors.New("lot is required")
	}
	if expectedVersion <= 0 {
		return errors.New("lot expected version is required")
	}
	if !auction.IsPreStartCancellableStatus(lot.GetStatus()) {
		return fmt.Errorf("%w: generic lot save only supports pre-start configuration", apperr.ErrInvalidArgument)
	}
	if lot.GetQueueStatus() != v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE && lot.GetQueueStatus() != v1.LotQueueStatus_LOT_QUEUE_STATUS_UNSPECIFIED {
		return fmt.Errorf("%w: queued lot must be removed from the queue before editing", apperr.ErrInvalidArgument)
	}
	if lot.GetVersion() != expectedVersion+1 {
		return fmt.Errorf("%w: pre-start save must advance lot version exactly once", apperr.ErrInvalidArgument)
	}
	return nil
}

func validatePreStartLotUpdate(current, next *AuctionLotModel, expectedVersion int64) error {
	if current == nil || next == nil {
		return fmt.Errorf("%w: current and next lot models are required", apperr.ErrInvalidArgument)
	}
	if current.Version != expectedVersion {
		return apperr.ErrLotVersionConflict
	}
	if !auction.IsPreStartCancellableStatus(v1.LotStatus(current.Status)) || current.Status != next.Status {
		return fmt.Errorf("%w: lot lifecycle state is not writable through generic save", apperr.ErrInvalidArgument)
	}
	if current.QueueStatus != next.QueueStatus || current.QueuePosition != next.QueuePosition {
		return fmt.Errorf("%w: lot queue state is not writable through generic save", apperr.ErrInvalidArgument)
	}
	if current.ID != next.ID || current.MainAccountID != next.MainAccountID {
		return fmt.Errorf("%w: lot identity is immutable", apperr.ErrInvalidArgument)
	}
	if next.Version != expectedVersion+1 {
		return fmt.Errorf("%w: pre-start save must advance lot version exactly once", apperr.ErrInvalidArgument)
	}
	if next.ConfigVersion != current.ConfigVersion+1 {
		return fmt.Errorf("%w: pre-start save must advance config version exactly once", apperr.ErrInvalidArgument)
	}
	return nil
}

func preStartLotUpdateColumns(model *AuctionLotModel) map[string]any {
	return map[string]any{
		"room_id":                   model.RoomID,
		"title":                     model.Title,
		"description":               model.Description,
		"image_url":                 model.ImageURL,
		"currency":                  model.Currency,
		"start_price_amount":        model.StartPriceAmount,
		"min_increment_amount":      model.MinIncrementAmount,
		"cap_price_amount":          model.CapPriceAmount,
		"duration_seconds":          model.DurationSeconds,
		"anti_snipe_window_seconds": model.AntiSnipeWindowSeconds,
		"anti_snipe_extend_seconds": model.AntiSnipeExtendSeconds,
		"max_extend_count":          model.MaxExtendCount,
		"current_price_amount":      model.CurrentPriceAmount,
		"version":                   model.Version,
		"config_version":            model.ConfigVersion,
		"payload":                   model.Payload,
	}
}

func (s *Store) AttachAssets(ctx context.Context, ownerUserID string, lot *v1.Lot) error {
	if lot == nil || ownerUserID == "" {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return attachAssetFilesByURL(tx, ownerUserID, lot.Id, lotAssetURLs(lot))
	})
}

func (s *Store) FindByID(ctx context.Context, lotID string) (*v1.Lot, error) {
	return s.findByID(ctx, lotID, true)
}

func (s *Store) FindCoreByID(ctx context.Context, lotID string) (*v1.Lot, error) {
	return s.findByID(ctx, lotID, false)
}

// FindByIDs hydrates search candidates from MySQL in one query, attaches stats
// in one query, then overlays all available Redis runtime states in one pipeline.
func (s *Store) FindByIDs(ctx context.Context, lotIDs []string) ([]*v1.Lot, error) {
	if len(lotIDs) == 0 {
		return nil, nil
	}
	if len(lotIDs) > 100 {
		return nil, errors.New("at most 100 lot ids are allowed")
	}
	var models []AuctionLotModel
	if err := s.db.WithContext(ctx).Where("id IN ?", lotIDs).Find(&models).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*v1.Lot, len(models))
	for i := range models {
		lot, err := modelToLot(&models[i])
		if err != nil {
			return nil, err
		}
		byID[lot.GetId()] = lot
	}
	lots := make([]*v1.Lot, 0, len(models))
	for _, lotID := range lotIDs {
		if lot := byID[lotID]; lot != nil {
			lots = append(lots, lot)
		}
	}
	if err := s.attachStats(ctx, lots); err != nil {
		return nil, err
	}
	if s.redis == nil || len(lots) == 0 {
		if err := s.attachLotPresentations(ctx, lots); err != nil {
			return nil, err
		}
		return lots, nil
	}
	pipe := s.redis.Pipeline()
	commands := make([]*redis.MapStringStringCmd, len(lots))
	for index, lot := range lots {
		commands[index] = pipe.HGetAll(ctx, runtimeStateKey(lot.GetId()))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read batch lot runtime states: %w", err)
	}
	for index, command := range commands {
		values, err := command.Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("read lot %s runtime state: %w", lots[index].GetId(), err)
		}
		if len(values) == 0 {
			if lots[index].GetStatus() == v1.LotStatus_LOT_STATUS_LIVE || lots[index].GetStatus() == v1.LotStatus_LOT_STATUS_EXTENDED {
				return nil, fmt.Errorf("live lot %s runtime state is missing", lots[index].GetId())
			}
			continue
		}
		overlay, err := authoritativeRuntimeLot(lots[index], values)
		if err != nil {
			return nil, err
		}
		lots[index] = overlay
	}
	if err := s.attachLotPresentations(ctx, lots); err != nil {
		return nil, err
	}
	return lots, nil
}

func authoritativeRuntimeLot(base *v1.Lot, values map[string]string) (*v1.Lot, error) {
	if base == nil || len(values) == 0 {
		return nil, errors.New("base lot and runtime values are required")
	}
	if values["lot_id"] != base.GetId() || values["room_id"] != base.GetRoomId() || values["main_account_id"] != base.GetMainAccountId() ||
		strings.TrimSpace(values["version"]) == "" || strings.TrimSpace(values["status"]) == "" {
		return nil, fmt.Errorf("lot %s runtime identity or state is incomplete", base.GetId())
	}
	overlay := runtimeStateToLot(base, values)
	if overlay.GetId() != base.GetId() || overlay.GetRoomId() != base.GetRoomId() || overlay.GetMainAccountId() != base.GetMainAccountId() {
		return nil, fmt.Errorf("lot %s runtime identity is inconsistent", base.GetId())
	}
	if overlay.GetVersion() < base.GetVersion() {
		return nil, fmt.Errorf("lot %s runtime version %d is behind MySQL version %d", base.GetId(), overlay.GetVersion(), base.GetVersion())
	}
	return overlay, nil
}

func (s *Store) findByID(ctx context.Context, lotID string, includeStats bool) (*v1.Lot, error) {
	if lotID == "" {
		return nil, errors.New("lot id is required")
	}
	var model AuctionLotModel
	if err := s.db.WithContext(ctx).Where("id = ?", lotID).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound
		}
		return nil, err
	}
	lot, err := modelToLot(&model)
	if err != nil {
		return nil, err
	}
	if !includeStats {
		return lot, nil
	}
	if err := s.attachStats(ctx, []*v1.Lot{lot}); err != nil {
		return nil, err
	}
	if err := s.attachLotPresentations(ctx, []*v1.Lot{lot}); err != nil {
		return nil, err
	}
	return lot, nil
}

func (s *Store) List(ctx context.Context, roomID string, status v1.LotStatus) ([]*v1.Lot, error) {
	if roomID == "" {
		return nil, errors.New("room id is required")
	}
	query := s.db.WithContext(ctx).Where("room_id = ?", roomID)
	if status != 0 {
		if status == v1.LotStatus_LOT_STATUS_LIVE {
			query = query.Where("status IN ?", []int32{int32(v1.LotStatus_LOT_STATUS_LIVE), int32(v1.LotStatus_LOT_STATUS_EXTENDED)})
		} else {
			query = query.Where("status = ?", int32(status))
		}
	}
	var models []AuctionLotModel
	if status == v1.LotStatus_LOT_STATUS_QUEUED {
		query = query.Order("queue_position ASC")
	} else {
		query = query.Order("updated_at DESC")
	}
	if err := query.Order("id ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	lots := make([]*v1.Lot, 0, len(models))
	for i := range models {
		lot, err := modelToLot(&models[i])
		if err != nil {
			return nil, err
		}
		lots = append(lots, lot)
	}
	if err := s.attachStats(ctx, lots); err != nil {
		return nil, err
	}
	if err := s.attachLotPresentations(ctx, lots); err != nil {
		return nil, err
	}
	return lots, nil
}

func (s *Store) ListLots(ctx context.Context, query auction.LotQuery) (auction.LotList, error) {
	query.Page, query.PageSize = auction.NormalizePagination(query.Page, query.PageSize)
	db := s.db.WithContext(ctx).Model(&AuctionLotModel{})
	if query.MainAccountID != "" {
		db = db.Where("main_account_id = ?", query.MainAccountID)
	}
	if query.RoomID != "" {
		db = db.Where("room_id = ?", query.RoomID)
	}
	if statuses := lotStatusesForView(query.View); len(statuses) > 0 {
		db = db.Where("status IN ?", statuses)
	}
	if query.Status != v1.LotStatus_LOT_STATUS_UNSPECIFIED {
		db = db.Where("status = ?", int32(query.Status))
	}
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where("id LIKE ? OR title LIKE ? OR description LIKE ? OR cancel_reason LIKE ?", like, like, like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return auction.LotList{}, err
	}
	var models []AuctionLotModel
	ordered := db
	if query.Status == v1.LotStatus_LOT_STATUS_QUEUED {
		ordered = ordered.Order("queue_position ASC")
	} else {
		ordered = ordered.Order("updated_at DESC")
	}
	if err := ordered.
		Order("id ASC").
		Offset(auction.PageOffset(query.Page, query.PageSize)).
		Limit(query.PageSize).
		Find(&models).Error; err != nil {
		return auction.LotList{}, err
	}
	lots := make([]*v1.Lot, 0, len(models))
	for i := range models {
		lot, err := modelToLot(&models[i])
		if err != nil {
			return auction.LotList{}, err
		}
		lots = append(lots, lot)
	}
	if err := s.attachStats(ctx, lots); err != nil {
		return auction.LotList{}, err
	}
	if err := s.attachLotPresentations(ctx, lots); err != nil {
		return auction.LotList{}, err
	}
	return auction.LotList{Lots: lots, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *Store) attachStats(ctx context.Context, lots []*v1.Lot) error {
	if len(lots) == 0 {
		return nil
	}
	lotIDs := make([]string, 0, len(lots))
	for _, lot := range lots {
		if lot != nil && lot.Id != "" {
			lotIDs = append(lotIDs, lot.Id)
		}
	}
	if len(lotIDs) == 0 {
		return nil
	}
	var models []AuctionLotStatsModel
	if err := s.db.WithContext(ctx).Where("lot_id IN ?", lotIDs).Find(&models).Error; err != nil {
		return err
	}
	byLotID := make(map[string]*v1.LotStats, len(models))
	for i := range models {
		byLotID[models[i].LotID] = &v1.LotStats{
			ParticipantCount: models[i].ParticipantCount,
			BidCount:         models[i].BidCount,
		}
	}
	for _, lot := range lots {
		if lot == nil {
			continue
		}
		if stats := byLotID[lot.Id]; stats != nil {
			lot.Stats = stats
		} else {
			lot.Stats = &v1.LotStats{}
		}
	}
	return nil
}

func lotStatusesForView(view string) []int32 {
	switch strings.ToLower(strings.TrimSpace(view)) {
	case "current":
		return []int32{
			int32(v1.LotStatus_LOT_STATUS_DRAFT),
			int32(v1.LotStatus_LOT_STATUS_READY),
			int32(v1.LotStatus_LOT_STATUS_QUEUED),
			int32(v1.LotStatus_LOT_STATUS_LIVE),
			int32(v1.LotStatus_LOT_STATUS_EXTENDED),
		}
	case "history":
		return []int32{
			int32(v1.LotStatus_LOT_STATUS_SETTLED),
			int32(v1.LotStatus_LOT_STATUS_CANCELLED),
			int32(v1.LotStatus_LOT_STATUS_FAILED),
		}
	case "library":
		return []int32{
			int32(v1.LotStatus_LOT_STATUS_DRAFT),
			int32(v1.LotStatus_LOT_STATUS_READY),
		}
	default:
		return nil
	}
}

func lotToModel(lot *v1.Lot) (*AuctionLotModel, error) {
	if lot.ConfigVersion <= 0 {
		lot.ConfigVersion = 1
	}
	var capAmount *int64
	rule := lot.GetRule()
	if rule == nil {
		rule = &v1.BidRule{}
		lot.Rule = rule
	}
	startPrice := rule.GetStartPrice()
	if startPrice == nil {
		startPrice = &v1.Money{}
	}
	minIncrement := rule.GetMinIncrement()
	if minIncrement == nil {
		minIncrement = &v1.Money{}
	}
	currentPrice := lot.GetCurrentPrice()
	if currentPrice == nil {
		currentPrice = &v1.Money{}
	}
	finalPrice := lot.GetFinalPrice()
	if finalPrice == nil {
		finalPrice = &v1.Money{}
	}
	if rule.GetCapPrice() != nil {
		amount := rule.GetCapPrice().GetAmount()
		capAmount = &amount
	}
	currency, err := unifiedLotCurrency(startPrice, minIncrement, rule.GetCapPrice(), currentPrice, finalPrice)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", apperr.ErrInvalidArgument, err)
	}
	payload, err := protojson.Marshal(lot)
	if err != nil {
		return nil, err
	}
	return &AuctionLotModel{
		ID:                     lot.Id,
		MainAccountID:          lot.GetMainAccountId(),
		RoomID:                 lot.RoomId,
		Title:                  lot.Title,
		Description:            lot.Description,
		ImageURL:               lot.ImageUrl,
		Status:                 int32(lot.Status),
		QueueStatus:            int32(normalizeQueueStatus(lot.GetQueueStatus())),
		QueuePosition:          lot.GetQueuePosition(),
		Currency:               currency,
		StartPriceAmount:       startPrice.GetAmount(),
		MinIncrementAmount:     minIncrement.GetAmount(),
		CapPriceAmount:         capAmount,
		DurationSeconds:        rule.GetDurationSeconds(),
		AntiSnipeWindowSeconds: rule.GetAntiSnipeWindowSeconds(),
		AntiSnipeExtendSeconds: rule.GetAntiSnipeExtendSeconds(),
		MaxExtendCount:         rule.GetMaxExtendCount(),
		CurrentPriceAmount:     currentPrice.GetAmount(),
		LeadingUserID:          lot.LeadingUserId,
		LeadingNickname:        lot.LeadingNickname,
		StartedAtUnixMs:        lot.StartedAtUnixMs,
		EndsAtUnixMs:           lot.EndsAtUnixMs,
		SettledAtUnixMs:        lot.SettledAtUnixMs,
		CancelReason:           lot.CancelReason,
		CancelledAtUnixMs:      lot.CancelledAtUnixMs,
		WinnerUserID:           lot.WinnerUserId,
		WinnerNickname:         lot.WinnerNickname,
		FinalPriceAmount:       finalPrice.GetAmount(),
		Version:                lot.Version,
		ConfigVersion:          lot.ConfigVersion,
		PlaybookStage:          int32(lot.PlaybookStage),
		Payload:                string(payload),
	}, nil
}

func unifiedLotCurrency(values ...*v1.Money) (string, error) {
	currency := ""
	for _, value := range values {
		if value == nil || strings.TrimSpace(value.GetCurrency()) == "" {
			continue
		}
		candidate := strings.TrimSpace(value.GetCurrency())
		if len(candidate) != 3 || strings.ToUpper(candidate) != candidate {
			return "", errors.New("lot currency must be three uppercase letters")
		}
		if currency != "" && candidate != currency {
			return "", errors.New("all lot monetary fields must use one currency")
		}
		currency = candidate
	}
	if currency == "" {
		currency = "CNY"
	}
	return currency, nil
}

func normalizeQueueStatus(status v1.LotQueueStatus) v1.LotQueueStatus {
	if status == v1.LotQueueStatus_LOT_QUEUE_STATUS_UNSPECIFIED {
		return v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE
	}
	return status
}

func modelTimeUnixMsOr(value time.Time, fallback int64) int64 {
	if value.IsZero() {
		return fallback
	}
	return value.UnixMilli()
}

func modelToLot(model *AuctionLotModel) (*v1.Lot, error) {
	lot := &v1.Lot{}
	if err := protojson.Unmarshal([]byte(model.Payload), lot); err != nil {
		return nil, err
	}
	lot.Id = model.ID
	lot.MainAccountId = model.MainAccountID
	lot.RoomId = model.RoomID
	lot.Title = model.Title
	lot.Description = model.Description
	lot.ImageUrl = model.ImageURL
	lot.Status = v1.LotStatus(model.Status)
	if lot.Rule == nil {
		lot.Rule = &v1.BidRule{}
	}
	lot.Rule.StartPrice = &v1.Money{Amount: model.StartPriceAmount, Currency: model.Currency}
	lot.Rule.MinIncrement = &v1.Money{Amount: model.MinIncrementAmount, Currency: model.Currency}
	lot.Rule.DurationSeconds = model.DurationSeconds
	lot.Rule.AntiSnipeWindowSeconds = model.AntiSnipeWindowSeconds
	lot.Rule.AntiSnipeExtendSeconds = model.AntiSnipeExtendSeconds
	lot.Rule.MaxExtendCount = model.MaxExtendCount
	if lot.Stock < 1 {
		lot.Stock = 1
	}
	lot.QueueStatus = normalizeQueueStatus(v1.LotQueueStatus(model.QueueStatus))
	lot.QueuePosition = model.QueuePosition
	lot.CurrentPrice = &v1.Money{Amount: model.CurrentPriceAmount, Currency: model.Currency}
	lot.LeadingUserId = model.LeadingUserID
	lot.LeadingNickname = model.LeadingNickname
	lot.StartedAtUnixMs = model.StartedAtUnixMs
	lot.EndsAtUnixMs = model.EndsAtUnixMs
	lot.SettledAtUnixMs = model.SettledAtUnixMs
	lot.CancelReason = model.CancelReason
	lot.CancelledAtUnixMs = model.CancelledAtUnixMs
	lot.WinnerUserId = model.WinnerUserID
	lot.WinnerNickname = model.WinnerNickname
	lot.FinalPrice = &v1.Money{Amount: model.FinalPriceAmount, Currency: model.Currency}
	lot.Version = model.Version
	lot.ConfigVersion = model.ConfigVersion
	if lot.ConfigVersion <= 0 {
		lot.ConfigVersion = 1
	}
	lot.PlaybookStage = v1.PlaybookStage(model.PlaybookStage)
	lot.CreatedAtUnixMs = modelTimeUnixMsOr(model.CreatedAt, time.Now().Add(-missingLotCreatedAtFallback).UnixMilli())
	lot.UpdatedAtUnixMs = modelTimeUnixMsOr(model.UpdatedAt, lot.CreatedAtUnixMs)
	if model.CapPriceAmount != nil {
		lot.Rule.CapPrice = &v1.Money{Amount: *model.CapPriceAmount, Currency: model.Currency}
	} else {
		lot.Rule.CapPrice = nil
	}
	return lot, nil
}
