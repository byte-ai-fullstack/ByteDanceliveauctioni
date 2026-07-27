package searchindex

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

var (
	ErrVectorSchemaUnavailable = errors.New("pgvector search schema is unavailable")
	ErrInvalidVectorDocument   = errors.New("invalid pgvector lot document")
	ErrVectorEmbeddingRequired = errors.New("pgvector embedding is required")
	ErrVectorVersionConflict   = errors.New("pgvector lot version identity conflict")
)

const DefaultPGVectorTable = "auction_lot_search_docs"

var pgVectorTablePattern = regexp.MustCompile(`^auction_lot_search_docs(?:_(?:v[1-9][0-9]*|backup_[0-9]{8}_[0-9]{6}))?$`)

type PGVectorConfig struct {
	DSN                   string
	TableName             string
	EmbeddingProvider     string
	EmbeddingModel        string
	EmbeddingModelVersion string
	EmbeddingDimensions   int
	MaxOpenConns          int
	MaxIdleConns          int
	ConnMaxLifetime       time.Duration
	ConnMaxIdleTime       time.Duration
}

type PGVectorIndex struct {
	db           *sql.DB
	table        string
	provider     string
	model        string
	modelVersion string
	dimensions   int
}

type VectorDocumentState struct {
	Found         bool
	LotVersion    int64
	LastEventID   string
	ContentHash   string
	EmbeddingHash string
	HasEmbedding  bool
}

type VectorApplyResult struct {
	Applied   bool
	Duplicate bool
	Stale     bool
}

func NewPGVectorIndex(ctx context.Context, cfg PGVectorConfig) (*PGVectorIndex, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("pgvector dsn is required")
	}
	table, err := normalizePGVectorTableName(cfg.TableName)
	if err != nil {
		return nil, err
	}
	cfg.EmbeddingProvider = strings.ToLower(strings.TrimSpace(cfg.EmbeddingProvider))
	if cfg.EmbeddingProvider == "" {
		cfg.EmbeddingProvider = "dashscope"
	}
	cfg.EmbeddingModel = strings.TrimSpace(cfg.EmbeddingModel)
	if cfg.EmbeddingModel == "" {
		cfg.EmbeddingModel = DefaultEmbeddingModel
	}
	cfg.EmbeddingModelVersion = strings.TrimSpace(cfg.EmbeddingModelVersion)
	if cfg.EmbeddingModelVersion == "" {
		cfg.EmbeddingModelVersion = cfg.EmbeddingModel
	}
	cfg.EmbeddingDimensions = NormalizeDimensions(cfg.EmbeddingDimensions)
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = 5
	}
	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = 2
	}
	if cfg.MaxIdleConns > cfg.MaxOpenConns {
		return nil, errors.New("pgvector max idle connections cannot exceed max open connections")
	}
	if cfg.ConnMaxLifetime <= 0 {
		cfg.ConnMaxLifetime = 30 * time.Minute
	}
	if cfg.ConnMaxIdleTime <= 0 {
		cfg.ConnMaxIdleTime = 2 * time.Minute
	}
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open pgvector: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping pgvector: %w", err)
	}
	index := &PGVectorIndex{
		db: db, table: table, provider: cfg.EmbeddingProvider, model: cfg.EmbeddingModel,
		modelVersion: cfg.EmbeddingModelVersion, dimensions: cfg.EmbeddingDimensions,
	}
	if err := index.verifySchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return index, nil
}

func (i *PGVectorIndex) verifySchema(ctx context.Context) error {
	if i == nil || i.db == nil {
		return ErrVectorSchemaUnavailable
	}
	return verifyVectorSchema(ctx, i.db, i.TableName())
}

type vectorSchemaQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func verifyVectorSchema(ctx context.Context, queryer vectorSchemaQueryer, table string) error {
	rows, err := queryer.QueryContext(ctx, fmt.Sprintf(`
SELECT lot_id, room_id, main_account_id, title, description, category, tags, image_url,
       search_text, status, start_price_fen, current_price_fen, currency,
       starts_at_unix_ms, ends_at_unix_ms, href, public_visible,
       lot_version, last_event_id, content_hash,
       embedding_provider, embedding_model, embedding_model_version,
       embedding_dimensions, embedding_hash, embedding IS NOT NULL, indexed_at
FROM %s
WHERE FALSE`, quotePGIdentifier(table)))
	if err != nil {
		return fmt.Errorf("%w: run deploy/postgres/migrations before starting: %v", ErrVectorSchemaUnavailable, err)
	}
	return rows.Close()
}

