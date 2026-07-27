package esindex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

const ElasticsearchVersionConflictFinding = "SEARCH_INDEX_CONFLICT"

type findingDatabase interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type SQLFindingStore struct {
	db    findingDatabase
	nowMs func() int64
}

func NewSQLFindingStore(db *sql.DB) (*SQLFindingStore, error) {
	if db == nil {
		return nil, errors.New("elasticsearch finding database is required")
	}
	return newSQLFindingStore(db)
}

func newSQLFindingStore(db findingDatabase) (*SQLFindingStore, error) {
	if db == nil {
		return nil, errors.New("elasticsearch finding database is required")
	}
	return &SQLFindingStore{db: db, nowMs: func() int64 { return time.Now().UnixMilli() }}, nil
}

// RecordIdentityConflict durably records the P0 before the Kafka message may be acknowledged.
// One unresolved row per lot/kind is retained; retries refresh its evidence instead of multiplying rows.
func (store *SQLFindingStore) RecordIdentityConflict(ctx context.Context, record searchstate.Record, cause error) error {
	if store == nil || store.db == nil || store.nowMs == nil || cause == nil || record.Document.LotID == "" ||
		record.Document.LotVersion <= 0 || record.MessageID == "" {
		return errors.New("elasticsearch identity finding requires a valid record and cause")
	}
	detectedAtMs := store.nowMs()
	if detectedAtMs <= 0 {
		return errors.New("elasticsearch identity finding time is invalid")
	}
	detail, err := json.Marshal(map[string]any{
		"consumer": "index-es", "topic": record.Topic, "partition": record.Partition, "offset": record.Offset,
		"message_id": record.MessageID, "event_id": record.LastEventID(), "lot_version": record.Document.LotVersion,
		"content_hash": record.Document.ContentHash, "error": truncateFindingText(cause.Error(), 512),
	})
	if err != nil {
		return fmt.Errorf("encode Elasticsearch identity finding: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `
INSERT INTO auction_reconcile_findings
  (kind, lot_id, severity, detail_json, detected_at_ms)
SELECT ?, ?, 'P0', ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM auction_reconcile_findings
  WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0
)`, ElasticsearchVersionConflictFinding, record.Document.LotID, detail, detectedAtMs,
		ElasticsearchVersionConflictFinding, record.Document.LotID)
	if err != nil {
		return fmt.Errorf("insert Elasticsearch identity finding: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Elasticsearch identity finding result: %w", err)
	}
	if rows == 1 {
		return nil
	}
	if rows != 0 {
		return fmt.Errorf("insert Elasticsearch identity finding affected %d rows", rows)
	}
	result, err = store.db.ExecContext(ctx, `
UPDATE auction_reconcile_findings
SET severity = 'P0', detail_json = ?, detected_at_ms = ?
WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0`, detail, detectedAtMs,
		ElasticsearchVersionConflictFinding, record.Document.LotID)
	if err != nil {
		return fmt.Errorf("refresh Elasticsearch identity finding: %w", err)
	}
	rows, err = result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("refresh Elasticsearch identity finding affected %d rows: %v", rows, err)
	}
	return nil
}

func truncateFindingText(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\x00' {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return string(runes)
}
