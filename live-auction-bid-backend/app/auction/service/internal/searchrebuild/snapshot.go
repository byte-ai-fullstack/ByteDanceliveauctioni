package searchrebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

var (
	ErrSnapshotClosed          = errors.New("search rebuild snapshot is closed")
	ErrSnapshotIdentityMissing = errors.New("search rebuild source event is missing")
	ErrSnapshotIdentityFork    = errors.New("search rebuild source event conflicts with MySQL")
)

type SnapshotPage struct {
	Documents    []searchindex.LotDocument
	Records      []SnapshotRecord
	SkippedDraft int
	LastLotID    string
	Done         bool
}

type SnapshotRecord struct {
	Document searchindex.LotDocument
	Payload  []byte
}

// Snapshot owns one repeatable-read transaction for the complete keyset scan.
// Keeping this explicit prevents callers from accidentally mixing pages from
// different database states while Kafka continues to receive new versions.
type Snapshot struct {
	tx        *sql.Tx
	lastLotID string
	closed    bool
}

func BeginSnapshot(ctx context.Context, db *sql.DB) (*Snapshot, error) {
	return BeginSnapshotAfter(ctx, db, "")
}

func BeginSnapshotAfter(ctx context.Context, db *sql.DB, afterLotID string) (*Snapshot, error) {
	if db == nil {
		return nil, errors.New("search rebuild MySQL database is required")
	}
	afterLotID = strings.TrimSpace(afterLotID)
	if len(afterLotID) > 64 || strings.ContainsAny(afterLotID, "\r\n\x00") {
		return nil, errors.New("search rebuild snapshot cursor is invalid")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin search rebuild snapshot: %w", err)
	}
	return &Snapshot{tx: tx, lastLotID: afterLotID}, nil
}

func (snapshot *Snapshot) Next(ctx context.Context, limit int) (SnapshotPage, error) {
	if snapshot == nil || snapshot.tx == nil || snapshot.closed {
		return SnapshotPage{}, ErrSnapshotClosed
	}
	if limit <= 0 || limit > 1000 {
		return SnapshotPage{}, errors.New("search rebuild page size must be within [1,1000]")
	}
	rows, err := snapshot.tx.QueryContext(ctx, `
SELECT l.id, l.room_id, l.main_account_id, l.version, l.status,
       l.title, l.description, l.image_url, l.currency,
       l.start_price_amount, l.current_price_amount,
       l.started_at_unix_ms, l.ends_at_unix_ms, l.payload,
       latest.payload
FROM auction_lots AS l
LEFT JOIN auction_domain_outbox AS latest
  ON latest.id = (
    SELECT candidate.id
    FROM auction_domain_outbox AS candidate
    WHERE candidate.topic = ? AND candidate.partition_key = l.id
    ORDER BY candidate.id DESC
    LIMIT 1
  )
WHERE l.id > ?
ORDER BY l.id
LIMIT ?`, eventcontract.LotStateTopicV1, snapshot.lastLotID, limit)
	if err != nil {
		return SnapshotPage{}, fmt.Errorf("query search rebuild snapshot page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := SnapshotPage{
		Documents: make([]searchindex.LotDocument, 0, limit),
		Records:   make([]SnapshotRecord, 0, limit),
	}
	rowCount := 0
	for rows.Next() {
		rowCount++
		var row snapshotLotRow
		if err := rows.Scan(
			&row.LotID, &row.RoomID, &row.MainAccountID, &row.LotVersion, &row.Status,
			&row.Title, &row.Description, &row.ImageURL, &row.Currency,
			&row.StartPriceFen, &row.CurrentPriceFen, &row.StartsAtUnixMs, &row.EndsAtUnixMs,
			&row.LotPayload, &row.DomainPayload,
		); err != nil {
			return SnapshotPage{}, fmt.Errorf("scan search rebuild snapshot row: %w", err)
		}
		if strings.TrimSpace(row.LotID) == "" {
			return SnapshotPage{}, fmt.Errorf("%w: MySQL returned an empty lot_id", ErrSnapshotIdentityFork)
		}
		snapshot.lastLotID = row.LotID
		page.LastLotID = row.LotID
		document, skipped, err := decodeSnapshotLotRow(row)
		if err != nil {
			return SnapshotPage{}, err
		}
		if skipped {
			page.SkippedDraft++
			continue
		}
		page.Documents = append(page.Documents, document)
		page.Records = append(page.Records, SnapshotRecord{Document: document, Payload: append([]byte(nil), row.DomainPayload...)})
	}
	if err := rows.Err(); err != nil {
		return SnapshotPage{}, fmt.Errorf("iterate search rebuild snapshot page: %w", err)
	}
	page.Done = rowCount < limit
	return page, nil
}

func (snapshot *Snapshot) Commit() error {
	if snapshot == nil || snapshot.tx == nil || snapshot.closed {
		return ErrSnapshotClosed
	}
	snapshot.closed = true
	if err := snapshot.tx.Commit(); err != nil {
		return fmt.Errorf("commit search rebuild read snapshot: %w", err)
	}
	return nil
}

func (snapshot *Snapshot) Close() error {
	if snapshot == nil || snapshot.tx == nil || snapshot.closed {
		return nil
	}
	snapshot.closed = true
	if err := snapshot.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback search rebuild snapshot: %w", err)
	}
	return nil
}