func (i *PGVectorIndex) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}

func (i *PGVectorIndex) Provider() string {
	if i == nil || i.provider == "" {
		return "dashscope"
	}
	return i.provider
}

func (i *PGVectorIndex) Model() string {
	if i == nil || i.model == "" {
		return DefaultEmbeddingModel
	}
	return i.model
}

func (i *PGVectorIndex) ModelVersion() string {
	if i == nil || i.modelVersion == "" {
		return i.Model()
	}
	return i.modelVersion
}

func (i *PGVectorIndex) Dimensions() int {
	if i == nil {
		return DefaultEmbeddingDimensions
	}
	return i.dimensions
}

func (i *PGVectorIndex) TableName() string {
	if i == nil || i.table == "" {
		return DefaultPGVectorTable
	}
	return i.table
}

func (i *PGVectorIndex) DocumentState(ctx context.Context, lotID string) (VectorDocumentState, error) {
	if i == nil || i.db == nil {
		return VectorDocumentState{}, errors.New("pgvector index is not initialized")
	}
	lotID = strings.TrimSpace(lotID)
	if lotID == "" || len(lotID) > 64 {
		return VectorDocumentState{}, fmt.Errorf("%w: lot_id is invalid", ErrInvalidVectorDocument)
	}
	return readVectorDocumentState(ctx, i.db, i.TableName(), lotID, "")
}

type vectorStateQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readVectorDocumentState(ctx context.Context, queryer vectorStateQueryer, table, lotID, lockClause string) (VectorDocumentState, error) {
	var state VectorDocumentState
	err := queryer.QueryRowContext(ctx, fmt.Sprintf(`
SELECT lot_version, last_event_id, content_hash, embedding_hash, embedding IS NOT NULL
FROM %s
WHERE lot_id = $1`, quotePGIdentifier(table))+lockClause, lotID).Scan(
		&state.LotVersion, &state.LastEventID, &state.ContentHash, &state.EmbeddingHash, &state.HasEmbedding,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return VectorDocumentState{}, nil
	}
	if err != nil {
		return VectorDocumentState{}, fmt.Errorf("read pgvector document state: %w", err)
	}
	state.Found = true
	return state, nil
}

