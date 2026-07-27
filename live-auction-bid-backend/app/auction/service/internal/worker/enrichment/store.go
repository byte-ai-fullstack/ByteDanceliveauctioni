package enrichment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

var (
	ErrInvalidStoreArgument    = errors.New("invalid order enrichment store argument")
	ErrInvalidApplyRecord      = errors.New("invalid order enrichment apply record")
	ErrMessageIdentityConflict = errors.New("order enrichment message identity conflict")
	ErrOrderEnrichmentConflict = errors.New("order already belongs to another enrichment message")
	ErrOrderNotFound           = errors.New("order enrichment core order not found")
	ErrOrderLotMismatch        = errors.New("order enrichment lot does not belong to order")
	ErrEnrichmentSourceCorrupt = errors.New("order enrichment source snapshot is corrupt")
)

// ApplyResult describes one idempotent enrichment effect.
type ApplyResult struct {
	Duplicate bool
	Status    orderenrichment.Status
}

type rowScanner interface {
	Scan(dest ...any) error
}

type enrichmentTransaction interface {
	QueryRowContext(ctx context.Context, query string, args ...any) rowScanner
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

type enrichmentDatabase interface {
	BeginTx(ctx context.Context, options *sql.TxOptions) (enrichmentTransaction, error)
}

type databaseAdapter struct{ db *sql.DB }

func (adapter databaseAdapter) BeginTx(ctx context.Context, options *sql.TxOptions) (enrichmentTransaction, error) {
	tx, err := adapter.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return transactionAdapter{Tx: tx}, nil
}

type transactionAdapter struct{ *sql.Tx }

func (adapter transactionAdapter) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return adapter.Tx.QueryRowContext(ctx, query, args...)
}

// SQLStore applies one domain message in a short MySQL transaction. It only writes
// auction_order_enrichments; the Projector remains the sole writer of order core columns.
type SQLStore struct {
	db    enrichmentDatabase
	nowMs func() int64
}

// NewSQLStore creates an enrichment effect store backed by the target MySQL schema.
func NewSQLStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalidStoreArgument)
	}
	return newSQLStore(databaseAdapter{db: db})
}

func newSQLStore(db enrichmentDatabase) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalidStoreArgument)
	}
	return &SQLStore{db: db, nowMs: func() int64 { return time.Now().UnixMilli() }}, nil
}

