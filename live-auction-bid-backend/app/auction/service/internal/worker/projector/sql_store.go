package projector

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/mysqlerr"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

var (
	ErrInvalidApplyRecord      = errors.New("invalid projector apply record")
	ErrPartitionOffsetMissing  = errors.New("projector partition offset is not initialized")
	ErrPartitionOffsetGap      = errors.New("projector partition offset gap")
	ErrEventIdentityConflict   = errors.New("runtime event identity conflict")
	ErrRuntimeProjectionGap    = errors.New("runtime lot version gap")
	ErrProjectionLotFrozen     = errors.New("runtime projection lot is frozen")
	ErrProjectionLotNotFound   = errors.New("runtime projection lot not found")
	ErrProjectionIdentity      = errors.New("runtime projection identity conflict")
	ErrProjectionConfigVersion = errors.New("runtime projection config version conflict")
	ErrProjectionCAS           = errors.New("runtime projection compare-and-set conflict")
)

type ApplyResult struct {
	NextOffset      int64
	AlreadyAdvanced bool
	DuplicateEvent  bool
}

type SQLStore struct {
	db               *sql.DB
	nowMs            func() int64
	retryMaxAttempts int
	retryBaseDelay   time.Duration
	retryMaxDelay    time.Duration
	retryJitter      func(time.Duration) time.Duration
	retryWait        func(context.Context, time.Duration) error
}

type SQLStoreOption func(*SQLStore) error

func WithRetryPolicy(maxAttempts int, baseDelay, maxDelay time.Duration) SQLStoreOption {
	return func(store *SQLStore) error {
		if maxAttempts <= 0 || baseDelay <= 0 || maxDelay < baseDelay {
			return errors.New("projector retry policy requires positive attempts and valid delays")
		}
		store.retryMaxAttempts = maxAttempts
		store.retryBaseDelay = baseDelay
		store.retryMaxDelay = maxDelay
		return nil
	}
}

func NewSQLStore(db *sql.DB, options ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("projector database is required")
	}
	store := &SQLStore{
		db:               db,
		nowMs:            func() int64 { return time.Now().UnixMilli() },
		retryMaxAttempts: 5,
		retryBaseDelay:   20 * time.Millisecond,
		retryMaxDelay:    time.Second,
		retryJitter: func(limit time.Duration) time.Duration {
			if limit <= 0 {
				return 0
			}
			return time.Duration(rand.Int63n(int64(limit)))
		},
		retryWait: waitForRetry,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("projector SQL store option is required")
		}
		if err := option(store); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// EnsurePartitionOffset initializes a partition exactly once from the caller-provided Kafka earliest offset.
// Existing DB recovery state is never overwritten by Kafka consumer-group state.
func (s *SQLStore) EnsurePartitionOffset(ctx context.Context, topic string, partition int32, earliest int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("projector database is required")
	}
	if topic != eventcontract.RuntimeProjectionTopicV1 || partition < 0 || earliest < 0 {
		return 0, fmt.Errorf("%w: invalid topic, partition, or earliest offset", ErrInvalidApplyRecord)
	}
	nowMs := s.nowMs()
	if _, err := s.db.ExecContext(ctx, `
INSERT IGNORE INTO auction_projection_partition_offsets
  (topic, kafka_partition, next_offset, updated_at_ms)
VALUES (?, ?, ?, ?)`, topic, partition, earliest, nowMs); err != nil {
		return 0, fmt.Errorf("initialize projector partition offset: %w", err)
	}
	var nextOffset int64
	if err := s.db.QueryRowContext(ctx, `
SELECT next_offset
FROM auction_projection_partition_offsets
WHERE topic = ? AND kafka_partition = ?`, topic, partition).Scan(&nextOffset); err != nil {
		return 0, fmt.Errorf("load projector partition offset: %w", err)
	}
	if nextOffset < 0 {
		return 0, fmt.Errorf("%w: stored next_offset is negative", ErrPartitionOffsetGap)
	}
	return nextOffset, nil
}

// Apply commits one decoded Kafka record, every business projection, inbox identity,
// domain outbox message, and the next DB offset in one InnoDB transaction.
func (s *SQLStore) Apply(ctx context.Context, record DecodedRecord) (ApplyResult, error) {
	if s == nil || s.db == nil {
		return ApplyResult{}, errors.New("projector database is required")
	}
	if err := validateApplyRecord(record); err != nil {
		return ApplyResult{}, err
	}
	var lastErr error
	for attempt := 1; attempt <= s.retryMaxAttempts; attempt++ {
		startedAt := time.Now()
		result, err := s.applyOnce(ctx, record)
		observability.RecordProjectorTransactionDuration(time.Since(startedAt))
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !IsTransientDatabaseError(err) || attempt == s.retryMaxAttempts {
			return ApplyResult{}, err
		}
		observability.RecordProjectorRetry(transientDatabaseReason(err))
		if err := s.retryWait(ctx, s.retryDelay(attempt)); err != nil {
			return ApplyResult{}, errors.Join(lastErr, err)
		}
	}
	return ApplyResult{}, lastErr
}

func transientDatabaseReason(err error) string {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1205:
			return "lock_wait_timeout"
		case 1213:
			return "deadlock"
		}
	}
	return "connection"
}

func (s *SQLStore) applyOnce(ctx context.Context, record DecodedRecord) (ApplyResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("begin projector transaction: %w", err)
	}
	result, applyErr := applyRuntimeRecord(ctx, sqlTxAdapter{Tx: tx}, record, s.nowMs())
	if applyErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return ApplyResult{}, errors.Join(applyErr, fmt.Errorf("rollback projector transaction: %w", rollbackErr))
		}
		return ApplyResult{}, applyErr
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, fmt.Errorf("commit projector transaction: %w", err)
	}
	return result, nil
}

func (s *SQLStore) retryDelay(failedAttempt int) time.Duration {
	limit := s.retryBaseDelay
	for step := 1; step < failedAttempt && limit < s.retryMaxDelay; step++ {
		if limit > s.retryMaxDelay/2 {
			limit = s.retryMaxDelay
			break
		}
		limit *= 2
	}
	if limit > s.retryMaxDelay {
		limit = s.retryMaxDelay
	}
	return s.retryJitter(limit)
}

// IsTransientDatabaseError classifies only errors for which replaying the complete idempotent transaction is safe.
func IsTransientDatabaseError(err error) bool {
	return mysqlerr.Transient(err)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func validateApplyRecord(record DecodedRecord) error {
	if record.Topic != eventcontract.RuntimeProjectionTopicV1 || record.Partition < 0 || record.Offset < 0 {
		return fmt.Errorf("%w: invalid source position", ErrInvalidApplyRecord)
	}
	if err := eventcontract.ValidateRuntimeFact(record.Fact); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidApplyRecord, err)
	}
	if len(record.Payload) == 0 || len(record.Payload) > eventcontract.MaxRuntimeFactBytes {
		return fmt.Errorf("%w: payload size is outside the allowed range", ErrInvalidApplyRecord)
	}
	sum := sha256.Sum256(record.Payload)
	if record.PayloadHash != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%w: payload hash mismatch", ErrInvalidApplyRecord)
	}
	decoded := new(v1.RuntimeFactV1)
	if err := proto.Unmarshal(record.Payload, decoded); err != nil || !proto.Equal(decoded, record.Fact) {
		return fmt.Errorf("%w: payload and decoded fact differ", ErrInvalidApplyRecord)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type sqlProjectionTx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) rowScanner
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlTxAdapter struct {
	*sql.Tx
}

func (tx sqlTxAdapter) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return tx.Tx.QueryRowContext(ctx, query, args...)
}