func (i *PGVectorIndex) ApplyDocument(ctx context.Context, document LotDocument, embedding []float64) (VectorApplyResult, error) {
	if i == nil || i.db == nil {
		return VectorApplyResult{}, errors.New("pgvector index is not initialized")
	}
	if err := i.prepareDocument(&document, embedding); err != nil {
		return VectorApplyResult{}, err
	}
	tx, err := i.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return VectorApplyResult{}, fmt.Errorf("begin pgvector apply: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readVectorDocumentState(ctx, tx, i.TableName(), document.LotID, " FOR UPDATE")
	if err != nil {
		return VectorApplyResult{}, err
	}
	if state.Found {
		if document.LotVersion < state.LotVersion {
			if err := tx.Commit(); err != nil {
				return VectorApplyResult{}, fmt.Errorf("commit stale pgvector apply: %w", err)
			}
			return VectorApplyResult{Stale: true}, nil
		}
		if document.LotVersion == state.LotVersion {
			if document.LastEventID != state.LastEventID || document.ContentHash != state.ContentHash {
				return VectorApplyResult{}, fmt.Errorf("%w: lot_id=%s version=%d", ErrVectorVersionConflict, document.LotID, document.LotVersion)
			}
			if document.EmbeddingHash == state.EmbeddingHash && (!document.PublicVisible || state.HasEmbedding) {
				if err := tx.Commit(); err != nil {
					return VectorApplyResult{}, fmt.Errorf("commit duplicate pgvector apply: %w", err)
				}
				return VectorApplyResult{Duplicate: true}, nil
			}
		}
	}
	vector := VectorLiteral(embedding)
	preserveEmbedding := state.Found && state.HasEmbedding && state.EmbeddingHash == document.EmbeddingHash && vector == ""
	if document.PublicVisible && vector == "" && !preserveEmbedding {
		return VectorApplyResult{}, fmt.Errorf("%w: lot_id=%s embedding_hash=%s", ErrVectorEmbeddingRequired, document.LotID, document.EmbeddingHash)
	}
	var vectorValue any
	if vector != "" {
		vectorValue = vector
	}
	if !state.Found {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (
  lot_id, room_id, main_account_id, title, description, category, tags, image_url,
  search_text, status, start_price_fen, current_price_fen, currency,
  starts_at_unix_ms, ends_at_unix_ms, href, public_visible,
  lot_version, last_event_id, content_hash,
  embedding_provider, embedding_model, embedding_model_version,
  embedding_dimensions, embedding_hash, embedding, indexed_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7::jsonb, $8,
  $9, $10, $11, $12, $13,
  $14, $15, $16, $17,
  $18, $19, $20,
  $21, $22, $23,
  $24, $25, $26::vector, NOW()
)`, quotePGIdentifier(i.TableName())), vectorDocumentArguments(document, vectorValue)...)
	} else {
		args := vectorDocumentArguments(document, vectorValue)
		args = append(args, preserveEmbedding)
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`
UPDATE %s SET
  room_id=$2, main_account_id=$3, title=$4, description=$5, category=$6, tags=$7::jsonb, image_url=$8,
  search_text=$9, status=$10, start_price_fen=$11, current_price_fen=$12, currency=$13,
  starts_at_unix_ms=$14, ends_at_unix_ms=$15, href=$16, public_visible=$17,
  lot_version=$18, last_event_id=$19, content_hash=$20,
  embedding_provider=$21, embedding_model=$22, embedding_model_version=$23,
  embedding_dimensions=$24, embedding_hash=$25,
  embedding=CASE WHEN $27 THEN embedding ELSE $26::vector END,
  indexed_at=NOW()
WHERE lot_id=$1`, quotePGIdentifier(i.TableName())), args...)
	}
	if err != nil {
		return VectorApplyResult{}, fmt.Errorf("write pgvector document: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return VectorApplyResult{}, fmt.Errorf("commit pgvector apply: %w", err)
	}
	return VectorApplyResult{Applied: true}, nil
}

func (i *PGVectorIndex) prepareDocument(document *LotDocument, embedding []float64) error {
	if document == nil || !validVectorText(document.LotID, 64) || !validVectorText(document.RoomID, 64) ||
		!validVectorText(document.MainAccountID, 64) || !validVectorText(document.Title, 255) || len(document.Description) > 65_535 ||
		len(document.Category) > 64 || len(document.ImageURL) > 1024 || len(document.SearchText) > 1<<20 || !validVectorText(document.Status, 64) ||
		document.LotVersion <= 0 || eventcontract.ValidateEventID(document.LastEventID) != nil || !validSHA256Text(document.ContentHash) {
		return fmt.Errorf("%w: document identity, version, content, or bounds are invalid", ErrInvalidVectorDocument)
	}
	if document.StartPrice == nil || document.CurrentPrice == nil || document.StartPrice.GetAmount() < 0 ||
		document.CurrentPrice.GetAmount() < document.StartPrice.GetAmount() || document.StartPrice.GetCurrency() != document.CurrentPrice.GetCurrency() ||
		!validVectorCurrency(document.StartPrice.GetCurrency()) || document.StartsAtUnixMs < 0 || document.EndsAtUnixMs < 0 {
		return fmt.Errorf("%w: money or time fields are invalid", ErrInvalidVectorDocument)
	}
	document.EmbeddingProvider = i.Provider()
	document.EmbeddingModel = i.Model()
	document.EmbeddingModelVersion = i.ModelVersion()
	document.EmbeddingDimensions = i.Dimensions()
	document.EmbeddingHash = document.StableEmbeddingHash(
		document.EmbeddingProvider, document.EmbeddingModel, document.EmbeddingModelVersion, document.EmbeddingDimensions,
	)
	if len(embedding) > 0 {
		if len(embedding) != i.Dimensions() {
			return fmt.Errorf("%w: embedding dimensions got=%d want=%d", ErrInvalidVectorDocument, len(embedding), i.Dimensions())
		}
		for _, value := range embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%w: embedding contains a non-finite value", ErrInvalidVectorDocument)
			}
		}
	}
	return nil
}

func vectorDocumentArguments(document LotDocument, vector any) []any {
	tags, _ := json.Marshal(document.Tags)
	return []any{
		document.LotID, document.RoomID, document.MainAccountID, document.Title, document.Description, document.Category, string(tags), document.ImageURL,
		document.SearchText, document.Status, document.StartPrice.GetAmount(), document.CurrentPrice.GetAmount(), document.CurrentPrice.GetCurrency(),
		document.StartsAtUnixMs, document.EndsAtUnixMs, document.Href, document.PublicVisible,
		document.LotVersion, document.LastEventID, document.ContentHash,
		document.EmbeddingProvider, document.EmbeddingModel, document.EmbeddingModelVersion,
		document.EmbeddingDimensions, document.EmbeddingHash, vector,
	}
}

// CompatibleEmbedding returns a paid embedding that can be reused for the
// exact model/content identity. Rebuild jobs use it to avoid charging again
// for unchanged documents.
func (i *PGVectorIndex) CompatibleEmbedding(ctx context.Context, lotID, embeddingHash string) ([]float64, bool, error) {
	if i == nil || i.db == nil {
		return nil, false, errors.New("pgvector index is not initialized")
	}
	lotID = strings.TrimSpace(lotID)
	if !validVectorText(lotID, 64) || !validSHA256Text(embeddingHash) {
		return nil, false, fmt.Errorf("%w: reusable embedding identity is invalid", ErrInvalidVectorDocument)
	}
	var literal string
	err := i.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT embedding::text
FROM %s
WHERE lot_id = $1 AND embedding_hash = $2 AND embedding IS NOT NULL`, quotePGIdentifier(i.TableName())), lotID, embeddingHash).Scan(&literal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read reusable pgvector embedding: %w", err)
	}
	vector, err := parseVectorLiteral(literal, i.Dimensions())
	if err != nil {
		return nil, false, err
	}
	return vector, true, nil
}

