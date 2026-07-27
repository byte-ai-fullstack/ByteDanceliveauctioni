package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/mysqlschema"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/searchrebuild"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

var esTargetPattern = regexp.MustCompile(`^auction-lots-v[1-9][0-9]*$`)
var pgvectorTargetPattern = regexp.MustCompile(`^auction_lot_search_docs_v[1-9][0-9]*$`)
var pgvectorBackupPattern = regexp.MustCompile(`^auction_lot_search_docs_backup_[0-9]{8}_[0-9]{6}$`)

type rebuildConfig struct {
	Sink             string
	Target           string
	MappingFile      string
	DryRun           bool
	Resume           bool
	SwitchAlias      bool
	SwitchTable      bool
	Backup           string
	PageSize         int
	MaxDocuments     int64
	MaxNewEmbeddings int64
	SampleSize       int
	OperationTimeout time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	config, err := parseRebuildConfig(os.Args[1:], os.Getenv)
	if err == nil {
		err = run(ctx, os.Getenv, config, logger)
	}
	if err != nil {
		logger.Error("search rebuild failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func parseRebuildConfig(args []string, getenv func(string) string) (rebuildConfig, error) {
	if getenv == nil {
		return rebuildConfig{}, errors.New("search rebuild environment reader is required")
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_SEARCH_REBUILD_OPERATION_TIMEOUT", 15*time.Second)
	if err != nil {
		return rebuildConfig{}, err
	}
	config := rebuildConfig{Sink: "es", SwitchAlias: true, SwitchTable: true, PageSize: 500, SampleSize: 32, OperationTimeout: operationTimeout}
	flags := flag.NewFlagSet("search-rebuild", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.Sink, "sink", config.Sink, "search sink: es or pgvector")
	flags.StringVar(&config.Target, "target", "", "versioned sink target")
	flags.StringVar(&config.MappingFile, "mapping", strings.TrimSpace(getenv("AUCTION_ES_MAPPING_FILE")), "Elasticsearch index definition file")
	flags.BoolVar(&config.DryRun, "dry-run", false, "validate Kafka/MySQL state without writing an index")
	flags.BoolVar(&config.Resume, "resume", false, "resume an existing target with external-version idempotency")
	flags.BoolVar(&config.SwitchAlias, "switch-alias", true, "atomically switch auction-lots-current after validation")
	flags.BoolVar(&config.SwitchTable, "switch-table", true, "atomically promote the pgvector table after validation")
	flags.StringVar(&config.Backup, "backup", "", "optional pgvector backup table name")
	flags.IntVar(&config.PageSize, "page-size", config.PageSize, "MySQL keyset page size")
	flags.Int64Var(&config.MaxDocuments, "max-documents", 0, "optional safety cap; zero is unlimited")
	flags.Int64Var(&config.MaxNewEmbeddings, "max-new-embeddings", 0, "hard cap on paid pgvector embeddings; zero permits reuse only")
	flags.IntVar(&config.SampleSize, "sample-size", config.SampleSize, "number of document identities to verify")
	if err := flags.Parse(args); err != nil {
		return rebuildConfig{}, err
	}
	if flags.NArg() != 0 {
		return rebuildConfig{}, errors.New("search rebuild does not accept positional arguments")
	}
	config.Sink = strings.ToLower(strings.TrimSpace(config.Sink))
	config.Target = strings.TrimSpace(config.Target)
	config.MappingFile = strings.TrimSpace(config.MappingFile)
	config.Backup = strings.TrimSpace(config.Backup)
	if config.Sink != "es" && config.Sink != "pgvector" {
		return rebuildConfig{}, errors.New("search rebuild --sink must be es or pgvector")
	}
	if config.Backup != "" && (config.Sink != "pgvector" || len(config.Backup) > 63 || !pgvectorBackupPattern.MatchString(config.Backup)) {
		return rebuildConfig{}, errors.New("--backup is only valid for pgvector and must match auction_lot_search_docs_backup_YYYYMMDD_HHMMSS")
	}
	if config.PageSize <= 0 || config.PageSize > 1000 || config.SampleSize <= 0 || config.SampleSize > 1000 || config.MaxDocuments < 0 || config.MaxNewEmbeddings < 0 {
		return rebuildConfig{}, errors.New("search rebuild page, sample, or document limit is invalid")
	}
	if !config.DryRun {
		switch config.Sink {
		case "es":
			if !esTargetPattern.MatchString(config.Target) {
				return rebuildConfig{}, errors.New("--target must match auction-lots-v<N>")
			}
			if config.MappingFile == "" {
				return rebuildConfig{}, errors.New("--mapping or AUCTION_ES_MAPPING_FILE is required")
			}
		case "pgvector":
			if len(config.Target) > 63 || !pgvectorTargetPattern.MatchString(config.Target) {
				return rebuildConfig{}, errors.New("--target must match auction_lot_search_docs_v<N>")
			}
		}
	}
	return config, nil
}

func run(ctx context.Context, getenv func(string) string, config rebuildConfig, logger *slog.Logger) error {
	if getenv == nil || logger == nil {
		return errors.New("search rebuild environment reader and logger are required")
	}
	startupTimeout, err := durationSetting(getenv, "AUCTION_SEARCH_REBUILD_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()
	db, err := openMySQL(startupCtx, getenv)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Warn("close search rebuild MySQL", slog.Any("error", closeErr))
		}
	}()
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "search-rebuild-admin")
	if err != nil {
		return err
	}
	admin, err := newKafkaAdmin(startupCtx, kafkaConfig)
	if err != nil {
		return err
	}
	defer admin.Close()
	initialBounds, err := searchrebuild.ReadLotStateBounds(startupCtx, admin)
	if err != nil {
		return err
	}
	starts := searchrebuild.LatestOffsets(initialBounds)
	logger.Info("search rebuild watermark captured", slog.Any("offsets", starts))
	if config.Sink == "pgvector" {
		return runPGVectorRebuild(ctx, getenv, config, logger, db, admin, kafkaConfig, starts)
	}

	var index *searchindex.ElasticsearchIndex
	if !config.DryRun {
		definition, err := os.ReadFile(config.MappingFile)
		if err != nil {
			return fmt.Errorf("read Elasticsearch mapping: %w", err)
		}
		index, err = newElasticsearchIndex(startupCtx, getenv)
		if err != nil {
			return err
		}
		created, err := index.EnsureVersionedIndex(startupCtx, config.Target, definition, config.Resume)
		if err != nil {
			return err
		}
		logger.Info("Elasticsearch rebuild target ready", slog.String("target", config.Target), slog.Bool("created", created), slog.Bool("resume", config.Resume))
	}

	snapshot, err := searchrebuild.BeginSnapshot(ctx, db)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			logger.Warn("close Elasticsearch rebuild snapshot", slog.Any("error", closeErr))
		}
	}()
	var snapshotDocuments, skippedDrafts int64
	samples := make([]searchindex.LotDocument, 0, config.SampleSize)
	for {
		page, err := snapshot.Next(ctx, config.PageSize)
		if err != nil {
			return err
		}
		skippedDrafts += int64(page.SkippedDraft)
		for _, document := range page.Documents {
			snapshotDocuments++
			if config.MaxDocuments > 0 && snapshotDocuments > config.MaxDocuments {
				return fmt.Errorf("search rebuild exceeded --max-documents=%d", config.MaxDocuments)
			}
			captureSample(&samples, config.SampleSize, snapshotDocuments, document)
			if config.DryRun {
				continue
			}
			if err := applyElasticsearchDocument(ctx, config.OperationTimeout, index, config.Target, document); err != nil {
				return err
			}
		}
		logger.Info("search rebuild snapshot page complete",
			slog.String("last_lot_id", page.LastLotID), slog.Int64("documents", snapshotDocuments), slog.Int64("skipped_drafts", skippedDrafts),
		)
		if page.Done {
			break
		}
	}
	if err := snapshot.Commit(); err != nil {
		return err
	}
	if config.DryRun {
		logger.Info("search rebuild dry run complete", slog.Int64("documents", snapshotDocuments), slog.Int64("skipped_drafts", skippedDrafts))
		return nil
	}

	currentBounds, err := searchrebuild.ReadLotStateBounds(ctx, admin)
	if err != nil {
		return err
	}
	if err := searchrebuild.ValidateCatchupStarts(starts, currentBounds); err != nil {
		return err
	}
	catchupConfig := kafkaConfig
	catchupConfig.ClientID = boundedClientID(kafkaConfig.ClientID + "-catchup")
	catchup, err := searchrebuild.NewKafkaCatchup(ctx, catchupConfig, starts)
	if err != nil {
		return err
	}
	defer catchup.Close()
	applyRecord := func(applyCtx context.Context, record searchstate.Record) error {
		return applyElasticsearchDocument(applyCtx, config.OperationTimeout, index, config.Target, record.Document)
	}
	catchupApplied, err := catchup.CatchUpTo(ctx, searchrebuild.LatestOffsets(currentBounds), applyRecord)
	if err != nil {
		return err
	}
	if err := validateElasticsearchTarget(ctx, config.OperationTimeout, index, config.Target, snapshotDocuments, catchupApplied, samples); err != nil {
		return err
	}
	logger.Info("Elasticsearch rebuild caught up", slog.Int64("snapshot_documents", snapshotDocuments), slog.Int64("catchup_records", catchupApplied))
	if !config.SwitchAlias {
		logger.Info("Elasticsearch rebuild target retained without alias switch", slog.String("target", config.Target))
		return nil
	}
	previous, err := switchElasticsearchAlias(ctx, config.OperationTimeout, index, config.Target)
	if err != nil {
		return err
	}
	postSwitchBounds, err := searchrebuild.ReadLotStateBounds(ctx, admin)
	if err != nil {
		return err
	}
	postApplied, err := catchup.CatchUpTo(ctx, searchrebuild.LatestOffsets(postSwitchBounds), applyRecord)
	if err != nil {
		return err
	}
	if err := validateElasticsearchTarget(ctx, config.OperationTimeout, index, config.Target, snapshotDocuments, catchupApplied+postApplied, samples); err != nil {
		return err
	}
	logger.Info("Elasticsearch alias switch complete",
		slog.String("target", config.Target), slog.String("previous", previous), slog.Int64("post_switch_records", postApplied),
		slog.String("old_index_retention", "keep at least 24h and take an SLM snapshot before deletion"),
	)
	return nil
}