type snapshotLotRow struct {
	LotID           string
	RoomID          string
	MainAccountID   string
	LotVersion      int64
	Status          int32
	Title           string
	Description     string
	ImageURL        string
	Currency        string
	StartPriceFen   int64
	CurrentPriceFen int64
	StartsAtUnixMs  int64
	EndsAtUnixMs    int64
	LotPayload      []byte
	DomainPayload   []byte
}

func decodeSnapshotLotRow(row snapshotLotRow) (searchindex.LotDocument, bool, error) {
	status := v1.LotStatus(row.Status)
	if len(row.DomainPayload) == 0 {
		if publicSearchStatus(status) {
			return searchindex.LotDocument{}, false, fmt.Errorf("%w: lot_id=%s version=%d", ErrSnapshotIdentityMissing, row.LotID, row.LotVersion)
		}
		return searchindex.LotDocument{}, true, nil
	}
	event := new(v1.LotStateDomainEventV1)
	if err := proto.Unmarshal(row.DomainPayload, event); err != nil {
		return searchindex.LotDocument{}, false, fmt.Errorf("%w: decode lot_id=%s domain payload: %v", ErrSnapshotIdentityFork, row.LotID, err)
	}
	if err := validateSnapshotMetadata(event); err != nil {
		return searchindex.LotDocument{}, false, fmt.Errorf("%w: lot_id=%s metadata: %v", ErrSnapshotIdentityFork, row.LotID, err)
	}
	if event.GetLotVersion() != row.LotVersion {
		if !publicSearchStatus(status) && event.GetLotVersion() < row.LotVersion {
			return searchindex.LotDocument{}, true, nil
		}
		return searchindex.LotDocument{}, false, fmt.Errorf(
			"%w: lot_id=%s MySQL version=%d event version=%d", ErrSnapshotIdentityFork, row.LotID, row.LotVersion, event.GetLotVersion(),
		)
	}
	lot := new(v1.Lot)
	if len(row.LotPayload) == 0 || protojson.Unmarshal(row.LotPayload, lot) != nil {
		return searchindex.LotDocument{}, false, fmt.Errorf("%w: lot_id=%s payload is invalid", ErrSnapshotIdentityFork, row.LotID)
	}
	expected := &v1.LotStateDomainEventV1{
		LotId: row.LotID, RoomId: row.RoomID, MainAccountId: row.MainAccountID, LotVersion: row.LotVersion,
		Status: status, Title: row.Title, Description: row.Description, Category: lot.GetCategory(),
		Tags: append([]string(nil), lot.GetTags()...), ImageUrl: row.ImageURL,
		StartPriceFen: row.StartPriceFen, CurrentPriceFen: row.CurrentPriceFen, Currency: row.Currency,
		StartsAtUnixMs: row.StartsAtUnixMs, EndsAtUnixMs: row.EndsAtUnixMs,
	}
	expectedHash, err := eventcontract.LotStateContentHash(expected)
	if err != nil || event.GetContentHash() != expectedHash || eventcontract.ValidateLotStateDomainEvent(event) != nil {
		return searchindex.LotDocument{}, false, fmt.Errorf("%w: lot_id=%s version=%d content does not match MySQL", ErrSnapshotIdentityFork, row.LotID, row.LotVersion)
	}
	document := searchindex.LotDocumentFromDomainEvent(event)
	return document, false, nil
}

func validateSnapshotMetadata(event *v1.LotStateDomainEventV1) error {
	if event == nil || event.GetMetadata() == nil {
		return errors.New("metadata is required")
	}
	metadata := event.GetMetadata()
	if eventcontract.ValidateEventID(metadata.GetCausationId()) != nil || metadata.GetSchemaVersion() != 1 ||
		metadata.GetOccurredAtUnixMs() <= 0 || !validSnapshotText(metadata.GetTraceId(), 128) {
		return errors.New("causation, trace, schema, or occurrence time is invalid")
	}
	expectedMessageID, err := eventcontract.DomainMessageID(metadata.GetCausationId(), eventcontract.LotStateTopicV1)
	if err != nil || metadata.GetMessageId() != expectedMessageID {
		return errors.New("message identity is invalid")
	}
	return nil
}

func publicSearchStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_QUEUED, v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED:
		return true
	default:
		return false
	}
}

func validSnapshotText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
