package domainrelay

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const (
	maxClaimLimit      = 100
	maxInstanceIDBytes = 64
	maxErrorBytes      = 512
)

var (
	ErrInvalidStoreArgument = errors.New("invalid domain outbox store argument")
	ErrClaimLost            = errors.New("domain outbox claim lost")
)

// Message is one transactionally claimed domain outbox row.
type Message struct {
	ID            int64
	MessageID     string
	CausationID   string
	Topic         string
	PartitionKey  string
	Payload       []byte
	HeadersJSON   []byte
	CreatedAtMs   int64
	Attempts      int
	LastError     string
	LockedBy      string
	LockToken     string
	LockedUntilMs int64
}

// Stats is the current unpublished domain outbox backlog.
type Stats struct {
	Pending              int64
	OldestAgeMs          int64
	OrderVisibilityLagMs int64
}

// Store is the persistence boundary used by the relay loop.
type Store interface {
	Claim(ctx context.Context, instanceID string, now time.Time, limit int, leaseTTL time.Duration) ([]Message, error)
	MarkPublished(ctx context.Context, message Message, now time.Time) error
	MarkFailed(ctx context.Context, message Message, now, nextAttempt time.Time, attempts int, failure string) error
	MarkDeadLettered(ctx context.Context, message Message, now time.Time, attempts int, failure string) error
	Stats(ctx context.Context, now time.Time) (Stats, error)
}

type sqlRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

type sqlRow interface {
	Scan(dest ...any) error
}

type sqlTransaction interface {
	QueryContext(ctx context.Context, query string, args ...any) (sqlRows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

type sqlDatabase interface {
	BeginTx(ctx context.Context, options *sql.TxOptions) (sqlTransaction, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) sqlRow
}

type databaseAdapter struct{ db *sql.DB }

func (adapter databaseAdapter) BeginTx(ctx context.Context, options *sql.TxOptions) (sqlTransaction, error) {
	tx, err := adapter.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return transactionAdapter{tx: tx}, nil
}

func (adapter databaseAdapter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return adapter.db.ExecContext(ctx, query, args...)
}

func (adapter databaseAdapter) QueryRowContext(ctx context.Context, query string, args ...any) sqlRow {
	return adapter.db.QueryRowContext(ctx, query, args...)
}

type transactionAdapter struct{ tx *sql.Tx }

func (adapter transactionAdapter) QueryContext(ctx context.Context, query string, args ...any) (sqlRows, error) {
	return adapter.tx.QueryContext(ctx, query, args...)
}

func (adapter transactionAdapter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return adapter.tx.ExecContext(ctx, query, args...)
}

func (adapter transactionAdapter) Commit() error   { return adapter.tx.Commit() }
func (adapter transactionAdapter) Rollback() error { return adapter.tx.Rollback() }

// SQLStore claims MySQL rows with SKIP LOCKED and fences every terminal update by a random token.
type SQLStore struct {
	db       sqlDatabase
	newToken func() (string, error)
}

// NewSQLStore creates a domain outbox store backed by the target MySQL schema.
func NewSQLStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalidStoreArgument)
	}
	return newSQLStore(databaseAdapter{db: db}, randomClaimToken)
}

func newSQLStore(db sqlDatabase, tokenFactory func() (string, error)) (*SQLStore, error) {
	if db == nil || tokenFactory == nil {
		return nil, fmt.Errorf("%w: database and token factory are required", ErrInvalidStoreArgument)
	}
	return &SQLStore{db: db, newToken: tokenFactory}, nil
}

