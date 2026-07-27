package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const (
	runtimeFindingStateDiverged = "RUNTIME_STATE_DIVERGED"
	runtimeFindingEventMissing  = "RUNTIME_EVENT_UNLOCATED"
	runtimeFindingStateMissing  = "RUNTIME_STATE_MISSING"
	runtimeFindingProjectionLag = "RUNTIME_PROJECTION_LAG"

	runtimeFindingSeverityP0 = "P0"
	runtimeFindingSeverityP1 = "P1"

	maxRuntimeReconcileLots       = 100_000
	maxRuntimeReconcileQueueItems = 100_000
	runtimeUnlocatedEscalation    = 2 * time.Minute
)

var (
	ErrRuntimeReconcilePending  = errors.New("runtime reconciliation is pending")
	ErrRuntimeStateDiverged     = errors.New("runtime state diverged")
	ErrRuntimeReconcileTooLarge = errors.New("runtime reconciliation input is too large")
)

type runtimeCoreState struct {
	Status          int32  `json:"status"`
	CurrentPriceFen int64  `json:"current_price_fen"`
	WinnerUserID    string `json:"winner_user_id"`
	EndsAtUnixMs    int64  `json:"ends_at_unix_ms"`
}

type runtimeRedisIdentity struct {
	LotID         string
	RoomID        string
	LastEventID   string
	LotVersion    int64
	CanonicalHash string
	State         *v1.LotRuntimeStateV1
}

type runtimeProjectionIdentity struct {
	Found            bool
	Frozen           bool
	LotID            string
	RoomID           string
	ProjectionRoomID string
	LastEventID      string
	LotVersion       int64
	LotRowVersion    int64
	CanonicalHash    string
	LastAppliedMs    int64
	LotCore          runtimeCoreState
}

type runtimeReconcileClass string

const (
	runtimeReconcileExact       runtimeReconcileClass = "EXACT"
	runtimeReconcileRecoverable runtimeReconcileClass = "REDIS_AHEAD_RECOVERABLE"
	runtimeReconcileUnlocated   runtimeReconcileClass = "REDIS_AHEAD_UNLOCATED"
	runtimeReconcileDiverged    runtimeReconcileClass = "DIVERGED"
)

type RuntimeReconciler struct {
	db         *sql.DB
	redis      redis.UniversalClient
	shardCount int
	now        func() time.Time
}

func NewRuntimeReconciler(db *sql.DB, client redis.UniversalClient, shardCount int) (*RuntimeReconciler, error) {
	if db == nil || client == nil {
		return nil, errors.New("runtime reconciler requires MySQL and Redis")
	}
	normalized, err := normalizeRuntimeOutboxShardCount(shardCount)
	if err != nil {
		return nil, err
	}
	return &RuntimeReconciler{db: db, redis: client, shardCount: normalized, now: time.Now}, nil
}

// VerifyActive compares every active or priority lot in both directions. Unsafe
// lots are fenced in Redis and persisted as findings; recoverable Redis-ahead
// chains are allowed because every missing fact is still in pending/inflight.
func (reconciler *RuntimeReconciler) VerifyActive(ctx context.Context) error {
	if reconciler == nil || reconciler.db == nil || reconciler.redis == nil {
		return errors.New("runtime reconciler is not initialized")
	}
	lotIDs, err := reconciler.activeLotIDs(ctx)
	if err != nil {
		return err
	}
	var pending []error
	for _, lotID := range lotIDs {
		if err := reconciler.verifyLot(ctx, lotID); err != nil {
			pending = append(pending, fmt.Errorf("lot %s: %w", lotID, err))
		}
	}
	return errors.Join(pending...)
}

