package searchreconcile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type findingDatabase interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type SQLFindingStore struct {
	db    findingDatabase
	nowMs func() int64
}

func NewSQLFindingStore(db *sql.DB) (*SQLFindingStore, error) {
	if db == nil {
		return nil, errors.New("search reconciliation finding database is required")
	}
	return &SQLFindingStore{db: db, nowMs: func() int64 { return time.Now().UnixMilli() }}, nil
}

func (store *SQLFindingStore) Record(ctx context.Context, finding Finding) error {
	if store == nil || store.db == nil || store.nowMs == nil || finding.LotID == "" || finding.Expected.LotVersion <= 0 ||
		(finding.Result != ResultConflict && finding.Result != ResultAhead) ||
		(finding.Sink != SinkElasticsearch && finding.Sink != SinkPGVector) {
		return errors.New("search reconciliation finding is invalid")
	}
	kind := findingKind(finding.Sink, finding.Result)
	detectedAtMs := store.nowMs()
	detail, err := json.Marshal(map[string]any{
		"sink": finding.Sink, "result": finding.Result,
		"expected_version": finding.Expected.LotVersion, "expected_event_id": finding.Expected.LastEventID, "expected_content_hash": finding.Expected.ContentHash,
		"actual_found": finding.Actual.Found, "actual_version": finding.Actual.LotVersion,
		"actual_event_id": finding.Actual.LastEventID, "actual_content_hash": finding.Actual.ContentHash,
	})
	if err != nil {
		return fmt.Errorf("encode search reconciliation finding: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `
INSERT INTO auction_reconcile_findings
  (kind, lot_id, severity, detail_json, detected_at_ms)
SELECT ?, ?, 'P0', ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM auction_reconcile_findings
  WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0
)`, kind, finding.LotID, detail, detectedAtMs, kind, finding.LotID)
	if err != nil {
		return fmt.Errorf("insert search reconciliation finding: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read search reconciliation finding result: %w", err)
	}
	if rows == 1 {
		return nil
	}
	if rows != 0 {
		return fmt.Errorf("insert search reconciliation finding affected %d rows", rows)
	}
	result, err = store.db.ExecContext(ctx, `
UPDATE auction_reconcile_findings
SET severity = 'P0', detail_json = ?, detected_at_ms = ?
WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0`, detail, detectedAtMs, kind, finding.LotID)
	if err != nil {
		return fmt.Errorf("refresh search reconciliation finding: %w", err)
	}
	rows, err = result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("refresh search reconciliation finding affected %d rows: %v", rows, err)
	}
	return nil
}

func findingKind(sink, result string) string {
	sink = strings.ToUpper(strings.TrimSpace(sink))
	switch sink {
	case "ELASTICSEARCH":
		sink = "ES"
	case "PGVECTOR":
		sink = "VECTOR"
	}
	return "SEARCH_" + sink + "_" + strings.ToUpper(strings.TrimSpace(result))
}
