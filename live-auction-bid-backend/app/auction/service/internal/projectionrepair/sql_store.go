package projectionrepair

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

type SQLStore struct {
	db    *sql.DB
	nowMs func() int64
}

func NewSQLStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("projection repair database is required")
	}
	return &SQLStore{db: db, nowMs: func() int64 { return time.Now().UnixMilli() }}, nil
}

func (store *SQLStore) ReadPartitionOffset(ctx context.Context, topic string, partition int32) (PartitionOffset, error) {
	if store == nil || store.db == nil {
		return PartitionOffset{}, errors.New("projection repair database is required")
	}
	if topic != eventcontract.RuntimeProjectionTopicV1 || partition < 0 {
		return PartitionOffset{}, errors.New("projection repair topic or partition is invalid")
	}
	var result PartitionOffset
	err := store.db.QueryRowContext(ctx, `
SELECT next_offset, updated_at_ms
FROM auction_projection_partition_offsets
WHERE topic = ? AND kafka_partition = ?`, topic, partition).Scan(&result.NextOffset, &result.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return PartitionOffset{}, nil
	}
	if err != nil {
		return PartitionOffset{}, fmt.Errorf("read projection partition offset: %w", err)
	}
	if result.NextOffset < 0 || result.UpdatedAtMs <= 0 {
		return PartitionOffset{}, errors.New("projection partition offset row is invalid")
	}
	result.Found = true
	return result, nil
}

func (store *SQLStore) ReadInboxRange(ctx context.Context, topic string, partition int32, start, end int64) ([]InboxEntry, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("projection repair database is required")
	}
	if topic != eventcontract.RuntimeProjectionTopicV1 || partition < 0 || start < 0 || end < start || end-start > MaxReplayRecords+1 {
		return nil, errors.New("projection repair inbox range is invalid")
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT event_id, kafka_offset, lot_id, lot_version, payload_hash, applied_at_ms
FROM auction_projection_inbox
WHERE topic = ? AND kafka_partition = ? AND kafka_offset >= ? AND kafka_offset < ?
ORDER BY kafka_offset`, topic, partition, start, end)
	if err != nil {
		return nil, fmt.Errorf("query projection inbox range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]InboxEntry, 0)
	for rows.Next() {
		var item InboxEntry
		if err := rows.Scan(&item.EventID, &item.Offset, &item.LotID, &item.LotVersion, &item.PayloadHash, &item.AppliedAtMs); err != nil {
			return nil, fmt.Errorf("scan projection inbox range: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projection inbox range: %w", err)
	}
	return result, nil
}

func (store *SQLStore) ReadInboxByEventIDs(ctx context.Context, eventIDs []string) (map[string]InboxEntry, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("projection repair database is required")
	}
	unique, err := normalizedEventIDs(eventIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]InboxEntry, len(unique))
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	arguments := make([]any, len(unique))
	for index, eventID := range unique {
		arguments[index] = eventID
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT event_id, kafka_offset, lot_id, lot_version, payload_hash, applied_at_ms
FROM auction_projection_inbox
WHERE event_id IN (`+placeholders+`)
ORDER BY event_id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query projection inbox event identities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item InboxEntry
		if err := rows.Scan(&item.EventID, &item.Offset, &item.LotID, &item.LotVersion, &item.PayloadHash, &item.AppliedAtMs); err != nil {
			return nil, fmt.Errorf("scan projection inbox event identity: %w", err)
		}
		if _, duplicate := result[item.EventID]; duplicate {
			return nil, fmt.Errorf("projection inbox duplicated event_id %s", item.EventID)
		}
		result[item.EventID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projection inbox event identities: %w", err)
	}
	return result, nil
}

func (store *SQLStore) ReadLotStates(ctx context.Context, lotIDs []string) (map[string]LotState, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("projection repair database is required")
	}
	unique, err := normalizedLotIDs(lotIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]LotState, len(unique))
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	arguments := make([]any, len(unique))
	for index, lotID := range unique {
		arguments[index] = lotID
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT lot.id, state.lot_id IS NOT NULL, COALESCE(state.last_event_id, ''),
       COALESCE(state.last_lot_version, 0), COALESCE(state.canonical_hash, ''),
       COALESCE(state.frozen, 0), COALESCE(state.last_applied_ms, 0), lot.version
FROM auction_lots AS lot
LEFT JOIN auction_lot_projection_state AS state ON state.lot_id = lot.id
WHERE lot.id IN (`+placeholders+`)
ORDER BY lot.id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query projection lot states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item LotState
		if err := rows.Scan(
			&item.LotID, &item.ProjectionStateFound, &item.LastEventID, &item.LastLotVersion, &item.CanonicalHash,
			&item.Frozen, &item.LastAppliedAtMs, &item.LotVersion,
		); err != nil {
			return nil, fmt.Errorf("scan projection lot state: %w", err)
		}
		if _, duplicate := result[item.LotID]; duplicate {
			return nil, fmt.Errorf("projection lot state duplicated lot_id %s", item.LotID)
		}
		result[item.LotID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projection lot states: %w", err)
	}
	return result, nil
}

func (store *SQLStore) BeginReplayAudit(
	ctx context.Context,
	repairID string,
	request ReplayRequest,
	toOffsetExclusive int64,
	detail any,
) error {
	if store == nil || store.db == nil {
		return errors.New("projection repair database is required")
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode projection repair audit detail: %w", err)
	}
	nowMs := store.nowMs()
	result, err := store.db.ExecContext(ctx, `
INSERT INTO auction_projection_repair_audit
  (repair_id, repair_type, topic, kafka_partition, from_offset, to_offset_exclusive,
   operator_id, repair_reason, status, detail_json, created_at_ms, completed_at_ms)
VALUES (?, 'ORIGINAL_REPLAY', ?, ?, ?, ?, ?, ?, 'STARTED', ?, ?, 0)`,
		repairID, eventcontract.RuntimeProjectionTopicV1, request.Partition, request.ExpectedNextOffset,
		toOffsetExclusive, request.Operator, request.Reason, detailJSON, nowMs)
	if err != nil {
		return fmt.Errorf("insert projection repair audit: %w", err)
	}
	return requireOneRow(result, "insert projection repair audit")
}

func (store *SQLStore) ReadSyntheticAuditHistory(
	ctx context.Context,
	metadata SyntheticBundleMetadata,
) (SyntheticAuditHistory, error) {
	if store == nil || store.db == nil {
		return SyntheticAuditHistory{}, errors.New("projection repair database is required")
	}
	if metadata.Topic != eventcontract.RuntimeProjectionTopicV1 || metadata.Partition < 0 ||
		metadata.FromOffset < 0 || metadata.ToOffsetExclusive <= metadata.FromOffset || !validLowerHexDigest(metadata.BundleSHA256) {
		return SyntheticAuditHistory{}, errors.New("synthetic repair audit identity is invalid")
	}
	var history SyntheticAuditHistory
	err := store.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(status = 'STARTED'), 0),
       COALESCE(SUM(status = 'FAILED'), 0),
       COALESCE(SUM(status = 'SUCCEEDED'), 0)
FROM auction_projection_repair_audit
WHERE repair_type = 'SYNTHETIC_REPLAY'
  AND topic = ? AND kafka_partition = ?
  AND from_offset = ? AND to_offset_exclusive = ?
  AND bundle_sha256 = ?`,
		metadata.Topic, metadata.Partition, metadata.FromOffset, metadata.ToOffsetExclusive, metadata.BundleSHA256,
	).Scan(&history.Started, &history.Failed, &history.Succeeded)
	if err != nil {
		return SyntheticAuditHistory{}, fmt.Errorf("read synthetic repair audit history: %w", err)
	}
	if history.Started < 0 || history.Failed < 0 || history.Succeeded < 0 {
		return SyntheticAuditHistory{}, errors.New("synthetic repair audit history is invalid")
	}
	return history, nil
}