func (reconciler *RuntimeReconciler) verifyLot(ctx context.Context, lotID string) error {
	projection, projectionErr := reconciler.readProjectionIdentity(ctx, lotID)
	if projectionErr != nil {
		return projectionErr
	}
	runtimeIdentity, runtimeErr := reconciler.readRedisIdentity(ctx, lotID)
	if runtimeErr != nil {
		if errors.Is(runtimeErr, redis.Nil) {
			if err := reconciler.quarantine(ctx, projection, nil, runtimeFindingStateMissing, runtimeFindingSeverityP0, runtimeErr, true); err != nil {
				return err
			}
			return nil
		}
		return runtimeErr
	}

	recoverable := false
	baseVersion := projection.LotVersion
	if runtimeIdentity.LotVersion > baseVersion {
		recoverable, runtimeErr = reconciler.outboxContainsCompleteChain(ctx, projection, runtimeIdentity)
		if runtimeErr != nil {
			return runtimeErr
		}
	}
	class, reason := classifyRuntimeReconciliation(runtimeIdentity, projection, recoverable)
	switch class {
	case runtimeReconcileExact, runtimeReconcileRecoverable:
		if err := reconciler.resolveRuntimeFindings(ctx, lotID); err != nil {
			return err
		}
		pipe := reconciler.redis.TxPipeline()
		pipe.Del(ctx, runtimeFrozenLotKey(lotID))
		pipe.SRem(ctx, runtimePriorityReconcileKey(), lotID)
		_, err := pipe.Exec(ctx)
		return err
	case runtimeReconcileUnlocated:
		severity, err := reconciler.unlocatedSeverity(ctx, lotID)
		if err != nil {
			return err
		}
		if err := reconciler.quarantine(ctx, projection, &runtimeIdentity, runtimeFindingEventMissing, severity, reason, false); err != nil {
			return err
		}
		// Kafka may already own the missing fact. Keep this lot fenced while the
		// Projector catches up, but do not freeze the Projector itself.
		return nil
	case runtimeReconcileDiverged:
		if err := reconciler.quarantine(ctx, projection, &runtimeIdentity, runtimeFindingStateDiverged, runtimeFindingSeverityP0, reason, true); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown classification %q", ErrRuntimeStateDiverged, class)
	}
}

func classifyRuntimeReconciliation(runtime runtimeRedisIdentity, projection runtimeProjectionIdentity, recoverable bool) (runtimeReconcileClass, error) {
	if runtime.LotID == "" || runtime.RoomID == "" || runtime.LastEventID == "" || runtime.LotVersion <= 0 || runtime.CanonicalHash == "" || runtime.State == nil {
		return runtimeReconcileDiverged, fmt.Errorf("%w: Redis identity is incomplete", ErrRuntimeStateDiverged)
	}
	if projection.LotID != runtime.LotID || projection.RoomID != runtime.RoomID || (projection.Found && projection.ProjectionRoomID != projection.RoomID) {
		return runtimeReconcileDiverged, fmt.Errorf("%w: lot or room identity mismatch", ErrRuntimeStateDiverged)
	}
	if projection.Frozen {
		return runtimeReconcileDiverged, fmt.Errorf("%w: projection is already frozen", ErrRuntimeStateDiverged)
	}
	if projection.Found && projection.LotVersion != projection.LotRowVersion {
		return runtimeReconcileDiverged, fmt.Errorf("%w: projection waterline version %d differs from auction_lots %d", ErrRuntimeStateDiverged, projection.LotVersion, projection.LotRowVersion)
	}
	if runtime.LotVersion < projection.LotVersion {
		return runtimeReconcileDiverged, fmt.Errorf("%w: MySQL version %d is ahead of Redis %d", ErrRuntimeStateDiverged, projection.LotVersion, runtime.LotVersion)
	}
	if runtime.LotVersion == projection.LotVersion {
		if !projection.Found {
			return runtimeReconcileDiverged, fmt.Errorf("%w: projection identity is missing at version %d", ErrRuntimeStateDiverged, runtime.LotVersion)
		}
		if runtime.LastEventID != projection.LastEventID {
			return runtimeReconcileDiverged, fmt.Errorf("%w: same version has different last_event_id", ErrRuntimeStateDiverged)
		}
		if runtime.CanonicalHash != projection.CanonicalHash {
			return runtimeReconcileDiverged, fmt.Errorf("%w: same version has different canonical hash", ErrRuntimeStateDiverged)
		}
		if runtimeCoreFromState(runtime.State) != projection.LotCore {
			return runtimeReconcileDiverged, fmt.Errorf("%w: MySQL projected core differs from canonical Redis state", ErrRuntimeStateDiverged)
		}
		return runtimeReconcileExact, nil
	}
	if recoverable {
		return runtimeReconcileRecoverable, nil
	}
	return runtimeReconcileUnlocated, fmt.Errorf("%w: Redis version %d is ahead of MySQL %d and the complete chain is not in Redis outbox", ErrRuntimeReconcilePending, runtime.LotVersion, projection.LotVersion)
}