var errEmbeddingBudgetExceeded = errors.New("pgvector rebuild paid embedding budget exceeded")

type pgvectorRebuildIndex interface {
	Provider() string
	Model() string
	ModelVersion() string
	Dimensions() int
	DocumentState(ctx context.Context, lotID string) (searchindex.VectorDocumentState, error)
	HasCompatibleEmbedding(ctx context.Context, lotID, embeddingHash string) (bool, error)
	CompatibleEmbedding(ctx context.Context, lotID, embeddingHash string) ([]float64, bool, error)
	ApplyDocument(ctx context.Context, document searchindex.LotDocument, embedding []float64) (searchindex.VectorApplyResult, error)
}

type rebuildEmbedder interface {
	Configured() bool
	Provider() string
	Model() string
	ModelVersion() string
	Dimensions() int
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}

type pgvectorRebuilder struct {
	source           pgvectorRebuildIndex
	target           pgvectorRebuildIndex
	embedder         rebuildEmbedder
	maxNewEmbeddings int64
	newEmbeddings    int64
	reusedEmbeddings int64
}

func (rebuilder *pgvectorRebuilder) Apply(ctx context.Context, document searchindex.LotDocument) error {
	if rebuilder == nil || rebuilder.source == nil || rebuilder.target == nil || rebuilder.embedder == nil {
		return errors.New("pgvector rebuilder is not initialized")
	}
	document.EmbeddingProvider = rebuilder.embedder.Provider()
	document.EmbeddingModel = rebuilder.embedder.Model()
	document.EmbeddingModelVersion = rebuilder.embedder.ModelVersion()
	document.EmbeddingDimensions = rebuilder.embedder.Dimensions()
	document.EmbeddingHash = document.StableEmbeddingHash(
		document.EmbeddingProvider, document.EmbeddingModel, document.EmbeddingModelVersion, document.EmbeddingDimensions,
	)
	state, err := rebuilder.target.DocumentState(ctx, document.LotID)
	if err != nil {
		return err
	}
	if state.Found {
		identityConflict := document.LotVersion == state.LotVersion &&
			(document.LastEventID != state.LastEventID || document.ContentHash != state.ContentHash)
		if document.LotVersion < state.LotVersion || identityConflict ||
			(state.HasEmbedding && state.EmbeddingHash == document.EmbeddingHash) {
			_, err := rebuilder.target.ApplyDocument(ctx, document, nil)
			return err
		}
	}
	if !document.PublicVisible || strings.TrimSpace(document.SearchText) == "" {
		_, err := rebuilder.target.ApplyDocument(ctx, document, nil)
		return err
	}
	embedding, reusable, err := rebuilder.source.CompatibleEmbedding(ctx, document.LotID, document.EmbeddingHash)
	if err != nil {
		return err
	}
	if reusable {
		rebuilder.reusedEmbeddings++
		_, err := rebuilder.target.ApplyDocument(ctx, document, embedding)
		return err
	}
	if rebuilder.newEmbeddings >= rebuilder.maxNewEmbeddings {
		return fmt.Errorf("%w: cap=%d next_lot_id=%s", errEmbeddingBudgetExceeded, rebuilder.maxNewEmbeddings, document.LotID)
	}
	if !rebuilder.embedder.Configured() {
		return errors.New("pgvector rebuild needs a configured embedding client for a document that cannot be reused")
	}
	// Reserve before the request because a provider may bill a request even when
	// the response is lost or invalid. Resume never repeats a successfully stored
	// embedding because target identity is checked first.
	rebuilder.newEmbeddings++
	embeddings, err := rebuilder.embedder.Embed(ctx, []string{document.SearchText})
	if err != nil {
		return fmt.Errorf("request pgvector rebuild embedding for lot_id=%s: %w", document.LotID, err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != rebuilder.embedder.Dimensions() {
		return fmt.Errorf("pgvector rebuild embedding shape is invalid for lot_id=%s", document.LotID)
	}
	_, err = rebuilder.target.ApplyDocument(ctx, document, embeddings[0])
	return err
}

func runPGVectorRebuild(
	ctx context.Context,
	getenv func(string) string,
	config rebuildConfig,
	logger *slog.Logger,
	mysqlDB *sql.DB,
	admin *kgo.Client,
	kafkaConfig kafkaclient.Config,
	starts map[int32]int64,
) error {
	embedder := searchindex.NewEmbeddingClientFromEnv(getenv)
	source, err := newPGVectorIndex(ctx, getenv, embedder, searchindex.DefaultPGVectorTable)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			logger.Warn("close pgvector rebuild source", slog.Any("error", closeErr))
		}
	}()
	var target *searchindex.PGVectorIndex
	if !config.DryRun {
		created, err := source.EnsureRebuildTable(ctx, config.Target, config.Resume)
		if err != nil {
			return err
		}
		target, err = newPGVectorIndex(ctx, getenv, embedder, config.Target)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := target.Close(); closeErr != nil {
				logger.Warn("close pgvector rebuild target", slog.Any("error", closeErr))
			}
		}()
		logger.Info("pgvector rebuild target ready", slog.String("target", config.Target), slog.Bool("created", created), slog.Bool("resume", config.Resume))
	}

	snapshot, err := searchrebuild.BeginSnapshot(ctx, mysqlDB)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			logger.Warn("close pgvector rebuild snapshot", slog.Any("error", closeErr))
		}
	}()
	var snapshotDocuments, skippedDrafts, reusableEstimate, newEstimate int64
	samples := make([]searchindex.LotDocument, 0, config.SampleSize)
	rebuilder := &pgvectorRebuilder{source: source, target: target, embedder: embedder, maxNewEmbeddings: config.MaxNewEmbeddings}
	for {
		page, err := snapshot.Next(ctx, config.PageSize)
		if err != nil {
			return err
		}
		skippedDrafts += int64(page.SkippedDraft)
		for _, document := range page.Documents {
			snapshotDocuments++
			if config.MaxDocuments > 0 && snapshotDocuments > config.MaxDocuments {
				return fmt.Errorf("search rebuild exceeded --max-documents=%d", config.MaxDocuments)
			}
			captureSample(&samples, config.SampleSize, snapshotDocuments, document)
			if config.DryRun {
				if !document.PublicVisible || strings.TrimSpace(document.SearchText) == "" {
					continue
				}
				hash := document.StableEmbeddingHash(embedder.Provider(), embedder.Model(), embedder.ModelVersion(), embedder.Dimensions())
				reusable, err := source.HasCompatibleEmbedding(ctx, document.LotID, hash)
				if err != nil {
					return err
				}
				if reusable {
					reusableEstimate++
				} else {
					newEstimate++
				}
				continue
			}
			operationCtx, cancel := context.WithTimeout(ctx, config.OperationTimeout)
			err := rebuilder.Apply(operationCtx, document)
			cancel()
			if err != nil {
				return err
			}
		}
		logger.Info("pgvector rebuild snapshot page complete",
			slog.String("last_lot_id", page.LastLotID), slog.Int64("documents", snapshotDocuments), slog.Int64("skipped_drafts", skippedDrafts),
			slog.Int64("new_embeddings", rebuilder.newEmbeddings), slog.Int64("reused_embeddings", rebuilder.reusedEmbeddings),
		)
		if page.Done {
			break
		}
	}
	if err := snapshot.Commit(); err != nil {
		return err
	}
	if config.DryRun {
		logger.Info("pgvector rebuild dry run complete",
			slog.Int64("documents", snapshotDocuments), slog.Int64("skipped_drafts", skippedDrafts),
			slog.Int64("reusable_embeddings", reusableEstimate), slog.Int64("new_embeddings_required", newEstimate),
			slog.String("fee_cap_unit", "paid embedding documents"),
		)
		return nil
	}

	currentBounds, err := searchrebuild.ReadLotStateBounds(ctx, admin)
	if err != nil {
		return err
	}
	if err := searchrebuild.ValidateCatchupStarts(starts, currentBounds); err != nil {
		return err
	}
	catchupConfig := kafkaConfig
	catchupConfig.ClientID = boundedClientID(kafkaConfig.ClientID + "-pgvector-catchup")
	catchup, err := searchrebuild.NewKafkaCatchup(ctx, catchupConfig, starts)
	if err != nil {
		return err
	}
	defer catchup.Close()
	applyRecord := func(applyCtx context.Context, record searchstate.Record) error {
		operationCtx, cancel := context.WithTimeout(applyCtx, config.OperationTimeout)
		defer cancel()
		return rebuilder.Apply(operationCtx, record.Document)
	}
	catchupApplied, err := catchup.CatchUpTo(ctx, searchrebuild.LatestOffsets(currentBounds), applyRecord)
	if err != nil {
		return err
	}
	if err := validatePGVectorTarget(ctx, config.OperationTimeout, target, snapshotDocuments, catchupApplied, samples); err != nil {
		return err
	}
	logger.Info("pgvector rebuild caught up",
		slog.Int64("snapshot_documents", snapshotDocuments), slog.Int64("catchup_records", catchupApplied),
		slog.Int64("new_embeddings", rebuilder.newEmbeddings), slog.Int64("reused_embeddings", rebuilder.reusedEmbeddings),
	)
	if !config.SwitchTable {
		logger.Info("pgvector rebuild target retained without table switch", slog.String("target", config.Target))
		return nil
	}
	backup := config.Backup
	if backup == "" {
		backup = searchindex.DefaultPGVectorTable + "_backup_" + time.Now().UTC().Format("20060102_150405")
	}
	switchCtx, cancelSwitch := context.WithTimeout(ctx, config.OperationTimeout)
	err = source.SwitchRebuildTable(switchCtx, config.Target, backup)
	cancelSwitch()
	if err != nil {
		return err
	}
	// The canonical index object resolves the newly promoted relation on each
	// statement, so post-switch replay lands on the live table.
	rebuilder.target = source
	rebuilder.source = source
	postSwitchBounds, err := searchrebuild.ReadLotStateBounds(ctx, admin)
	if err != nil {
		return err
	}
	postApplied, err := catchup.CatchUpTo(ctx, searchrebuild.LatestOffsets(postSwitchBounds), applyRecord)
	if err != nil {
		return err
	}
	if err := validatePGVectorTarget(ctx, config.OperationTimeout, source, snapshotDocuments, catchupApplied+postApplied, samples); err != nil {
		return err
	}
	logger.Info("pgvector table switch complete",
		slog.String("target", searchindex.DefaultPGVectorTable), slog.String("backup", backup), slog.Int64("post_switch_records", postApplied),
		slog.Int64("new_embeddings", rebuilder.newEmbeddings), slog.Int64("reused_embeddings", rebuilder.reusedEmbeddings),
		slog.String("old_table_retention", "keep at least 24h before manual DROP TABLE"),
	)
	return nil
}