func (i *PGVectorIndex) HasCompatibleEmbedding(ctx context.Context, lotID, embeddingHash string) (bool, error) {
	if i == nil || i.db == nil {
		return false, errors.New("pgvector index is not initialized")
	}
	lotID = strings.TrimSpace(lotID)
	if !validVectorText(lotID, 64) || !validSHA256Text(embeddingHash) {
		return false, fmt.Errorf("%w: reusable embedding identity is invalid", ErrInvalidVectorDocument)
	}
	var reusable bool
	if err := i.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT EXISTS (
  SELECT 1 FROM %s
  WHERE lot_id = $1 AND embedding_hash = $2 AND embedding IS NOT NULL
)`, quotePGIdentifier(i.TableName())), lotID, embeddingHash).Scan(&reusable); err != nil {
		return false, fmt.Errorf("check reusable pgvector embedding: %w", err)
	}
	return reusable, nil
}

func (i *PGVectorIndex) EnsureRebuildTable(ctx context.Context, target string, resume bool) (bool, error) {
	if i == nil || i.db == nil || i.TableName() != DefaultPGVectorTable {
		return false, errors.New("pgvector rebuild table manager must use the canonical table")
	}
	target, err := normalizePGVectorTableName(target)
	if err != nil || !strings.HasPrefix(target, DefaultPGVectorTable+"_v") {
		return false, errors.New("pgvector rebuild target must match auction_lot_search_docs_v<N>")
	}
	tx, err := i.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, fmt.Errorf("begin pgvector rebuild table setup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('auction_search_rebuild'))`); err != nil {
		return false, fmt.Errorf("lock pgvector rebuild table setup: %w", err)
	}
	exists, err := vectorTableExists(ctx, tx, target)
	if err != nil {
		return false, err
	}
	if exists {
		if !resume {
			return false, fmt.Errorf("pgvector rebuild target %s already exists; use --resume or choose a new version", target)
		}
		if err := verifyVectorSchema(ctx, tx, target); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit pgvector rebuild target validation: %w", err)
		}
		return false, nil
	}
	if resume {
		return false, fmt.Errorf("pgvector rebuild target %s does not exist and cannot be resumed", target)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s (LIKE %s INCLUDING ALL)`, quotePGIdentifier(target), quotePGIdentifier(DefaultPGVectorTable))); err != nil {
		return false, fmt.Errorf("create pgvector rebuild target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit pgvector rebuild target creation: %w", err)
	}
	return true, nil
}

// SwitchRebuildTable atomically replaces the canonical relation and retains
// the previous table as a timestamped rollback copy. Running readers/writers
// either resolve the old or the new relation; Kafka catch-up repairs writes
// that committed to the old relation during the lock handoff.
func (i *PGVectorIndex) SwitchRebuildTable(ctx context.Context, target, backup string) error {
	if i == nil || i.db == nil || i.TableName() != DefaultPGVectorTable {
		return errors.New("pgvector rebuild table manager must use the canonical table")
	}
	target, err := normalizePGVectorTableName(target)
	if err != nil || !strings.HasPrefix(target, DefaultPGVectorTable+"_v") {
		return errors.New("pgvector rebuild target must match auction_lot_search_docs_v<N>")
	}
	backup, err = normalizePGVectorTableName(backup)
	if err != nil || !strings.HasPrefix(backup, DefaultPGVectorTable+"_backup_") {
		return errors.New("pgvector backup must match auction_lot_search_docs_backup_YYYYMMDD_HHMMSS")
	}
	tx, err := i.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin pgvector rebuild switch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('auction_search_rebuild'))`); err != nil {
		return fmt.Errorf("lock pgvector rebuild switch: %w", err)
	}
	targetExists, err := vectorTableExists(ctx, tx, target)
	if err != nil {
		return err
	}
	backupExists, err := vectorTableExists(ctx, tx, backup)
	if err != nil {
		return err
	}
	if !targetExists || backupExists {
		return fmt.Errorf("pgvector switch requires target present and backup absent: target=%t backup=%t", targetExists, backupExists)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`LOCK TABLE %s, %s IN ACCESS EXCLUSIVE MODE`, quotePGIdentifier(DefaultPGVectorTable), quotePGIdentifier(target))); err != nil {
		return fmt.Errorf("lock pgvector tables for switch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, quotePGIdentifier(DefaultPGVectorTable), quotePGIdentifier(backup))); err != nil {
		return fmt.Errorf("rename canonical pgvector table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, quotePGIdentifier(target), quotePGIdentifier(DefaultPGVectorTable))); err != nil {
		return fmt.Errorf("promote pgvector rebuild target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pgvector rebuild switch: %w", err)
	}
	return nil
}

func (i *PGVectorIndex) CountDocuments(ctx context.Context) (int64, error) {
	if i == nil || i.db == nil {
		return 0, errors.New("pgvector index is not initialized")
	}
	var count int64
	if err := i.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quotePGIdentifier(i.TableName()))).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pgvector documents: %w", err)
	}
	return count, nil
}

type vectorTableQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func vectorTableExists(ctx context.Context, queryer vectorTableQueryer, table string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		return false, fmt.Errorf("check pgvector table %s: %w", table, err)
	}
	return exists, nil
}

func normalizePGVectorTableName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultPGVectorTable, nil
	}
	if len(value) > 63 || !pgVectorTablePattern.MatchString(value) {
		return "", errors.New("pgvector table name is invalid")
	}
	return value, nil
}

func quotePGIdentifier(value string) string {
	return `"` + value + `"`
}

func parseVectorLiteral(literal string, dimensions int) ([]float64, error) {
	literal = strings.TrimSpace(literal)
	if len(literal) < 2 || literal[0] != '[' || literal[len(literal)-1] != ']' {
		return nil, fmt.Errorf("%w: stored embedding literal is invalid", ErrInvalidVectorDocument)
	}
	parts := strings.Split(literal[1:len(literal)-1], ",")
	if len(parts) != dimensions {
		return nil, fmt.Errorf("%w: stored embedding dimensions got=%d want=%d", ErrInvalidVectorDocument, len(parts), dimensions)
	}
	vector := make([]float64, len(parts))
	for index, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("%w: stored embedding contains an invalid value", ErrInvalidVectorDocument)
		}
		vector[index] = value
	}
	return vector, nil
}

func (i *PGVectorIndex) PurgeHiddenOlderThan(ctx context.Context, retention time.Duration) (int64, error) {
	if i == nil || i.db == nil {
		return 0, errors.New("pgvector index is not initialized")
	}
	if retention <= 0 {
		return 0, nil
	}
	result, err := i.db.ExecContext(ctx, fmt.Sprintf(`
DELETE FROM %s
WHERE public_visible = FALSE AND indexed_at < $1`, quotePGIdentifier(i.TableName())), time.Now().Add(-retention))
	if err != nil {
		return 0, fmt.Errorf("purge hidden pgvector documents: %w", err)
	}
	return result.RowsAffected()
}