func (reconciler *RuntimeReconciler) activeLotIDs(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	// Redis may lose both the aggregate hash and its expiry member during a
	// failover. Start from MySQL's durable active projection so that absence on
	// the new primary is detected instead of being mistaken for an empty room.
	if err := reconciler.addMySQLActiveLotIDs(ctx, seen); err != nil {
		return nil, err
	}
	var zCursor uint64
	for {
		values, next, err := reconciler.redis.ZScan(ctx, runtimeExpiringKey(), zCursor, "", 500).Result()
		if err != nil {
			return nil, fmt.Errorf("scan active runtime lots: %w", err)
		}
		for index := 0; index+1 < len(values); index += 2 {
			if lotID := strings.TrimSpace(values[index]); lotID != "" {
				seen[lotID] = struct{}{}
			}
		}
		if len(seen) > maxRuntimeReconcileLots {
			return nil, ErrRuntimeReconcileTooLarge
		}
		zCursor = next
		if next == 0 {
			break
		}
	}
	var setCursor uint64
	for {
		values, next, err := reconciler.redis.SScan(ctx, runtimePriorityReconcileKey(), setCursor, "", 500).Result()
		if err != nil {
			return nil, fmt.Errorf("scan priority runtime lots: %w", err)
		}
		for _, value := range values {
			if lotID := strings.TrimSpace(value); lotID != "" {
				seen[lotID] = struct{}{}
			}
		}
		if len(seen) > maxRuntimeReconcileLots {
			return nil, ErrRuntimeReconcileTooLarge
		}
		setCursor = next
		if next == 0 {
			break
		}
	}
	result := make([]string, 0, len(seen))
	for lotID := range seen {
		result = append(result, lotID)
	}
	return result, nil
}