func (store *SQLStore) BeginSyntheticAudit(
	ctx context.Context,
	repairID string,
	request SyntheticRequest,
	metadata SyntheticBundleMetadata,
	detail any,
) (int, error) {
	if store == nil || store.db == nil {
		return 0, errors.New("projection repair database is required")
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return 0, fmt.Errorf("encode synthetic repair audit detail: %w", err)
	}
	nowMs := store.nowMs()
	interruptedJSON, err := json.Marshal(map[string]any{
		"interrupted_by": repairID,
		"reason":         "superseded by verified same-bundle resume",
	})
	if err != nil {
		return 0, fmt.Errorf("encode interrupted synthetic repair detail: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin synthetic repair audit: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	interruptedResult, err := tx.ExecContext(ctx, `
UPDATE auction_projection_repair_audit
SET status = 'FAILED', detail_json = ?, completed_at_ms = ?
WHERE repair_type = 'SYNTHETIC_REPLAY' AND status = 'STARTED'
  AND topic = ? AND kafka_partition = ?
  AND from_offset = ? AND to_offset_exclusive = ?
  AND bundle_sha256 = ?`,
		interruptedJSON, nowMs, metadata.Topic, metadata.Partition,
		metadata.FromOffset, metadata.ToOffsetExclusive, metadata.BundleSHA256)
	if err != nil {
		return 0, fmt.Errorf("terminalize interrupted synthetic repair audits: %w", err)
	}
	interruptedRows, err := interruptedResult.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count interrupted synthetic repair audits: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO auction_projection_repair_audit
  (repair_id, repair_type, topic, kafka_partition, from_offset, to_offset_exclusive,
   operator_id, repair_reason, bundle_sha256, prepared_by, change_ticket, record_count,
   status, detail_json, created_at_ms, completed_at_ms)
VALUES (?, 'SYNTHETIC_REPLAY', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'STARTED', ?, ?, 0)`,
		repairID, metadata.Topic, metadata.Partition, metadata.FromOffset, metadata.ToOffsetExclusive,
		request.ExecutedBy, metadata.RepairReason, metadata.BundleSHA256, metadata.PreparedBy,
		metadata.ChangeTicket, metadata.RecordCount, detailJSON, nowMs)
	if err != nil {
		return 0, fmt.Errorf("insert synthetic repair audit: %w", err)
	}
	if err := requireOneRow(result, "insert synthetic repair audit"); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit synthetic repair audit: %w", err)
	}
	committed = true
	return int(interruptedRows), nil
}