func newPGVectorIndex(ctx context.Context, getenv func(string) string, embedder *searchindex.EmbeddingClient, table string) (*searchindex.PGVectorIndex, error) {
	if getenv == nil || embedder == nil {
		return nil, errors.New("pgvector rebuild environment reader and embedder are required")
	}
	dsn := strings.TrimSpace(getenv("AUCTION_SEARCH_PG_DSN"))
	if dsn == "" {
		dsn = "postgres://auction_search:auction_search_dev@127.0.0.1:15432/live_auction_search?sslmode=disable"
	}
	maxOpen, err := intSetting(getenv, "AUCTION_VECTOR_DB_MAX_OPEN_CONNS", 4)
	if err != nil {
		return nil, err
	}
	maxIdle, err := intSetting(getenv, "AUCTION_VECTOR_DB_MAX_IDLE_CONNS", 2)
	if err != nil {
		return nil, err
	}
	if maxIdle > maxOpen {
		return nil, errors.New("AUCTION_VECTOR_DB_MAX_IDLE_CONNS cannot exceed max open connections")
	}
	maxLifetime, err := durationSetting(getenv, "AUCTION_VECTOR_DB_CONN_MAX_LIFETIME", 30*time.Minute)
	if err != nil {
		return nil, err
	}
	maxIdleTime, err := durationSetting(getenv, "AUCTION_VECTOR_DB_CONN_MAX_IDLE_TIME", 2*time.Minute)
	if err != nil {
		return nil, err
	}
	return searchindex.NewPGVectorIndex(ctx, searchindex.PGVectorConfig{
		DSN: dsn, TableName: table, EmbeddingProvider: embedder.Provider(), EmbeddingModel: embedder.Model(),
		EmbeddingModelVersion: embedder.ModelVersion(), EmbeddingDimensions: embedder.Dimensions(),
		MaxOpenConns: maxOpen, MaxIdleConns: maxIdle, ConnMaxLifetime: maxLifetime, ConnMaxIdleTime: maxIdleTime,
	})
}

