package projector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protojson"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const orderPaymentWindowMs int64 = 30 * 60 * 1000

type projectionStateRow struct {
	RoomID          string
	LastEventID     sql.NullString
	LastLotVersion  int64
	CanonicalHash   string
	Frozen          bool
	LastAppliedAtMs int64
}

type lotProjectionRow struct {
	Metadata      LotMetadata
	Version       int64
	ConfigVersion int64
}

func applyRuntimeRecord(ctx context.Context, tx sqlProjectionTx, record DecodedRecord, appliedAtMs int64) (ApplyResult, error) {
	if appliedAtMs <= 0 {
		return ApplyResult{}, fmt.Errorf("%w: applied_at_ms must be positive", ErrInvalidApplyRecord)
	}
	nextOffset, err := lockPartitionOffset(ctx, tx, record)
	if err != nil {
		return ApplyResult{}, err
	}
	if record.Offset < nextOffset {
		return ApplyResult{NextOffset: nextOffset, AlreadyAdvanced: true}, nil
	}
	if record.Offset > nextOffset {
		return ApplyResult{}, fmt.Errorf("%w: record offset %d, DB next_offset %d", ErrPartitionOffsetGap, record.Offset, nextOffset)
	}

	duplicate, err := findProjectionInbox(ctx, tx, record)
	if err != nil {
		return ApplyResult{}, err
	}
	if duplicate {
		if err := advancePartitionOffset(ctx, tx, record, appliedAtMs); err != nil {
			return ApplyResult{}, err
		}
		return ApplyResult{NextOffset: record.Offset + 1, DuplicateEvent: true}, nil
	}

	fact := record.Fact
	if _, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO auction_lot_projection_state
  (lot_id, room_id, last_event_id, last_lot_version, canonical_hash, frozen, last_applied_ms)
VALUES (?, ?, NULL, ?, '', 0, 0)`, fact.GetLotId(), fact.GetRoomId(), fact.GetPrevLotVersion()); err != nil {
		return ApplyResult{}, fmt.Errorf("initialize lot projection state: %w", err)
	}
	state, err := lockProjectionState(ctx, tx, fact.GetLotId())
	if err != nil {
		return ApplyResult{}, err
	}
	if state.RoomID != fact.GetRoomId() {
		return ApplyResult{}, fmt.Errorf("%w: projection state room mismatch", ErrProjectionIdentity)
	}
	if state.Frozen {
		return ApplyResult{}, fmt.Errorf("%w: lot_id=%s", ErrProjectionLotFrozen, fact.GetLotId())
	}
	if state.LastLotVersion != fact.GetPrevLotVersion() {
		return ApplyResult{}, fmt.Errorf("%w: lot_id=%s got prev=%d want=%d", ErrRuntimeProjectionGap, fact.GetLotId(), fact.GetPrevLotVersion(), state.LastLotVersion)
	}

	lot, err := lockProjectionLot(ctx, tx, fact.GetLotId())
	if err != nil {
		return ApplyResult{}, err
	}
	if lot.Metadata.RoomID != fact.GetRoomId() || lot.Metadata.LotID != fact.GetLotId() {
		return ApplyResult{}, fmt.Errorf("%w: auction lot identity mismatch", ErrProjectionIdentity)
	}
	if lot.Version != fact.GetPrevLotVersion() {
		return ApplyResult{}, fmt.Errorf("%w: auction_lots version=%d fact prev=%d", ErrRuntimeProjectionGap, lot.Version, fact.GetPrevLotVersion())
	}
	expectedConfigVersion := fact.GetConfigVersion()
	if fact.GetCommand() == v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG {
		expectedConfigVersion--
	}
	if expectedConfigVersion <= 0 || lot.ConfigVersion != expectedConfigVersion {
		return ApplyResult{}, fmt.Errorf("%w: DB=%d fact=%d command=%s", ErrProjectionConfigVersion, lot.ConfigVersion, fact.GetConfigVersion(), fact.GetCommand())
	}

	projection, err := BuildProjection(fact, lot.Metadata)
	if err != nil {
		return ApplyResult{}, err
	}
	lotPayload, durationSeconds, antiSnipeWindowSeconds, antiSnipeExtendSeconds, err := projectLotPayload(lot.Metadata.LotPayloadJSON, fact)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := updateProjectionLot(ctx, tx, record, lotPayload, durationSeconds, antiSnipeWindowSeconds, antiSnipeExtendSeconds, lot.ConfigVersion); err != nil {
		return ApplyResult{}, err
	}
	if err := updateRoomProjection(ctx, tx, fact, lot.Metadata.MainAccountID); err != nil {
		return ApplyResult{}, err
	}
	if err := insertAcceptedBid(ctx, tx, fact, lot.Metadata.MainAccountID); err != nil {
		return ApplyResult{}, err
	}
	if err := upsertLotStats(ctx, tx, fact, lot.Metadata.MainAccountID); err != nil {
		return ApplyResult{}, err
	}
	if err := insertOrderDraft(ctx, tx, fact); err != nil {
		return ApplyResult{}, err
	}
	if err := advanceProjectionState(ctx, tx, fact, projection.CanonicalStateHash, appliedAtMs); err != nil {
		return ApplyResult{}, err
	}
	if err := insertProjectionInbox(ctx, tx, record, appliedAtMs); err != nil {
		return ApplyResult{}, err
	}
	if err := insertDomainMessages(ctx, tx, projection.DomainMessages); err != nil {
		return ApplyResult{}, err
	}
	if err := advancePartitionOffset(ctx, tx, record, appliedAtMs); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{NextOffset: record.Offset + 1}, nil
}

func lockPartitionOffset(ctx context.Context, tx sqlProjectionTx, record DecodedRecord) (int64, error) {
	var nextOffset int64
	err := tx.QueryRowContext(ctx, `
SELECT next_offset
FROM auction_projection_partition_offsets
WHERE topic = ? AND kafka_partition = ?
FOR UPDATE`, record.Topic, record.Partition).Scan(&nextOffset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: topic=%s partition=%d", ErrPartitionOffsetMissing, record.Topic, record.Partition)
	}
	if err != nil {
		return 0, fmt.Errorf("lock projector partition offset: %w", err)
	}
	if nextOffset < 0 {
		return 0, fmt.Errorf("%w: stored next_offset is negative", ErrPartitionOffsetGap)
	}
	return nextOffset, nil
}

func findProjectionInbox(ctx context.Context, tx sqlProjectionTx, record DecodedRecord) (bool, error) {
	var payloadHash string
	err := tx.QueryRowContext(ctx, `
SELECT payload_hash
FROM auction_projection_inbox
WHERE event_id = ?`, record.Fact.GetEventId()).Scan(&payloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read projector inbox: %w", err)
	}
	if payloadHash != record.PayloadHash {
		return false, fmt.Errorf("%w: event_id=%s", ErrEventIdentityConflict, record.Fact.GetEventId())
	}
	return true, nil
}

func lockProjectionState(ctx context.Context, tx sqlProjectionTx, lotID string) (projectionStateRow, error) {
	var row projectionStateRow
	err := tx.QueryRowContext(ctx, `
SELECT room_id, last_event_id, last_lot_version, canonical_hash, frozen, last_applied_ms
FROM auction_lot_projection_state
WHERE lot_id = ?
FOR UPDATE`, lotID).Scan(&row.RoomID, &row.LastEventID, &row.LastLotVersion, &row.CanonicalHash, &row.Frozen, &row.LastAppliedAtMs)
	if err != nil {
		return projectionStateRow{}, fmt.Errorf("lock lot projection state: %w", err)
	}
	return row, nil
}

func lockProjectionLot(ctx context.Context, tx sqlProjectionTx, lotID string) (lotProjectionRow, error) {
	var row lotProjectionRow
	err := tx.QueryRowContext(ctx, `
SELECT id, room_id, main_account_id, title, description, image_url, payload, version, config_version
FROM auction_lots
WHERE id = ?
FOR UPDATE`, lotID).Scan(
		&row.Metadata.LotID,
		&row.Metadata.RoomID,
		&row.Metadata.MainAccountID,
		&row.Metadata.Title,
		&row.Metadata.Description,
		&row.Metadata.ImageURL,
		&row.Metadata.LotPayloadJSON,
		&row.Version,
		&row.ConfigVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return lotProjectionRow{}, fmt.Errorf("%w: lot_id=%s", ErrProjectionLotNotFound, lotID)
	}
	if err != nil {
		return lotProjectionRow{}, fmt.Errorf("lock auction lot: %w", err)
	}
	return row, nil
}

func projectLotPayload(raw []byte, fact *v1.RuntimeFactV1) ([]byte, int32, int32, int32, error) {
	lot := new(v1.Lot)
	if len(raw) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("%w: auction lot payload is empty", ErrInvalidProjection)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, lot); err != nil {
		return nil, 0, 0, 0, fmt.Errorf("%w: decode auction lot payload: %v", ErrInvalidProjection, err)
	}
	state := fact.GetStateAfter()
	durationSeconds, err := exactSeconds(state.DurationMs, "duration_ms", true)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	windowSeconds, err := exactSeconds(state.AntiSnipeWindowMs, "anti_snipe_window_ms", false)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	extendSeconds, err := exactSeconds(state.AntiSnipeExtendMs, "anti_snipe_extend_ms", false)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	lot.Id = fact.GetLotId()
	lot.RoomId = fact.GetRoomId()
	lot.Status = state.GetStatus()
	lot.Version = fact.GetLotVersion()
	lot.ConfigVersion = fact.GetConfigVersion()
	lot.CurrentPrice = &v1.Money{Amount: state.GetCurrentPriceFen(), Currency: state.GetCurrency()}
	lot.LeadingUserId = state.GetLeadingUserId()
	lot.LeadingNickname = state.GetLeadingNickname()
	lot.WinnerUserId = state.GetWinnerUserId()
	lot.WinnerNickname = state.GetWinnerNickname()
	lot.FinalPrice = &v1.Money{Amount: state.GetFinalPriceFen(), Currency: state.GetCurrency()}
	lot.StartedAtUnixMs = state.GetStartedAtUnixMs()
	lot.EndsAtUnixMs = state.GetEndsAtUnixMs()
	lot.SettledAtUnixMs = state.GetSettledAtUnixMs()
	lot.CancelledAtUnixMs = state.GetCancelledAtUnixMs()
	lot.CancelReason = state.GetCancelReason()
	lot.UpdatedAtUnixMs = fact.GetOccurredAtUnixMs()
	lot.Stats = &v1.LotStats{BidCount: state.GetBidCount(), ParticipantCount: state.GetParticipantCount()}
	if lot.Rule == nil {
		lot.Rule = &v1.BidRule{}
	}
	lot.Rule.StartPrice = &v1.Money{Amount: state.GetStartPriceFen(), Currency: state.GetCurrency()}
	lot.Rule.MinIncrement = &v1.Money{Amount: state.GetMinIncrementFen(), Currency: state.GetCurrency()}
	lot.Rule.DurationSeconds = durationSeconds
	lot.Rule.AntiSnipeWindowSeconds = windowSeconds
	lot.Rule.AntiSnipeExtendSeconds = extendSeconds
	lot.Rule.MaxExtendCount = state.GetMaxExtendCount()
	if state.CapPriceFen != nil {
		lot.Rule.CapPrice = &v1.Money{Amount: state.GetCapPriceFen(), Currency: state.GetCurrency()}
	} else {
		lot.Rule.CapPrice = nil
	}
	if fact.GetCommand() == v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT || isTerminalLotStatus(state.GetStatus()) {
		lot.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE
		lot.QueuePosition = 0
	}
	if lot.DuelState != nil {
		lot.DuelState.ExtendCount = state.GetExtendCount()
		lot.DuelState.MaxExtendCount = state.GetMaxExtendCount()
		if isTerminalLotStatus(state.GetStatus()) {
			lot.DuelState.Active = false
		}
	}
	payload, err := (protojson.MarshalOptions{}).Marshal(lot)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("%w: encode auction lot payload: %v", ErrInvalidProjection, err)
	}
	return payload, durationSeconds, windowSeconds, extendSeconds, nil
}

func exactSeconds(value *int64, field string, positive bool) (int32, error) {
	if value == nil {
		return 0, fmt.Errorf("%w: %s is required", ErrInvalidProjection, field)
	}
	if (positive && *value <= 0) || (!positive && *value < 0) || *value%1000 != 0 || *value/1000 > math.MaxInt32 {
		return 0, fmt.Errorf("%w: %s must be an exact in-range number of seconds", ErrInvalidProjection, field)
	}
	return int32(*value / 1000), nil
}

func updateProjectionLot(ctx context.Context, tx sqlProjectionTx, record DecodedRecord, payload []byte, durationSeconds, windowSeconds, extendSeconds int32, dbConfigVersion int64) error {
	fact := record.Fact
	state := fact.GetStateAfter()
	clearQueue := fact.GetCommand() == v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT || isTerminalLotStatus(state.GetStatus())
	var capPrice any
	if state.CapPriceFen != nil {
		capPrice = state.GetCapPriceFen()
	}
	result, err := tx.ExecContext(ctx, `
UPDATE auction_lots
SET status = ?,
    queue_status = CASE WHEN ? THEN ? ELSE queue_status END,
    queue_position = CASE WHEN ? THEN 0 ELSE queue_position END,
    currency = ?, start_price_amount = ?, min_increment_amount = ?, cap_price_amount = ?,
    duration_seconds = ?, anti_snipe_window_seconds = ?, anti_snipe_extend_seconds = ?, max_extend_count = ?,
    current_price_amount = ?, leading_user_id = ?, leading_nickname = ?,
    started_at_unix_ms = ?, ends_at_unix_ms = ?, settled_at_unix_ms = ?,
    cancel_reason = ?, cancelled_at_unix_ms = ?, winner_user_id = ?, winner_nickname = ?,
    final_price_amount = ?, version = ?, config_version = ?, payload = ?, updated_at = UTC_TIMESTAMP(3)
WHERE id = ? AND version = ? AND config_version = ?`,
		state.GetStatus(), clearQueue, v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE, clearQueue,
		state.GetCurrency(), state.GetStartPriceFen(), state.GetMinIncrementFen(), capPrice,
		durationSeconds, windowSeconds, extendSeconds, state.GetMaxExtendCount(),
		state.GetCurrentPriceFen(), state.GetLeadingUserId(), state.GetLeadingNickname(),
		state.GetStartedAtUnixMs(), state.GetEndsAtUnixMs(), state.GetSettledAtUnixMs(),
		state.GetCancelReason(), state.GetCancelledAtUnixMs(), state.GetWinnerUserId(), state.GetWinnerNickname(),
		state.GetFinalPriceFen(), fact.GetLotVersion(), fact.GetConfigVersion(), payload,
		fact.GetLotId(), fact.GetPrevLotVersion(), dbConfigVersion,
	)
	return requireOneRow(result, err, "update auction lot")
}

func updateRoomProjection(ctx context.Context, tx sqlProjectionTx, fact *v1.RuntimeFactV1, mainAccountID string) error {
	state := fact.GetStateAfter()
	var (
		result sql.Result
		err    error
	)
	if isTerminalLotStatus(state.GetStatus()) {
		result, err = tx.ExecContext(ctx, `
UPDATE auction_room_states
SET active_lot_id = '', display_lot_id = ?, active_lot_version = ?, updated_at_unix_ms = ?, updated_at = UTC_TIMESTAMP(3)
WHERE room_id = ? AND main_account_id = ? AND active_lot_id = ?`,
			fact.GetLotId(), fact.GetLotVersion(), fact.GetOccurredAtUnixMs(), fact.GetRoomId(), mainAccountID, fact.GetLotId())
	} else if fact.GetCommand() == v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT {
		result, err = tx.ExecContext(ctx, `
UPDATE auction_room_states
SET active_lot_id = ?, display_lot_id = ?, active_lot_version = ?, updated_at_unix_ms = ?, updated_at = UTC_TIMESTAMP(3)
WHERE room_id = ? AND main_account_id = ? AND (active_lot_id = '' OR active_lot_id = ?)`,
			fact.GetLotId(), fact.GetLotId(), fact.GetLotVersion(), fact.GetOccurredAtUnixMs(), fact.GetRoomId(), mainAccountID, fact.GetLotId())
	} else {
		result, err = tx.ExecContext(ctx, `
UPDATE auction_room_states
SET display_lot_id = ?, active_lot_version = ?, updated_at_unix_ms = ?, updated_at = UTC_TIMESTAMP(3)
WHERE room_id = ? AND main_account_id = ? AND active_lot_id = ?`,
			fact.GetLotId(), fact.GetLotVersion(), fact.GetOccurredAtUnixMs(), fact.GetRoomId(), mainAccountID, fact.GetLotId())
	}
	return requireOneRow(result, err, "update auction room state")
}

func insertAcceptedBid(ctx context.Context, tx sqlProjectionTx, fact *v1.RuntimeFactV1, mainAccountID string) error {
	bid := fact.GetAcceptedBid()
	if bid == nil {
		return nil
	}
	payload, err := (protojson.MarshalOptions{}).Marshal(&v1.Bid{
		Id:              bid.GetBidId(),
		LotId:           fact.GetLotId(),
		UserId:          bid.GetUserId(),
		Nickname:        bid.GetNickname(),
		AvatarUrl:       bid.GetAvatarUrl(),
		Amount:          &v1.Money{Amount: bid.GetAmountFen(), Currency: fact.GetStateAfter().GetCurrency()},
		CreatedAtUnixMs: bid.GetAcceptedAtUnixMs(),
	})
	if err != nil {
		return fmt.Errorf("marshal accepted bid projection: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO auction_bids
  (id, main_account_id, lot_id, user_id, nickname, amount, idempotency_key, created_at_unix_ms, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(3))`,
		bid.GetBidId(), mainAccountID, fact.GetLotId(), bid.GetUserId(), bid.GetNickname(), bid.GetAmountFen(),
		fact.GetIdempotencyKey(), bid.GetAcceptedAtUnixMs(), payload)
	if err := requireOneRow(result, err, "insert accepted bid"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO auction_lot_participants
  (lot_id, user_id, main_account_id, room_id, first_bid_id, first_bid_at_unix_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(3))`,
		fact.GetLotId(), bid.GetUserId(), mainAccountID, fact.GetRoomId(), bid.GetBidId(), bid.GetAcceptedAtUnixMs()); err != nil {
		return fmt.Errorf("insert auction participant: %w", err)
	}
	return nil
}

func upsertLotStats(ctx context.Context, tx sqlProjectionTx, fact *v1.RuntimeFactV1, mainAccountID string) error {
	state := fact.GetStateAfter()
	lastBidID := ""
	lastBidAtMs := int64(0)
	if bid := fact.GetAcceptedBid(); bid != nil {
		lastBidID = bid.GetBidId()
		lastBidAtMs = bid.GetAcceptedAtUnixMs()
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO auction_lot_stats
  (lot_id, main_account_id, room_id, bid_count, participant_count, last_bid_id, last_bid_at_unix_ms, projected_version, updated_at_unix_ms, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE
  main_account_id = VALUES(main_account_id), room_id = VALUES(room_id),
  bid_count = VALUES(bid_count), participant_count = VALUES(participant_count),
  last_bid_id = CASE WHEN VALUES(last_bid_id) = '' THEN last_bid_id ELSE VALUES(last_bid_id) END,
  last_bid_at_unix_ms = GREATEST(last_bid_at_unix_ms, VALUES(last_bid_at_unix_ms)),
  projected_version = VALUES(projected_version), updated_at_unix_ms = VALUES(updated_at_unix_ms), updated_at = UTC_TIMESTAMP(3)`,
		fact.GetLotId(), mainAccountID, fact.GetRoomId(), state.GetBidCount(), state.GetParticipantCount(),
		lastBidID, lastBidAtMs, fact.GetLotVersion(), fact.GetOccurredAtUnixMs())
	if err != nil {
		return fmt.Errorf("upsert auction lot stats: %w", err)
	}
	return nil
}

