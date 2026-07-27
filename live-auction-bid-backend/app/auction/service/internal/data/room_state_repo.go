package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func (s *Store) QueueLotAsNext(ctx context.Context, lotID, mainAccountID, ownerUserID string, nowMs int64) (*v1.Lot, int32, []*v1.AuctionEvent, error) {
	lotID = strings.TrimSpace(lotID)
	mainAccountID = strings.TrimSpace(mainAccountID)
	if lotID == "" {
		return nil, 0, nil, errors.New("lot id is required")
	}
	if mainAccountID == "" {
		return nil, 0, nil, fmt.Errorf("%w: main account id is required", apperr.ErrPermissionDenied)
	}
	if nowMs <= 0 {
		nowMs = time.Now().UnixMilli()
	}

	var queuedLot *v1.Lot
	var queuePosition int32
	var events []*v1.AuctionEvent
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current AuctionLotModel
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", lotID).
			First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return err
		}
		if current.MainAccountID != mainAccountID {
			return apperr.ErrPermissionDenied
		}
		if err := s.ensureRuntimeStateAbsentForPreStartUpdate(ctx, lotID); err != nil {
			return err
		}
		if current.RoomID == "" {
			return errors.New("room id is required")
		}
		if err := ensureRoomStateRecord(ctx, tx, current.RoomID, current.MainAccountID, nowMs); err != nil {
			return err
		}
		var state AuctionRoomStateModel
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("room_id = ?", current.RoomID).
			First(&state).Error; err != nil {
			return err
		}

		lot, err := modelToLot(&current)
		if err != nil {
			return err
		}
		queuePosition = lot.GetQueuePosition()
		alreadyQueued := lot.GetQueueStatus() == v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED && queuePosition > 0
		if alreadyQueued {
			queuedLot = lot
			return nil
		}
		if queuePosition <= 0 {
			queuePosition = state.NextQueuePosition
			if queuePosition <= 0 {
				queuePosition = 1
			}
		}
		if err := auction.QueueLot(lot, queuePosition); err != nil {
			return err
		}
		model, err := lotToModel(lot)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).
			Model(&AuctionLotModel{}).
			Where("id = ? AND version = ?", lot.GetId(), current.Version).
			Updates(queueLotUpdateColumns(model))
		if result.Error != nil {
			return mapQueuePositionConflict(result.Error)
		}
		if result.RowsAffected == 0 {
			return apperr.ErrLotVersionConflict
		}
		if state.NextQueuePosition <= queuePosition {
			state.NextQueuePosition = queuePosition + 1
		}
		if err := tx.WithContext(ctx).
			Model(&AuctionRoomStateModel{}).
			Where("room_id = ?", lot.GetRoomId()).
			Updates(map[string]any{
				"main_account_id":     lot.GetMainAccountId(),
				"next_queue_position": state.NextQueuePosition,
				"updated_at_unix_ms":  nowMs,
			}).Error; err != nil {
			return err
		}
		if err := attachAssetFilesByURL(tx, ownerUserID, lot.GetId(), lotAssetURLs(lot)); err != nil {
			return err
		}
		event := auction.NewAuctionEvent(v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_QUEUED, lot)
		events = []*v1.AuctionEvent{event}
		if err := createEventModels(ctx, tx, events); err != nil {
			return err
		}
		lot.UpdatedAtUnixMs = nowMs
		if err := appendPreStartLotStateOutbox(ctx, tx, lot, nowMs); err != nil {
			return err
		}
		queuedLot = lot
		return nil
	}); err != nil {
		return nil, 0, nil, err
	}
	return queuedLot, queuePosition, events, nil
}

func queueLotUpdateColumns(model *AuctionLotModel) map[string]any {
	return map[string]any{
		"status":               model.Status,
		"queue_status":         model.QueueStatus,
		"queue_position":       model.QueuePosition,
		"current_price_amount": model.CurrentPriceAmount,
		"final_price_amount":   model.FinalPriceAmount,
		"version":              model.Version,
		"payload":              model.Payload,
	}
}

func (s *Store) FindRoomState(ctx context.Context, roomID, mainAccountID string) (*auction.RoomState, error) {
	roomID = strings.TrimSpace(roomID)
	mainAccountID = strings.TrimSpace(mainAccountID)
	if roomID == "" || mainAccountID == "" {
		return nil, fmt.Errorf("%w: room id and main account id are required", apperr.ErrInvalidArgument)
	}
	var model AuctionRoomStateModel
	if err := s.db.WithContext(ctx).Where("room_id = ? AND main_account_id = ?", roomID, mainAccountID).First(&model).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return &auction.RoomState{RoomID: roomID, MainAccountID: mainAccountID, NextQueuePosition: 1}, nil
	} else if err != nil {
		return nil, err
	}
	return roomStateFromModel(&model), nil
}

func ensureRoomStateRecord(ctx context.Context, db *gorm.DB, roomID, mainAccountID string, nowMs int64) error {
	roomID = strings.TrimSpace(roomID)
	mainAccountID = strings.TrimSpace(mainAccountID)
	if roomID == "" {
		return errors.New("room id is required")
	}
	if mainAccountID == "" {
		return errors.New("main account id is required")
	}
	if nowMs <= 0 {
		nowMs = time.Now().UnixMilli()
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&AuctionRoomStateModel{
		RoomID:            roomID,
		MainAccountID:     mainAccountID,
		ActiveLotID:       "",
		DisplayLotID:      "",
		ActiveLotVersion:  0,
		NextQueuePosition: 1,
		UpdatedAtUnixMs:   nowMs,
	}).Error
}

func roomStateFromModel(model *AuctionRoomStateModel) *auction.RoomState {
	if model == nil {
		return nil
	}
	return &auction.RoomState{
		RoomID:            model.RoomID,
		MainAccountID:     model.MainAccountID,
		ActiveLotID:       model.ActiveLotID,
		DisplayLotID:      model.DisplayLotID,
		ActiveLotVersion:  model.ActiveLotVersion,
		NextQueuePosition: model.NextQueuePosition,
		UpdatedAtUnixMs:   model.UpdatedAtUnixMs,
	}
}

func mapQueuePositionConflict(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "uidx_one_queued_position_per_room") || strings.Contains(message, "queued_room_position_key") {
		return apperr.ErrQueuePositionConflict
	}
	return err
}