// Claim locks only currently claimable rows and commits their fencing tokens before returning.
func (store *SQLStore) Claim(ctx context.Context, instanceID string, now time.Time, limit int, leaseTTL time.Duration) ([]Message, error) {
	if store == nil || store.db == nil || store.newToken == nil {
		return nil, errors.New("domain outbox SQL store is not initialized")
	}
	instanceID = strings.TrimSpace(instanceID)
	if !validBoundedText(instanceID, maxInstanceIDBytes) || strings.Contains(instanceID, ":") || limit <= 0 || limit > maxClaimLimit || leaseTTL <= 0 {
		return nil, fmt.Errorf("%w: invalid instance, limit, or lease", ErrInvalidStoreArgument)
	}
	nowMs := now.UnixMilli()
	lockedUntilMs := now.Add(leaseTTL).UnixMilli()
	if nowMs <= 0 || lockedUntilMs <= nowMs {
		return nil, fmt.Errorf("%w: invalid claim time window", ErrInvalidStoreArgument)
	}

	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin domain outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT candidate.id, candidate.message_id, candidate.causation_id,
       candidate.topic, candidate.partition_key, candidate.payload,
       COALESCE(CAST(candidate.headers_json AS CHAR), '{}'),
       candidate.created_at_ms, candidate.attempts, candidate.last_error
FROM auction_domain_outbox AS candidate
WHERE candidate.published_at_ms = 0
  AND candidate.next_attempt_ms <= ?
  AND candidate.locked_until_ms < ?
  AND NOT EXISTS (
      SELECT 1
      FROM auction_domain_outbox AS predecessor
      WHERE predecessor.topic = candidate.topic
        AND predecessor.partition_key = candidate.partition_key
        AND predecessor.published_at_ms = 0
        AND predecessor.id < candidate.id
  )
ORDER BY candidate.id
LIMIT ?
FOR UPDATE SKIP LOCKED`, nowMs, nowMs, limit)
	if err != nil {
		return nil, fmt.Errorf("select claimable domain outbox rows: %w", err)
	}
	messages := make([]Message, 0, limit)
	for rows.Next() {
		var message Message
		if err := rows.Scan(
			&message.ID, &message.MessageID, &message.CausationID, &message.Topic, &message.PartitionKey,
			&message.Payload, &message.HeadersJSON, &message.CreatedAtMs, &message.Attempts, &message.LastError,
		); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan domain outbox row: %w", err)
		}
		message.Payload = append([]byte(nil), message.Payload...)
		message.HeadersJSON = append([]byte(nil), message.HeadersJSON...)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate domain outbox rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close domain outbox rows: %w", err)
	}

	for index := range messages {
		token, err := store.newToken()
		if err != nil {
			return nil, fmt.Errorf("create domain outbox lock token: %w", err)
		}
		if !validClaimToken(token) {
			return nil, fmt.Errorf("%w: token factory returned an invalid token", ErrInvalidStoreArgument)
		}
		result, err := tx.ExecContext(ctx, `
UPDATE auction_domain_outbox
SET locked_by = ?, lock_token = ?, locked_until_ms = ?
WHERE id = ? AND published_at_ms = 0 AND locked_until_ms < ?`,
			instanceID, token, lockedUntilMs, messages[index].ID, nowMs)
		if err := requireOneRow(result, err, "claim domain outbox row"); err != nil {
			return nil, err
		}
		messages[index].LockedBy = instanceID
		messages[index].LockToken = token
		messages[index].LockedUntilMs = lockedUntilMs
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit domain outbox claim: %w", err)
	}
	return messages, nil
}

// MarkPublished records success only after Kafka has acknowledged the original domain record.
func (store *SQLStore) MarkPublished(ctx context.Context, message Message, now time.Time) error {
	return store.finish(ctx, message, now, 0, "", false)
}

// MarkDeadLettered records a terminal, auditable outcome only after Kafka has acknowledged the DLQ copy.
func (store *SQLStore) MarkDeadLettered(ctx context.Context, message Message, now time.Time, attempts int, failure string) error {
	if attempts <= message.Attempts {
		return fmt.Errorf("%w: attempts must advance", ErrInvalidStoreArgument)
	}
	return store.finish(ctx, message, now, attempts, failure, true)
}

func (store *SQLStore) finish(ctx context.Context, message Message, now time.Time, attempts int, failure string, deadLettered bool) error {
	if store == nil || store.db == nil {
		return errors.New("domain outbox SQL store is not initialized")
	}
	nowMs := now.UnixMilli()
	if err := validateClaim(message, nowMs); err != nil {
		return err
	}
	failure = sanitizeFailure(failure)
	var result sql.Result
	var err error
	if deadLettered {
		result, err = store.db.ExecContext(ctx, `
UPDATE auction_domain_outbox
SET published_at_ms = ?, attempts = ?, next_attempt_ms = 0,
    locked_by = '', lock_token = '', locked_until_ms = 0, last_error = ?
WHERE id = ? AND locked_by = ? AND lock_token = ? AND published_at_ms = 0 AND locked_until_ms >= ?`,
			nowMs, attempts, failure, message.ID, message.LockedBy, message.LockToken, nowMs)
	} else {
		result, err = store.db.ExecContext(ctx, `
UPDATE auction_domain_outbox
SET published_at_ms = ?, next_attempt_ms = 0,
    locked_by = '', lock_token = '', locked_until_ms = 0, last_error = ''
WHERE id = ? AND locked_by = ? AND lock_token = ? AND published_at_ms = 0 AND locked_until_ms >= ?`,
			nowMs, message.ID, message.LockedBy, message.LockToken, nowMs)
	}
	if err := requireClaimRow(result, err, "finish domain outbox row"); err != nil {
		return err
	}
	return nil
}

// MarkFailed releases a live claim with a bounded error and a future retry time.
func (store *SQLStore) MarkFailed(ctx context.Context, message Message, now, nextAttempt time.Time, attempts int, failure string) error {
	if store == nil || store.db == nil {
		return errors.New("domain outbox SQL store is not initialized")
	}
	nowMs := now.UnixMilli()
	if err := validateClaim(message, nowMs); err != nil {
		return err
	}
	nextAttemptMs := nextAttempt.UnixMilli()
	if attempts <= message.Attempts || nextAttemptMs <= nowMs {
		return fmt.Errorf("%w: attempts and next attempt must advance", ErrInvalidStoreArgument)
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE auction_domain_outbox
SET attempts = ?, next_attempt_ms = ?, locked_by = '', lock_token = '', locked_until_ms = 0, last_error = ?
WHERE id = ? AND locked_by = ? AND lock_token = ? AND published_at_ms = 0 AND locked_until_ms >= ?`,
		attempts, nextAttemptMs, sanitizeFailure(failure), message.ID, message.LockedBy, message.LockToken, nowMs)
	return requireClaimRow(result, err, "release failed domain outbox row")
}