func validatePGVectorTarget(
	ctx context.Context,
	timeout time.Duration,
	index *searchindex.PGVectorIndex,
	snapshotDocuments, catchupRecords int64,
	samples []searchindex.LotDocument,
) error {
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := index.CountDocuments(operationCtx)
	if err != nil {
		return err
	}
	if count < snapshotDocuments || count > snapshotDocuments+catchupRecords {
		return fmt.Errorf("pgvector rebuild count=%d outside expected range [%d,%d]", count, snapshotDocuments, snapshotDocuments+catchupRecords)
	}
	for _, document := range samples {
		identity, err := index.DocumentState(operationCtx, document.LotID)
		if err != nil {
			return err
		}
		if !identity.Found || identity.LotVersion < document.LotVersion ||
			(identity.LotVersion == document.LotVersion && (identity.LastEventID != document.LastEventID || identity.ContentHash != document.ContentHash)) {
			return fmt.Errorf("pgvector rebuild sample identity mismatch for lot_id=%s", document.LotID)
		}
	}
	return nil
}

func openMySQL(ctx context.Context, getenv func(string) string) (*sql.DB, error) {
	raw := strings.TrimSpace(getenv("AUCTION_MYSQL_DSN"))
	if raw == "" {
		raw = "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"
	}
	config, err := mysql.ParseDSN(raw)
	if err != nil {
		return nil, fmt.Errorf("parse search rebuild MySQL DSN: %w", err)
	}
	if strings.TrimSpace(config.DBName) == "" || config.MultiStatements {
		return nil, errors.New("search rebuild MySQL DSN must name a database and disable multiStatements")
	}
	config.ParseTime = true
	config.RejectReadOnly = true
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping search rebuild MySQL: %w", err)
	}
	verifier, err := mysqlschema.NewVerifier()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifier.VerifyCurrent(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify search rebuild MySQL schema: %w", err)
	}
	return db, nil
}