func (reconciler *RuntimeReconciler) addMySQLActiveLotIDs(ctx context.Context, seen map[string]struct{}) error {
	if reconciler == nil || reconciler.db == nil {
		return errors.New("runtime reconciler MySQL connection is required")
	}
	if seen == nil {
		return errors.New("runtime reconciler lot set is required")
	}
	rows, err := reconciler.db.QueryContext(ctx, `
SELECT id
FROM auction_lots
WHERE status IN (?, ?)`,
		int32(v1.LotStatus_LOT_STATUS_LIVE), int32(v1.LotStatus_LOT_STATUS_EXTENDED))
	if err != nil {
		return fmt.Errorf("scan MySQL active runtime lots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var lotID string
		if err := rows.Scan(&lotID); err != nil {
			return fmt.Errorf("scan MySQL active runtime lot id: %w", err)
		}
		if lotID = strings.TrimSpace(lotID); lotID != "" {
			seen[lotID] = struct{}{}
		}
		if len(seen) > maxRuntimeReconcileLots {
			return ErrRuntimeReconcileTooLarge
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate MySQL active runtime lots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close MySQL active runtime lots: %w", err)
	}
	return nil
}

func (reconciler *RuntimeReconciler) readRedisIdentity(ctx context.Context, lotID string) (runtimeRedisIdentity, error) {
	values, err := reconciler.redis.HMGet(ctx, runtimeStateKey(lotID), "room_id", "version", "last_event_id", "state_after_json").Result()
	if err != nil {
		return runtimeRedisIdentity{}, err
	}
	for _, value := range values {
		if value == nil {
			return runtimeRedisIdentity{}, redis.Nil
		}
	}
	version, err := strconv.ParseInt(fmt.Sprint(values[1]), 10, 64)
	if err != nil || version <= 0 {
		return runtimeRedisIdentity{}, fmt.Errorf("%w: invalid Redis lot version", ErrRuntimeStateDiverged)
	}
	lastEventID := fmt.Sprint(values[2])
	if err := eventcontract.ValidateEventID(lastEventID); err != nil {
		return runtimeRedisIdentity{}, fmt.Errorf("%w: invalid Redis last_event_id: %v", ErrRuntimeStateDiverged, err)
	}
	state := new(v1.LotRuntimeStateV1)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(fmt.Sprint(values[3])), state); err != nil {
		return runtimeRedisIdentity{}, fmt.Errorf("%w: decode Redis state_after_json: %v", ErrRuntimeStateDiverged, err)
	}
	roomID := fmt.Sprint(values[0])
	if state.GetLotId() != lotID || state.GetRoomId() != roomID {
		return runtimeRedisIdentity{}, fmt.Errorf("%w: Redis state JSON identity mismatch", ErrRuntimeStateDiverged)
	}
	hash, err := eventcontract.CanonicalStateHash(state)
	if err != nil {
		return runtimeRedisIdentity{}, err
	}
	return runtimeRedisIdentity{LotID: lotID, RoomID: roomID, LastEventID: lastEventID, LotVersion: version, CanonicalHash: hash, State: state}, nil
}

func (reconciler *RuntimeReconciler) readProjectionIdentity(ctx context.Context, lotID string) (runtimeProjectionIdentity, error) {
	identity := runtimeProjectionIdentity{LotID: lotID}
	err := reconciler.db.QueryRowContext(ctx, `
SELECT room_id, version, status, current_price_amount, winner_user_id, ends_at_unix_ms
FROM auction_lots
WHERE id = ?`, lotID).Scan(
		&identity.RoomID, &identity.LotRowVersion, &identity.LotCore.Status, &identity.LotCore.CurrentPriceFen,
		&identity.LotCore.WinnerUserID, &identity.LotCore.EndsAtUnixMs,
	)
	if err != nil {
		return runtimeProjectionIdentity{}, fmt.Errorf("read auction lot for reconciliation: %w", err)
	}
	var lastEventID sql.NullString
	err = reconciler.db.QueryRowContext(ctx, `
SELECT room_id, last_event_id, last_lot_version, canonical_hash, frozen, last_applied_ms
FROM auction_lot_projection_state
	WHERE lot_id = ?`, lotID).Scan(
		&identity.ProjectionRoomID, &lastEventID, &identity.LotVersion, &identity.CanonicalHash, &identity.Frozen, &identity.LastAppliedMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		identity.LotVersion = identity.LotRowVersion
		return identity, nil
	}
	if err != nil {
		return runtimeProjectionIdentity{}, fmt.Errorf("read runtime projection identity: %w", err)
	}
	identity.Found = true
	identity.LastEventID = lastEventID.String
	return identity, nil
}

func (reconciler *RuntimeReconciler) outboxContainsCompleteChain(ctx context.Context, projection runtimeProjectionIdentity, runtime runtimeRedisIdentity) (bool, error) {
	shard := runtimeOutboxShardFor(runtime.LotID, reconciler.shardCount)
	keys := []string{runtimeOutboxPendingKey(shard), runtimeOutboxInflightKey(shard)}
	facts := make(map[int64]*v1.RuntimeFactV1)
	for _, key := range keys {
		length, err := reconciler.redis.LLen(ctx, key).Result()
		if err != nil {
			return false, fmt.Errorf("read runtime outbox length: %w", err)
		}
		if length > maxRuntimeReconcileQueueItems {
			return false, fmt.Errorf("%w: key=%s length=%d", ErrRuntimeReconcileTooLarge, key, length)
		}
		items, err := reconciler.redis.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return false, fmt.Errorf("scan runtime outbox: %w", err)
		}
		for _, item := range items {
			fact, err := eventcontract.DecodeRuntimeOutboxItem(item)
			if err != nil {
				return false, fmt.Errorf("decode runtime outbox during reconciliation: %w", err)
			}
			if fact.GetLotId() != runtime.LotID {
				continue
			}
			if previous, exists := facts[fact.GetLotVersion()]; exists && !proto.Equal(previous, fact) {
				return false, fmt.Errorf("%w: conflicting outbox facts at lot version %d", ErrRuntimeStateDiverged, fact.GetLotVersion())
			}
			facts[fact.GetLotVersion()] = fact
		}
	}
	for version := projection.LotVersion + 1; version <= runtime.LotVersion; version++ {
		fact := facts[version]
		if fact == nil || fact.GetPrevLotVersion() != version-1 || fact.GetRoomId() != runtime.RoomID {
			return false, nil
		}
		if version == runtime.LotVersion {
			if fact.GetEventId() != runtime.LastEventID || !proto.Equal(fact.GetStateAfter(), runtime.State) {
				return false, fmt.Errorf("%w: final outbox fact differs from Redis identity", ErrRuntimeStateDiverged)
			}
		}
	}
	return true, nil
}