// Apply is idempotent by domain message_id and rejects the same identity with different bytes.
func (store *SQLStore) Apply(ctx context.Context, record Record, attempt int) (ApplyResult, error) {
	if store == nil || store.db == nil || store.nowMs == nil {
		return ApplyResult{}, errors.New("order enrichment SQL store is not initialized")
	}
	if err := validateApplyRecord(record); err != nil {
		return ApplyResult{}, err
	}
	if attempt <= 0 || attempt > 100 {
		return ApplyResult{}, fmt.Errorf("%w: attempt must be within [1,100]", ErrInvalidStoreArgument)
	}
	updatedAtMs := store.nowMs()
	if updatedAtMs <= 0 {
		return ApplyResult{}, fmt.Errorf("%w: current time is invalid", ErrInvalidStoreArgument)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("begin order enrichment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if existing, found, err := findByMessageID(ctx, tx, record.MessageID); err != nil {
		return ApplyResult{}, err
	} else if found {
		if existing.OrderID != record.Event.GetOrderId() || existing.PayloadHash != record.PayloadHash {
			return ApplyResult{}, fmt.Errorf("%w: message_id=%s", ErrMessageIdentityConflict, record.MessageID)
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("commit duplicate order enrichment transaction: %w", err)
		}
		return ApplyResult{Duplicate: true, Status: existing.Status}, nil
	}
	if existing, found, err := findByOrderID(ctx, tx, record.Event.GetOrderId()); err != nil {
		return ApplyResult{}, err
	} else if found {
		return ApplyResult{}, fmt.Errorf("%w: order_id=%s existing_message_id=%s", ErrOrderEnrichmentConflict, existing.OrderID, existing.MessageID)
	}

	order, err := loadCoreOrder(ctx, tx, record.Event.GetOrderId())
	if err != nil {
		return ApplyResult{}, err
	}
	if err := requireOrderLot(ctx, tx, record.Event.GetOrderId(), record.Event.GetLotId()); err != nil {
		return ApplyResult{}, err
	}
	address, addressFound, err := resolveAddress(ctx, tx, record, order)
	if err != nil {
		return ApplyResult{}, err
	}
	shopSnapshot, shopFound, err := resolveShop(ctx, tx, record, order)
	if err != nil {
		return ApplyResult{}, err
	}
	status := orderenrichment.StatusReady
	reasons := make([]string, 0, 2)
	if !addressFound {
		status = orderenrichment.StatusPartial
		reasons = append(reasons, "address_not_found")
	}
	if !shopFound {
		status = orderenrichment.StatusPartial
		reasons = append(reasons, "shop_not_found")
	}
	addressJSON, err := optionalJSON(address, addressFound)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("marshal address snapshot: %w", err)
	}
	shopJSON, err := optionalJSON(shopSnapshot, shopFound)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("marshal shop snapshot: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO auction_order_enrichments
  (order_id, source_message_id, payload_hash, address_snapshot, shop_snapshot, status, attempts, last_error, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.Event.GetOrderId(), record.MessageID, record.PayloadHash, addressJSON, shopJSON, string(status), attempt, strings.Join(reasons, ";"), updatedAtMs)
	if err := requireOneRow(result, err, "insert order enrichment"); err != nil {
		return ApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, fmt.Errorf("commit order enrichment transaction: %w", err)
	}
	return ApplyResult{Status: status}, nil
}

type existingEnrichment struct {
	OrderID     string
	MessageID   string
	PayloadHash string
	Status      orderenrichment.Status
}

func findByMessageID(ctx context.Context, tx enrichmentTransaction, messageID string) (existingEnrichment, bool, error) {
	var result existingEnrichment
	err := tx.QueryRowContext(ctx, `
SELECT order_id, source_message_id, payload_hash, status
FROM auction_order_enrichments
WHERE source_message_id = ?
FOR UPDATE`, messageID).Scan(&result.OrderID, &result.MessageID, &result.PayloadHash, &result.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return existingEnrichment{}, false, nil
	}
	if err != nil {
		return existingEnrichment{}, false, fmt.Errorf("read enrichment by message_id: %w", err)
	}
	if !result.Status.Valid() {
		return existingEnrichment{}, false, fmt.Errorf("%w: invalid persisted status %q", ErrEnrichmentSourceCorrupt, result.Status)
	}
	return result, true, nil
}

func findByOrderID(ctx context.Context, tx enrichmentTransaction, orderID string) (existingEnrichment, bool, error) {
	var result existingEnrichment
	err := tx.QueryRowContext(ctx, `
SELECT order_id, source_message_id, payload_hash, status
FROM auction_order_enrichments
WHERE order_id = ?
FOR UPDATE`, orderID).Scan(&result.OrderID, &result.MessageID, &result.PayloadHash, &result.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return existingEnrichment{}, false, nil
	}
	if err != nil {
		return existingEnrichment{}, false, fmt.Errorf("read enrichment by order_id: %w", err)
	}
	return result, true, nil
}

type coreOrder struct {
	BuyerUserID   string
	MainAccountID string
}

func loadCoreOrder(ctx context.Context, tx enrichmentTransaction, orderID string) (coreOrder, error) {
	var result coreOrder
	err := tx.QueryRowContext(ctx, `
SELECT user_id, main_account_id
FROM user_orders
WHERE id = ? AND source = 'auction'
FOR SHARE`, orderID).Scan(&result.BuyerUserID, &result.MainAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return coreOrder{}, fmt.Errorf("%w: order_id=%s", ErrOrderNotFound, orderID)
	}
	if err != nil {
		return coreOrder{}, fmt.Errorf("load core order: %w", err)
	}
	if !validID(result.BuyerUserID) || !validID(result.MainAccountID) {
		return coreOrder{}, fmt.Errorf("%w: core order identity is invalid", ErrEnrichmentSourceCorrupt)
	}
	return result, nil
}

func requireOrderLot(ctx context.Context, tx enrichmentTransaction, orderID, lotID string) error {
	var marker int
	err := tx.QueryRowContext(ctx, `
SELECT 1
FROM user_order_items
WHERE order_id = ? AND source = 'auction' AND lot_id = ?
LIMIT 1`, orderID, lotID).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: order_id=%s lot_id=%s", ErrOrderLotMismatch, orderID, lotID)
	}
	if err != nil {
		return fmt.Errorf("verify order lot: %w", err)
	}
	return nil
}

