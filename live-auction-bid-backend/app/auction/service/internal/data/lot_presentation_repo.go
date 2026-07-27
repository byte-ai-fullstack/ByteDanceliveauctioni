package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func (s *Store) SaveLotPresentation(ctx context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error {
	if lot == nil {
		return errors.New("lot presentation is required")
	}
	if lot.GetId() == "" || lot.GetMainAccountId() == "" {
		return errors.New("lot presentation identity is required")
	}
	if expectedVersion < 0 || lot.GetPresentationVersion() != expectedVersion+1 {
		return errors.New("lot presentation version must advance by one")
	}
	model, err := lotPresentationToModel(lot, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if expectedVersion == 0 {
			if err := tx.Create(model).Error; err != nil {
				if isPresentationDuplicateKey(err) {
					return apperr.ErrLotVersionConflict
				}
				return err
			}
		} else {
			result := tx.Model(&AuctionLotPresentationModel{}).
				Where("lot_id = ? AND version = ?", lot.GetId(), expectedVersion).
				Updates(map[string]any{
					"main_account_id":    model.MainAccountID,
					"version":            model.Version,
					"payload":            model.Payload,
					"updated_at_unix_ms": model.UpdatedAtUnixMs,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return apperr.ErrLotVersionConflict
			}
		}
		return createEventModels(ctx, tx, events)
	})
}

func lotPresentationToModel(lot *v1.Lot, updatedAtUnixMs int64) (*AuctionLotPresentationModel, error) {
	if lot == nil || lot.GetPresentationVersion() <= 0 || updatedAtUnixMs <= 0 {
		return nil, errors.New("valid lot presentation and update time are required")
	}
	presentation := &v1.Lot{
		Id:                  lot.GetId(),
		MainAccountId:       lot.GetMainAccountId(),
		TrustCards:          cloneTrustCardsForStorage(lot.GetTrustCards()),
		DuelState:           cloneDuelStateForStorage(lot.GetDuelState()),
		PlaybookStage:       lot.GetPlaybookStage(),
		PresentationVersion: lot.GetPresentationVersion(),
	}
	payload, err := protojson.Marshal(presentation)
	if err != nil {
		return nil, fmt.Errorf("marshal lot presentation: %w", err)
	}
	return &AuctionLotPresentationModel{
		LotID: lot.GetId(), MainAccountID: lot.GetMainAccountId(), Version: lot.GetPresentationVersion(),
		Payload: string(payload), UpdatedAtUnixMs: updatedAtUnixMs,
	}, nil
}

func (s *Store) attachLotPresentations(ctx context.Context, lots []*v1.Lot) error {
	lotIDs := make([]string, 0, len(lots))
	for _, lot := range lots {
		if lot != nil && lot.GetId() != "" {
			lotIDs = append(lotIDs, lot.GetId())
		}
	}
	if len(lotIDs) == 0 {
		return nil
	}
	var models []AuctionLotPresentationModel
	if err := s.db.WithContext(ctx).Where("lot_id IN ?", lotIDs).Find(&models).Error; err != nil {
		return fmt.Errorf("load lot presentations: %w", err)
	}
	byLotID := make(map[string]*AuctionLotPresentationModel, len(models))
	for index := range models {
		byLotID[models[index].LotID] = &models[index]
	}
	for _, lot := range lots {
		model := byLotID[lot.GetId()]
		if model == nil {
			continue
		}
		presentation, err := modelToLotPresentation(model)
		if err != nil {
			return err
		}
		if err := auction.OverlayLotPresentation(lot, presentation); err != nil {
			return fmt.Errorf("overlay lot %s presentation: %w", lot.GetId(), err)
		}
	}
	return nil
}

func modelToLotPresentation(model *AuctionLotPresentationModel) (*v1.Lot, error) {
	if model == nil || model.LotID == "" || model.MainAccountID == "" || model.Version <= 0 {
		return nil, errors.New("stored lot presentation is invalid")
	}
	presentation := new(v1.Lot)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(model.Payload), presentation); err != nil {
		return nil, fmt.Errorf("decode lot %s presentation: %w", model.LotID, err)
	}
	presentation.Id = model.LotID
	presentation.MainAccountId = model.MainAccountID
	presentation.PresentationVersion = model.Version
	return presentation, nil
}

func cloneTrustCardsForStorage(cards []*v1.TrustRevealCard) []*v1.TrustRevealCard {
	cloned := make([]*v1.TrustRevealCard, 0, len(cards))
	for _, card := range cards {
		if card != nil {
			cloned = append(cloned, proto.Clone(card).(*v1.TrustRevealCard))
		}
	}
	return cloned
}

func cloneDuelStateForStorage(duel *v1.DuelState) *v1.DuelState {
	if duel == nil {
		return nil
	}
	return proto.Clone(duel).(*v1.DuelState)
}

func isPresentationDuplicateKey(err error) bool {
	var mysqlError *mysqlDriver.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}