func (store *SQLStore) FinishReplayAudit(
	ctx context.Context,
	repairID string,
	succeeded bool,
	detail any,
	resolvedLotIDs []string,
) error {
	return store.finishAudit(ctx, repairID, succeeded, detail, resolvedLotIDs, "original_runtime_topic_replay_verified")
}

func (store *SQLStore) FinishSyntheticAudit(
	ctx context.Context,
	repairID string,
	succeeded bool,
	detail any,
	resolvedLotIDs []string,
) error {
	return store.finishAudit(ctx, repairID, succeeded, detail, resolvedLotIDs, "synthetic_runtime_fact_repair_verified")
}

func (store *SQLStore) finishAudit(
	ctx context.Context,
	repairID string,
	succeeded bool,
	detail any,
	resolvedLotIDs []string,
	resolution string,
) error {
	if store == nil || store.db == nil {
		return errors.New("projection repair database is required")
	}
	if resolution != "original_runtime_topic_replay_verified" && resolution != "synthetic_runtime_fact_repair_verified" {
		return errors.New("projection repair resolution is invalid")
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode projection repair completion detail: %w", err)
	}
	status := "FAILED"
	if succeeded {
		status = "SUCCEEDED"
	}
	nowMs := store.nowMs()
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin projection repair completion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, `
UPDATE auction_projection_repair_audit
SET status = ?, detail_json = ?, completed_at_ms = ?
WHERE repair_id = ? AND status = 'STARTED'`, status, detailJSON, nowMs, repairID)
	if err != nil {
		return fmt.Errorf("complete projection repair audit: %w", err)
	}
	if err := requireOneRow(result, "complete projection repair audit"); err != nil {
		return err
	}
	if succeeded {
		lotIDs, err := normalizedLotIDs(resolvedLotIDs)
		if err != nil {
			return err
		}
		if len(lotIDs) > 0 {
			resolutionJSON, err := json.Marshal(map[string]any{
				"repair_id":  repairID,
				"resolution": resolution,
			})
			if err != nil {
				return fmt.Errorf("encode projection finding resolution: %w", err)
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(lotIDs)), ",")
			arguments := []any{repairID, nowMs, resolutionJSON, projector.FindingRuntimeVersionGap}
			for _, lotID := range lotIDs {
				arguments = append(arguments, lotID)
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE auction_reconcile_findings AS finding
JOIN auction_projection_repair_audit AS audit ON audit.repair_id = ?
SET finding.resolved_at_ms = ?, finding.resolution_json = ?
WHERE finding.kind = ? AND finding.resolved_at_ms = 0 AND finding.lot_id IN (`+placeholders+`)
  AND CAST(JSON_UNQUOTE(JSON_EXTRACT(finding.detail_json, '$.partition')) AS SIGNED) = audit.kafka_partition
  AND CAST(JSON_UNQUOTE(JSON_EXTRACT(finding.detail_json, '$.offset')) AS SIGNED) >= audit.from_offset
  AND CAST(JSON_UNQUOTE(JSON_EXTRACT(finding.detail_json, '$.offset')) AS SIGNED) < audit.to_offset_exclusive`, arguments...); err != nil {
				return fmt.Errorf("resolve projection gap findings: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit projection repair completion: %w", err)
	}
	committed = true
	return nil
}

func normalizedLotIDs(values []string) ([]string, error) {
	if len(values) > int(MaxReplayRecords) {
		return nil, errors.New("projection repair lot set exceeds safety limit")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("projection repair lot_id is invalid")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizedEventIDs(values []string) ([]string, error) {
	if len(values) > int(MaxReplayRecords) {
		return nil, errors.New("projection repair event set exceeds safety limit")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !validAuditText(value, 64, false) {
			return nil, errors.New("projection repair event_id is invalid")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func requireOneRow(result sql.Result, action string) error {
	if result == nil {
		return fmt.Errorf("%s returned no result", action)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", action, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s affected %d rows", action, rows)
	}
	return nil
}

var _ interface {
	ReadPartitionOffset(context.Context, string, int32) (PartitionOffset, error)
	ReadInboxRange(context.Context, string, int32, int64, int64) ([]InboxEntry, error)
	ReadInboxByEventIDs(context.Context, []string) (map[string]InboxEntry, error)
	ReadLotStates(context.Context, []string) (map[string]LotState, error)
	BeginReplayAudit(context.Context, string, ReplayRequest, int64, any) error
	FinishReplayAudit(context.Context, string, bool, any, []string) error
	ReadSyntheticAuditHistory(context.Context, SyntheticBundleMetadata) (SyntheticAuditHistory, error)
	BeginSyntheticAudit(context.Context, string, SyntheticRequest, SyntheticBundleMetadata, any) (int, error)
	FinishSyntheticAudit(context.Context, string, bool, any, []string) error
} = (*SQLStore)(nil)