func (reconciler *RuntimeReconciler) quarantine(ctx context.Context, projection runtimeProjectionIdentity, runtime *runtimeRedisIdentity, kind, severity string, cause error, freezeProjection bool) error {
	if cause == nil {
		cause = ErrRuntimeStateDiverged
	}
	if err := reconciler.redis.Set(ctx, runtimeFrozenLotKey(projection.LotID), kind, 0).Err(); err != nil {
		return fmt.Errorf("fence divergent runtime lot: %w", err)
	}
	detailValue := map[string]any{
		"error": cause.Error(), "room_id": projection.RoomID, "mysql_version": projection.LotVersion,
		"mysql_event_id": projection.LastEventID, "mysql_hash": projection.CanonicalHash,
		"mysql_core": projection.LotCore,
	}
	if runtime != nil {
		detailValue["redis_version"] = runtime.LotVersion
		detailValue["redis_event_id"] = runtime.LastEventID
		detailValue["redis_hash"] = runtime.CanonicalHash
		detailValue["redis_core"] = runtimeCoreFromState(runtime.State)
	}
	detail, err := json.Marshal(detailValue)
	if err != nil {
		return err
	}
	tx, err := reconciler.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin runtime reconcile finding: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(cause, rollbackErr)
		}
		return cause
	}
	if freezeProjection {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO auction_lot_projection_state
  (lot_id, room_id, last_event_id, last_lot_version, canonical_hash, frozen, last_applied_ms)
VALUES (?, ?, NULL, ?, '', 1, 0)
ON DUPLICATE KEY UPDATE frozen = 1`, projection.LotID, projection.RoomID, projection.LotVersion); err != nil {
			return rollback(fmt.Errorf("freeze divergent projection lot: %w", err))
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO auction_reconcile_findings (kind, lot_id, severity, detail_json, detected_at_ms)
SELECT ?, ?, ?, ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM auction_reconcile_findings
  WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0
)`, kind, projection.LotID, severity, detail, reconciler.now().UnixMilli(), kind, projection.LotID); err != nil {
		return rollback(fmt.Errorf("insert runtime reconcile finding: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE auction_reconcile_findings
SET severity = CASE WHEN severity = 'P0' THEN severity ELSE ? END,
    detail_json = ?
WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0`, severity, detail, kind, projection.LotID); err != nil {
		return rollback(fmt.Errorf("update runtime reconcile finding: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runtime reconcile finding: %w", err)
	}
	return nil
}

func (reconciler *RuntimeReconciler) unlocatedSeverity(ctx context.Context, lotID string) (string, error) {
	var detectedAt sql.NullInt64
	err := reconciler.db.QueryRowContext(ctx, `
SELECT MIN(detected_at_ms)
FROM auction_reconcile_findings
WHERE kind = ? AND lot_id = ? AND resolved_at_ms = 0`, runtimeFindingEventMissing, lotID).Scan(&detectedAt)
	if err != nil {
		return "", fmt.Errorf("read unresolved runtime event finding: %w", err)
	}
	if detectedAt.Valid && reconciler.now().Sub(time.UnixMilli(detectedAt.Int64)) >= runtimeUnlocatedEscalation {
		return runtimeFindingSeverityP0, nil
	}
	return runtimeFindingSeverityP1, nil
}

func (reconciler *RuntimeReconciler) resolveRuntimeFindings(ctx context.Context, lotID string) error {
	resolution, err := json.Marshal(map[string]any{"status": "identity_match", "resolved_by": "runtime_reconciler"})
	if err != nil {
		return err
	}
	_, err = reconciler.db.ExecContext(ctx, `
UPDATE auction_reconcile_findings
SET resolved_at_ms = ?, resolution_json = ?
WHERE lot_id = ? AND resolved_at_ms = 0 AND kind IN (?, ?, ?, ?)`,
		reconciler.now().UnixMilli(), resolution, lotID,
		runtimeFindingStateDiverged, runtimeFindingEventMissing, runtimeFindingStateMissing, runtimeFindingProjectionLag,
	)
	return err
}

func runtimeCoreFromState(state *v1.LotRuntimeStateV1) runtimeCoreState {
	if state == nil {
		return runtimeCoreState{}
	}
	return runtimeCoreState{
		Status: int32(state.GetStatus()), CurrentPriceFen: state.GetCurrentPriceFen(),
		WinnerUserID: state.GetWinnerUserId(), EndsAtUnixMs: state.GetEndsAtUnixMs(),
	}
}

func runtimeOutboxShardFor(lotID string, shardCount int) int {
	store := &Store{outboxShards: shardCount}
	return store.runtimeOutboxShard(lotID)
}