func resolveAddress(ctx context.Context, tx enrichmentTransaction, record Record, order coreOrder) (orderenrichment.AddressSnapshot, bool, error) {
	if addressID := record.Event.GetAddressId(); addressID != "" {
		return loadActiveAddress(ctx, tx, order.BuyerUserID, addressID, false)
	}
	var addressID string
	var raw []byte
	err := tx.QueryRowContext(ctx, `
SELECT address_id, CAST(address_snapshot AS CHAR)
FROM auction_deposit_holds
WHERE lot_id = ? AND buyer_user_id = ? AND status IN ('HELD', 'CONSUMED')
ORDER BY updated_at_unix_ms DESC, id ASC
LIMIT 1`, record.Event.GetLotId(), order.BuyerUserID).Scan(&addressID, &raw)
	if err == nil {
		if !validID(addressID) || len(raw) == 0 || string(raw) == "null" {
			return orderenrichment.AddressSnapshot{}, false, fmt.Errorf("%w: deposit address snapshot is incomplete", ErrEnrichmentSourceCorrupt)
		}
		var snapshot orderenrichment.AddressSnapshot
		if json.Unmarshal(raw, &snapshot) != nil || snapshot.AddressID != addressID || strings.TrimSpace(snapshot.FullAddress) == "" {
			return orderenrichment.AddressSnapshot{}, false, fmt.Errorf("%w: deposit address snapshot is invalid", ErrEnrichmentSourceCorrupt)
		}
		return snapshot, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return orderenrichment.AddressSnapshot{}, false, fmt.Errorf("load deposit address snapshot: %w", err)
	}
	return loadActiveAddress(ctx, tx, order.BuyerUserID, "", true)
}

func loadActiveAddress(ctx context.Context, tx enrichmentTransaction, buyerUserID, addressID string, defaultFirst bool) (orderenrichment.AddressSnapshot, bool, error) {
	query := `
SELECT id, receiver_name, phone, province, city, district, street, detail, postal_code
FROM user_delivery_addresses
WHERE user_id = ? AND status = 'active'`
	args := []any{buyerUserID}
	if addressID != "" {
		query += " AND id = ?"
		args = append(args, addressID)
	}
	if defaultFirst {
		query += " ORDER BY is_default DESC, updated_at_unix_ms DESC, id ASC LIMIT 1"
	} else {
		query += " LIMIT 1"
	}
	var snapshot orderenrichment.AddressSnapshot
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&snapshot.AddressID, &snapshot.ReceiverName, &snapshot.Phone, &snapshot.Province, &snapshot.City,
		&snapshot.District, &snapshot.Street, &snapshot.Detail, &snapshot.PostalCode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return orderenrichment.AddressSnapshot{}, false, nil
	}
	if err != nil {
		return orderenrichment.AddressSnapshot{}, false, fmt.Errorf("load active delivery address: %w", err)
	}
	if !validID(snapshot.AddressID) || strings.TrimSpace(snapshot.ReceiverName) == "" || strings.TrimSpace(snapshot.Phone) == "" {
		return orderenrichment.AddressSnapshot{}, false, fmt.Errorf("%w: delivery address identity is invalid", ErrEnrichmentSourceCorrupt)
	}
	snapshot.FullAddress = orderenrichment.FullAddress(snapshot.Province, snapshot.City, snapshot.District, snapshot.Street, snapshot.Detail)
	if snapshot.FullAddress == "" {
		return orderenrichment.AddressSnapshot{}, false, fmt.Errorf("%w: delivery address text is empty", ErrEnrichmentSourceCorrupt)
	}
	return snapshot, true, nil
}