func newKafkaAdmin(ctx context.Context, config kafkaclient.Config) (*kgo.Client, error) {
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	client, err := kgo.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create search rebuild Kafka admin: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping search rebuild Kafka: %w", err)
	}
	return client, nil
}

func newElasticsearchIndex(ctx context.Context, getenv func(string) string) (*searchindex.ElasticsearchIndex, error) {
	requestTimeout, err := durationSetting(getenv, "AUCTION_ES_REQUEST_TIMEOUT", 5*time.Second)
	if err != nil {
		return nil, err
	}
	maxResponseBytes, err := intSetting(getenv, "AUCTION_ES_MAX_RESPONSE_BYTES", 1<<20)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(getenv("AUCTION_ES_URL"))
	if baseURL == "" {
		baseURL = "http://127.0.0.1:19200"
	}
	alias := strings.TrimSpace(getenv("AUCTION_ES_WRITE_ALIAS"))
	if alias == "" {
		alias = "auction-lots-current"
	}
	return searchindex.NewElasticsearchIndex(ctx, searchindex.ElasticsearchConfig{
		BaseURL: baseURL, Username: strings.TrimSpace(getenv("AUCTION_ES_USERNAME")), Password: getenv("AUCTION_ES_PASSWORD"),
		WriteAlias: alias, RequestTimeout: requestTimeout, MaxResponseBytes: int64(maxResponseBytes),
	})
}