func (i *PGVectorIndex) Search(ctx context.Context, query SearchQuery) ([]LotDocument, error) {
	if i == nil || i.db == nil {
		return nil, errors.New("pgvector index is not initialized")
	}
	vector := VectorLiteral(query.Vector)
	if vector == "" || len(query.Vector) != i.Dimensions() {
		return nil, errors.New("query vector has invalid dimensions")
	}
	limit := NormalizeLimit(query.Limit, DefaultSearchLimit)
	rows, err := i.db.QueryContext(ctx, fmt.Sprintf(`
SELECT lot_id
FROM %s
WHERE public_visible = TRUE AND embedding IS NOT NULL
  AND embedding_provider = $2 AND embedding_model = $3 AND embedding_model_version = $4 AND embedding_dimensions = $5
  AND ($6 = '' OR room_id = $6) AND ($7 = '' OR lot_id = $7)
ORDER BY embedding <=> $1::vector
LIMIT $8`, quotePGIdentifier(i.TableName())), vector, i.Provider(), i.Model(), i.ModelVersion(), i.Dimensions(), strings.TrimSpace(query.RoomID), strings.TrimSpace(query.LotID), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanVectorCandidateIDs(rows)
}

func (i *PGVectorIndex) RandomPublicDocuments(ctx context.Context, limit int) ([]LotDocument, error) {
	if i == nil || i.db == nil {
		return nil, errors.New("pgvector index is not initialized")
	}
	limit = NormalizeLimit(limit, DefaultSearchLimit)
	rows, err := i.db.QueryContext(ctx, fmt.Sprintf(`
SELECT lot_id, room_id, main_account_id, title, description, category, tags, image_url,
       search_text, status, start_price_fen, current_price_fen, currency,
       starts_at_unix_ms, ends_at_unix_ms, href, public_visible,
       lot_version, last_event_id, content_hash,
       embedding_provider, embedding_model, embedding_model_version, embedding_dimensions, embedding_hash
FROM %s
WHERE public_visible = TRUE AND embedding IS NOT NULL
  AND embedding_provider = $1 AND embedding_model = $2 AND embedding_model_version = $3 AND embedding_dimensions = $4
  AND status IN ('LOT_STATUS_QUEUED', 'LOT_STATUS_LIVE', 'LOT_STATUS_EXTENDED')
ORDER BY random()
LIMIT $5`, quotePGIdentifier(i.TableName())), i.Provider(), i.Model(), i.ModelVersion(), i.Dimensions(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanVectorDocuments(rows)
}

type vectorRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanVectorCandidateIDs(rows vectorRows) ([]LotDocument, error) {
	out := make([]LotDocument, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var lotID string
		if err := rows.Scan(&lotID); err != nil {
			return nil, err
		}
		lotID = strings.TrimSpace(lotID)
		if !validVectorText(lotID, 64) {
			return nil, fmt.Errorf("%w: query returned an invalid lot_id", ErrInvalidVectorDocument)
		}
		if _, duplicate := seen[lotID]; duplicate {
			return nil, fmt.Errorf("%w: query returned duplicate lot_id %s", ErrInvalidVectorDocument, lotID)
		}
		seen[lotID] = struct{}{}
		out = append(out, LotDocument{LotID: lotID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanVectorDocuments(rows vectorRows) ([]LotDocument, error) {
	out := make([]LotDocument, 0)
	for rows.Next() {
		var document LotDocument
		var tags []byte
		var startAmount, currentAmount int64
		var currency string
		if err := rows.Scan(
			&document.LotID, &document.RoomID, &document.MainAccountID, &document.Title, &document.Description,
			&document.Category, &tags, &document.ImageURL, &document.SearchText, &document.Status,
			&startAmount, &currentAmount, &currency, &document.StartsAtUnixMs, &document.EndsAtUnixMs,
			&document.Href, &document.PublicVisible, &document.LotVersion, &document.LastEventID, &document.ContentHash,
			&document.EmbeddingProvider, &document.EmbeddingModel, &document.EmbeddingModelVersion,
			&document.EmbeddingDimensions, &document.EmbeddingHash,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(tags, &document.Tags); err != nil {
			return nil, fmt.Errorf("decode pgvector document tags: %w", err)
		}
		document.StartPrice = &v1.Money{Amount: startAmount, Currency: currency}
		document.CurrentPrice = &v1.Money{Amount: currentAmount, Currency: currency}
		out = append(out, document)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func validVectorText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func validSHA256Text(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validVectorCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}
