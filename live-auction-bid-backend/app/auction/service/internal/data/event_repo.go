package data

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
)

func (s *Store) Publish(ctx context.Context, event *v1.AuctionEvent) error {
	return s.PersistEvents(ctx, []*v1.AuctionEvent{event})
}

func (s *Store) PersistEvents(ctx context.Context, events []*v1.AuctionEvent) error {
	if len(events) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return createEventModels(ctx, tx, events)
	})
}

func (s *Store) ListRoomEvents(ctx context.Context, query auction.RoomEventQuery) (auction.RoomEventList, error) {
	if strings.TrimSpace(query.RoomID) == "" {
		return auction.RoomEventList{}, errors.New("room id is required")
	}
	_, pageSize := auction.NormalizePagination(1, query.PageSize)
	offset := 0
	if token := strings.TrimSpace(query.PageToken); token != "" {
		nextOffset, err := strconv.Atoi(token)
		if err != nil || nextOffset < 0 {
			return auction.RoomEventList{}, errors.New("invalid page token")
		}
		offset = nextOffset
	}

	var models []AuctionEventModel
	db := s.db.WithContext(ctx).Where("room_id = ?", query.RoomID)
	if query.MainAccountID != "" {
		db = db.Where("main_account_id = ?", query.MainAccountID)
	}
	if err := db.
		Order("occurred_at_unix_ms DESC").
		Order("id DESC").
		Offset(offset).
		Limit(pageSize + 1).
		Find(&models).Error; err != nil {
		return auction.RoomEventList{}, err
	}

	hasNext := len(models) > pageSize
	if hasNext {
		models = models[:pageSize]
	}
	events := make([]*v1.AuctionEvent, 0, len(models))
	for i := range models {
		event := v1.AuctionEvent{}
		if err := protojson.Unmarshal([]byte(models[i].Payload), &event); err != nil {
			return auction.RoomEventList{}, err
		}
		events = append(events, &event)
	}

	nextPageToken := ""
	if hasNext {
		nextPageToken = strconv.Itoa(offset + pageSize)
	}
	return auction.RoomEventList{Events: events, NextPageToken: nextPageToken}, nil
}

func createEventModels(ctx context.Context, tx *gorm.DB, events []*v1.AuctionEvent) error {
	for _, event := range events {
		model, err := eventToModel(event)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(model).Error; err != nil {
			return err
		}
	}
	return nil
}

func eventToModel(event *v1.AuctionEvent) (*AuctionEventModel, error) {
	if event == nil {
		return nil, errors.New("auction event is required")
	}
	if event.Id == "" {
		return nil, errors.New("event id is required")
	}
	if event.Type == v1.AuctionEventType_AUCTION_EVENT_TYPE_UNSPECIFIED {
		return nil, errors.New("event type is required")
	}
	if event.RoomId == "" {
		return nil, errors.New("event room id is required")
	}
	if event.OccurredAtUnixMs == 0 {
		return nil, errors.New("event occurred time is required")
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return nil, err
	}
	return &AuctionEventModel{
		ID:               event.Id,
		MainAccountID:    event.GetMainAccountId(),
		RoomID:           event.RoomId,
		LotID:            event.LotId,
		Type:             int32(event.Type),
		OccurredAtUnixMs: event.OccurredAtUnixMs,
		Reason:           event.Reason,
		Payload:          string(payload),
	}, nil
}