func resolveShop(ctx context.Context, tx enrichmentTransaction, record Record, order coreOrder) (orderenrichment.ShopSnapshot, bool, error) {
	shopID := record.Event.GetShopId()
	if shopID == "" {
		shopID = order.MainAccountID
	}
	var nickname, username string
	err := tx.QueryRowContext(ctx, `
SELECT nickname, username
FROM auction_users
WHERE id = ? AND status = 1
LIMIT 1`, shopID).Scan(&nickname, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return orderenrichment.ShopSnapshot{}, false, nil
	}
	if err != nil {
		return orderenrichment.ShopSnapshot{}, false, fmt.Errorf("load shop identity: %w", err)
	}
	name := strings.TrimSpace(nickname)
	if name == "" {
		name = strings.TrimSpace(username)
	}
	if !validID(shopID) || name == "" || len(name) > 128 {
		return orderenrichment.ShopSnapshot{}, false, fmt.Errorf("%w: shop identity is invalid", ErrEnrichmentSourceCorrupt)
	}
	return orderenrichment.ShopSnapshot{ShopID: shopID, ShopName: name}, true, nil
}

func optionalJSON(value any, present bool) (any, error) {
	if !present {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func requireOneRow(result sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if result == nil {
		return fmt.Errorf("%s returned no SQL result", operation)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s affected %d rows", operation, rows)
	}
	return nil
}

func validateApplyRecord(record Record) error {
	if record.Topic != eventcontract.OrderEnrichmentTopicV1 || record.Partition < 0 || record.Offset < 0 ||
		!validText(record.MessageID, 128) || !validText(record.CausationID, 64) || !validText(record.TraceID, 128) ||
		record.Event == nil || record.OccurredAtMs <= 0 || len(record.Payload) == 0 || len(record.Payload) > maxDomainPayloadBytes {
		return fmt.Errorf("%w: identity, source position, or payload is invalid", ErrInvalidApplyRecord)
	}
	expectedMessageID, err := eventcontract.DomainMessageID(record.CausationID, record.Topic)
	if err != nil || expectedMessageID != record.MessageID {
		return fmt.Errorf("%w: message identity is invalid", ErrInvalidApplyRecord)
	}
	digest := sha256.Sum256(record.Payload)
	if record.PayloadHash != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: payload hash mismatch", ErrInvalidApplyRecord)
	}
	decoded := new(v1.OrderEnrichmentRequestedDomainEventV1)
	if err := proto.Unmarshal(record.Payload, decoded); err != nil || !proto.Equal(decoded, record.Event) {
		return fmt.Errorf("%w: payload and decoded event differ", ErrInvalidApplyRecord)
	}
	metadata := decoded.GetMetadata()
	if metadata == nil || metadata.GetMessageId() != record.MessageID || metadata.GetCausationId() != record.CausationID ||
		metadata.GetTraceId() != record.TraceID || metadata.GetOccurredAtUnixMs() != record.OccurredAtMs || metadata.GetSchemaVersion() != 1 ||
		!validID(decoded.GetOrderId()) || !validID(decoded.GetLotId()) {
		return fmt.Errorf("%w: protobuf metadata or business identity differs", ErrInvalidApplyRecord)
	}
	return nil
}
