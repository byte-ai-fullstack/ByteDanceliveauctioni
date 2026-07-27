package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
)

var errLotStateDocumentIncomplete = errors.New("lot state search document is incomplete")

type lotStateOutboxMessage struct {
	MessageID    string
	CausationID  string
	PartitionKey string
	Payload      []byte
	HeadersJSON  []byte
	CreatedAtMs  int64
}

// appendPreStartLotStateOutbox keeps MySQL-owned management transitions on the
// same durable domain-event path as Redis-owned runtime transitions. Active and
// terminal state is deliberately excluded because Redis Lua + Projector owns it.
func appendPreStartLotStateOutbox(ctx context.Context, tx *gorm.DB, lot *v1.Lot, occurredAtMs int64) error {
	if tx == nil || lot == nil {
		return errors.New("lot state outbox transaction and lot are required")
	}
	switch lot.GetStatus() {
	case v1.LotStatus_LOT_STATUS_DRAFT, v1.LotStatus_LOT_STATUS_READY, v1.LotStatus_LOT_STATUS_QUEUED:
	default:
		return nil
	}
	message, err := buildPreStartLotStateOutboxMessage(ctx, lot, occurredAtMs)
	if errors.Is(err, errLotStateDocumentIncomplete) && lot.GetStatus() != v1.LotStatus_LOT_STATUS_QUEUED {
		// Incomplete drafts are intentionally absent from public search. Queueing
		// validates the complete document and must never take this branch.
		return nil
	}
	if err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
INSERT INTO auction_domain_outbox
  (message_id, causation_id, topic, partition_key, payload, headers_json,
   created_at_ms, published_at_ms, attempts, next_attempt_ms,
   locked_by, lock_token, locked_until_ms, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 0, '', '', 0, '')`,
		message.MessageID, message.CausationID, eventcontract.LotStateTopicV1, message.PartitionKey,
		message.Payload, message.HeadersJSON, message.CreatedAtMs,
	)
	if result.Error != nil {
		return fmt.Errorf("insert pre-start lot state domain outbox: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("insert pre-start lot state domain outbox affected an unexpected row count")
	}
	return nil
}

func buildPreStartLotStateOutboxMessage(ctx context.Context, lot *v1.Lot, occurredAtMs int64) (lotStateOutboxMessage, error) {
	if lot == nil {
		return lotStateOutboxMessage{}, errLotStateDocumentIncomplete
	}
	if occurredAtMs <= 0 {
		occurredAtMs = time.Now().UnixMilli()
	}
	causationID, err := eventcontract.NewEventID()
	if err != nil {
		return lotStateOutboxMessage{}, err
	}
	messageID, err := eventcontract.DomainMessageID(causationID, eventcontract.LotStateTopicV1)
	if err != nil {
		return lotStateOutboxMessage{}, err
	}
	traceID := strings.TrimSpace(requestctx.TraceID(ctx))
	if !validLotStateTraceID(traceID) {
		traceID = causationID
	}
	startPrice := lot.GetRule().GetStartPrice()
	currentPrice := lot.GetCurrentPrice()
	if currentPrice == nil || strings.TrimSpace(currentPrice.GetCurrency()) == "" {
		currentPrice = startPrice
	}
	event := &v1.LotStateDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{
			MessageId: messageID, CausationId: causationID, TraceId: traceID,
			SchemaVersion: 1, OccurredAtUnixMs: occurredAtMs,
		},
		LotId: lot.GetId(), RoomId: lot.GetRoomId(), MainAccountId: lot.GetMainAccountId(),
		LotVersion: lot.GetVersion(), Status: lot.GetStatus(), Title: lot.GetTitle(), Description: lot.GetDescription(),
		Category: lot.GetCategory(), Tags: append([]string(nil), lot.GetTags()...), ImageUrl: lot.GetImageUrl(),
		StartPriceFen: startPrice.GetAmount(), CurrentPriceFen: currentPrice.GetAmount(), Currency: currentPrice.GetCurrency(),
		StartsAtUnixMs: lot.GetStartedAtUnixMs(), EndsAtUnixMs: lot.GetEndsAtUnixMs(),
	}
	contentHash, err := eventcontract.LotStateContentHash(event)
	if err != nil {
		return lotStateOutboxMessage{}, err
	}
	event.ContentHash = contentHash
	if err := eventcontract.ValidateLotStateDomainEvent(event); err != nil {
		return lotStateOutboxMessage{}, fmt.Errorf("%w: %v", errLotStateDocumentIncomplete, err)
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		return lotStateOutboxMessage{}, fmt.Errorf("marshal pre-start lot state domain event: %w", err)
	}
	headers, err := json.Marshal(map[string]string{
		eventcontract.RuntimeHeaderContentType:   eventcontract.DomainEventContentType,
		eventcontract.DomainHeaderMessageID:      messageID,
		eventcontract.DomainHeaderCausationID:    causationID,
		eventcontract.RuntimeHeaderTraceID:       traceID,
		eventcontract.RuntimeHeaderSchemaVersion: "1",
	})
	if err != nil {
		return lotStateOutboxMessage{}, fmt.Errorf("marshal pre-start lot state domain headers: %w", err)
	}
	return lotStateOutboxMessage{
		MessageID: messageID, CausationID: causationID, PartitionKey: lot.GetId(),
		Payload: payload, HeadersJSON: headers, CreatedAtMs: occurredAtMs,
	}, nil
}

func validLotStateTraceID(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
