package projector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	FindingEventIdentityConflict = "EVENT_IDENTITY_CONFLICT"
	FindingRuntimeVersionGap     = "RUNTIME_VERSION_GAP"
	FindingProjectionConflict    = "PROJECTION_CONFLICT"

	FindingSeverityP0 = "P0"
	FindingSeverityP1 = "P1"
)

// RecordFinding persists a pause reason independently from the failed projection transaction.
// When freeze is true, the lot is fenced before the finding is committed; the Kafka offset is never advanced.
func (s *SQLStore) RecordFinding(ctx context.Context, record DecodedRecord, kind, severity string, freeze bool, cause error) error {
	if s == nil || s.db == nil {
		return errors.New("projector database is required")
	}
	if err := validateApplyRecord(record); err != nil {
		return err
	}
	if !validFinding(kind, severity) || cause == nil {
		return errors.New("projector finding requires a valid kind, severity, and cause")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin projector finding transaction: %w", err)
	}
	if err := recordProjectionFinding(ctx, sqlTxAdapter{Tx: tx}, record, kind, severity, freeze, cause, s.nowMs()); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback projector finding transaction: %w", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit projector finding transaction: %w", err)
	}
	return nil
}

func recordProjectionFinding(ctx context.Context, tx sqlProjectionTx, record DecodedRecord, kind, severity string, freeze bool, cause error, detectedAtMs int64) error {
	if !validFinding(kind, severity) || cause == nil || detectedAtMs <= 0 {
		return errors.New("invalid projector finding")
	}
	if freeze {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO auction_lot_projection_state
  (lot_id, room_id, last_event_id, last_lot_version, canonical_hash, frozen, last_applied_ms)
VALUES (?, ?, NULL, 0, '', 1, 0)
ON DUPLICATE KEY UPDATE frozen = 1`, record.Fact.GetLotId(), record.Fact.GetRoomId()); err != nil {
			return fmt.Errorf("freeze conflicted projection lot: %w", err)
		}
	}
	detail, err := json.Marshal(map[string]any{
		"event_id":         record.Fact.GetEventId(),
		"topic":            record.Topic,
		"partition":        record.Partition,
		"offset":           record.Offset,
		"prev_lot_version": record.Fact.GetPrevLotVersion(),
		"lot_version":      record.Fact.GetLotVersion(),
		"payload_hash":     record.PayloadHash,
		"error":            truncateFindingText(cause.Error(), 512),
	})
	if err != nil {
		return fmt.Errorf("encode projector finding: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO auction_reconcile_findings
  (kind, lot_id, severity, detail_json, detected_at_ms)
VALUES (?, ?, ?, ?, ?)`, kind, record.Fact.GetLotId(), severity, detail, detectedAtMs)
	return requireOneRow(result, err, "insert projector reconcile finding")
}

func validFinding(kind, severity string) bool {
	if strings.TrimSpace(kind) != kind || kind == "" || len(kind) > 32 {
		return false
	}
	return severity == FindingSeverityP0 || severity == FindingSeverityP1
}

func truncateFindingText(value string, limit int) string {
	value = strings.Map(func(char rune) rune {
		if char == '\r' || char == '\n' || char == '\x00' {
			return ' '
		}
		return char
	}, value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