// Stats returns a low-cardinality backlog snapshot for readiness-independent metrics.
func (store *SQLStore) Stats(ctx context.Context, now time.Time) (Stats, error) {
	if store == nil || store.db == nil || now.UnixMilli() <= 0 {
		return Stats{}, fmt.Errorf("%w: initialized store and valid time are required", ErrInvalidStoreArgument)
	}
	var stats Stats
	var oldestCreatedMs int64
	var oldestOrderCreatedMs int64
	err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MIN(created_at_ms), 0),
       COALESCE(MIN(CASE WHEN topic = ? THEN created_at_ms END), 0)
FROM auction_domain_outbox
WHERE published_at_ms = 0`, eventcontract.OrderCreatedTopicV1).Scan(&stats.Pending, &oldestCreatedMs, &oldestOrderCreatedMs)
	if err != nil {
		return Stats{}, fmt.Errorf("query domain outbox stats: %w", err)
	}
	if stats.Pending > 0 && oldestCreatedMs > 0 && oldestCreatedMs < now.UnixMilli() {
		stats.OldestAgeMs = now.UnixMilli() - oldestCreatedMs
	}
	if oldestOrderCreatedMs > 0 && oldestOrderCreatedMs < now.UnixMilli() {
		stats.OrderVisibilityLagMs = now.UnixMilli() - oldestOrderCreatedMs
	}
	return stats, nil
}

func validateClaim(message Message, nowMs int64) error {
	if message.ID <= 0 || !validBoundedText(message.LockedBy, maxInstanceIDBytes) || !validClaimToken(message.LockToken) || nowMs <= 0 || message.LockedUntilMs < nowMs {
		return fmt.Errorf("%w: message does not hold a live claim", ErrInvalidStoreArgument)
	}
	return nil
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
		return fmt.Errorf("%w: %s affected %d rows", ErrClaimLost, operation, rows)
	}
	return nil
}

func requireClaimRow(result sql.Result, err error, operation string) error {
	if err := requireOneRow(result, err, operation); err != nil {
		if errors.Is(err, ErrClaimLost) {
			return err
		}
		return err
	}
	return nil
}

func randomClaimToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func validClaimToken(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func validBoundedText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func sanitizeFailure(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	if value == "" {
		value = "unspecified failure"
	}
	for len(value) > maxErrorBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			size = 1
		}
		value = value[:len(value)-size]
	}
	return value
}