func insertOrderDraft(ctx context.Context, tx sqlProjectionTx, fact *v1.RuntimeFactV1) error {
	draft := fact.GetOrderDraft()
	if draft == nil {
		return nil
	}
	payload, err := (protojson.MarshalOptions{}).Marshal(draft)
	if err != nil {
		return fmt.Errorf("marshal order draft projection: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO user_orders
  (id, source, source_order_id, order_no, main_account_id, user_id, nickname, status, payment_status,
   title, total_amount, currency, created_at_unix_ms, updated_at_unix_ms, expires_at_unix_ms, version,
   source_payload, created_at, updated_at)
VALUES (?, 'auction', ?, ?, ?, ?, ?, 'pending_payment', 'init', ?, ?, ?, ?, ?, ?, 1, ?, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`,
		draft.GetOrderId(), draft.GetOrderId(), draft.GetOrderId(), draft.GetMainAccountId(), draft.GetBuyerUserId(),
		draft.GetBuyerNickname(), draft.GetTitle(), draft.GetTotalAmountFen(), draft.GetCurrency(),
		draft.GetCreatedAtUnixMs(), draft.GetCreatedAtUnixMs(), draft.GetCreatedAtUnixMs()+orderPaymentWindowMs, payload)
	if err := requireOneRow(result, err, "insert auction order"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
INSERT INTO user_order_items
  (id, order_id, source, source_item_id, lot_id, room_id, title, image_url, sku_name, quantity,
   unit_amount, total_amount, currency, created_at, updated_at)
VALUES (?, ?, 'auction', ?, ?, ?, ?, ?, '竞拍拍品', 1, ?, ?, ?, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`,
		"auction_item_"+draft.GetOrderId(), draft.GetOrderId(), fact.GetLotId(), fact.GetLotId(), fact.GetRoomId(),
		draft.GetTitle(), draft.GetImageUrl(), draft.GetTotalAmountFen(), draft.GetTotalAmountFen(), draft.GetCurrency())
	return requireOneRow(result, err, "insert auction order item")
}

func advanceProjectionState(ctx context.Context, tx sqlProjectionTx, fact *v1.RuntimeFactV1, canonicalHash string, appliedAtMs int64) error {
	result, err := tx.ExecContext(ctx, `
UPDATE auction_lot_projection_state
SET last_event_id = ?, last_lot_version = ?, canonical_hash = ?, last_applied_ms = ?
WHERE lot_id = ? AND room_id = ? AND last_lot_version = ? AND frozen = 0`,
		fact.GetEventId(), fact.GetLotVersion(), canonicalHash, appliedAtMs,
		fact.GetLotId(), fact.GetRoomId(), fact.GetPrevLotVersion())
	return requireOneRow(result, err, "advance lot projection state")
}

func insertProjectionInbox(ctx context.Context, tx sqlProjectionTx, record DecodedRecord, appliedAtMs int64) error {
	result, err := tx.ExecContext(ctx, `
INSERT INTO auction_projection_inbox
  (event_id, topic, kafka_partition, kafka_offset, lot_id, lot_version, payload_hash, applied_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.Fact.GetEventId(), record.Topic, record.Partition, record.Offset, record.Fact.GetLotId(),
		record.Fact.GetLotVersion(), record.PayloadHash, appliedAtMs)
	return requireOneRow(result, err, "insert projector inbox")
}

func insertDomainMessages(ctx context.Context, tx sqlProjectionTx, messages []DomainMessage) error {
	for _, message := range messages {
		if !json.Valid(message.HeadersJSON) {
			return fmt.Errorf("%w: domain message headers are not valid JSON", ErrInvalidProjection)
		}
		result, err := tx.ExecContext(ctx, `
INSERT INTO auction_domain_outbox
  (message_id, causation_id, topic, partition_key, payload, headers_json, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			message.MessageID, message.CausationID, message.Topic, message.PartitionKey,
			message.Payload, message.HeadersJSON, message.CreatedAtMs)
		if err := requireOneRow(result, err, "insert domain outbox message"); err != nil {
			return err
		}
	}
	return nil
}

func advancePartitionOffset(ctx context.Context, tx sqlProjectionTx, record DecodedRecord, appliedAtMs int64) error {
	result, err := tx.ExecContext(ctx, `
UPDATE auction_projection_partition_offsets
SET next_offset = ?, updated_at_ms = ?
WHERE topic = ? AND kafka_partition = ? AND next_offset = ?`,
		record.Offset+1, appliedAtMs, record.Topic, record.Partition, record.Offset)
	return requireOneRow(result, err, "advance projector partition offset")
}

func requireOneRow(result sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if result == nil {
		return fmt.Errorf("%w: %s returned no SQL result", ErrProjectionCAS, operation)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: %s affected %d rows", ErrProjectionCAS, operation, rows)
	}
	return nil
}

func isTerminalLotStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}