func applyElasticsearchDocument(ctx context.Context, timeout time.Duration, index *searchindex.ElasticsearchIndex, target string, document searchindex.LotDocument) error {
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := index.ApplyDocumentTo(operationCtx, target, document)
	return err
}

func switchElasticsearchAlias(ctx context.Context, timeout time.Duration, index *searchindex.ElasticsearchIndex, target string) (string, error) {
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return index.SwitchWriteAlias(operationCtx, target)
}

func validateElasticsearchTarget(
	ctx context.Context,
	timeout time.Duration,
	index *searchindex.ElasticsearchIndex,
	target string,
	snapshotDocuments, catchupRecords int64,
	samples []searchindex.LotDocument,
) error {
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := index.RefreshIndex(operationCtx, target); err != nil {
		return err
	}
	count, err := index.CountDocuments(operationCtx, target)
	if err != nil {
		return err
	}
	if count < snapshotDocuments || count > snapshotDocuments+catchupRecords {
		return fmt.Errorf("elasticsearch rebuild count=%d outside expected range [%d,%d]", count, snapshotDocuments, snapshotDocuments+catchupRecords)
	}
	for _, document := range samples {
		identity, err := index.DocumentIdentity(operationCtx, target, document.LotID)
		if err != nil {
			return err
		}
		if !identity.Found || identity.LotVersion < document.LotVersion ||
			(identity.LotVersion == document.LotVersion && (identity.LastEventID != document.LastEventID || identity.ContentHash != document.ContentHash)) {
			return fmt.Errorf("elasticsearch rebuild sample identity mismatch for lot_id=%s", document.LotID)
		}
	}
	return nil
}

func captureSample(samples *[]searchindex.LotDocument, limit int, seen int64, document searchindex.LotDocument) {
	if len(*samples) < limit {
		*samples = append(*samples, document)
		return
	}
	// Deterministic rolling reservoir keeps coverage at both early and late pages
	// without introducing non-reproducible random validation.
	(*samples)[int((seen-1)%int64(limit))] = document
}

func boundedClientID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 128 {
		return value
	}
	return value[:128]
}

func durationSetting(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return parsed, nil
}

func intSetting(getenv func(string) string, key string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}
