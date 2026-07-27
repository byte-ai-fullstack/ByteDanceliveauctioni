package searchindex

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestPGVectorRebuildTableLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("AUCTION_TEST_PGVECTOR_DSN")
	if dsn == "" {
		t.Skip("AUCTION_TEST_PGVECTOR_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const target = "auction_lot_search_docs_v999999999"
	const backup = "auction_lot_search_docs_backup_20991231_235959"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	schema := fmt.Sprintf("auction_rebuild_test_%d", time.Now().UnixNano())
	quotedSchema := pq.QuoteIdentifier(schema)
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+quotedSchema+` CASCADE`)
	})
	if _, err := db.ExecContext(ctx, `CREATE TABLE `+quotedSchema+`."auction_lot_search_docs" (LIKE public."auction_lot_search_docs" INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	testDSN, err := dsnWithSearchPath(dsn, schema+",public")
	if err != nil {
		t.Fatal(err)
	}
	config := PGVectorConfig{
		DSN: testDSN, EmbeddingProvider: "dashscope", EmbeddingModel: "test-model", EmbeddingModelVersion: "v1",
		EmbeddingDimensions: 3, MaxOpenConns: 2, MaxIdleConns: 1,
	}
	source, err := NewPGVectorIndex(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	created, err := source.EnsureRebuildTable(ctx, target, false)
	if err != nil || !created {
		t.Fatalf("EnsureRebuildTable created=%t error=%v", created, err)
	}
	config.TableName = target
	targetIndex, err := NewPGVectorIndex(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = targetIndex.Close() }()
	document := validVectorDocument(t)
	if _, err := targetIndex.ApplyDocument(ctx, document, []float64{0.1, 0.2, 0.3}); err != nil {
		t.Fatal(err)
	}
	if count, err := targetIndex.CountDocuments(ctx); err != nil || count != 1 {
		t.Fatalf("target count=%d error=%v", count, err)
	}
	hash := document.StableEmbeddingHash("dashscope", "test-model", "v1", 3)
	if reusable, err := targetIndex.HasCompatibleEmbedding(ctx, document.LotID, hash); err != nil || !reusable {
		t.Fatalf("reusable=%t error=%v", reusable, err)
	}
	if err := source.SwitchRebuildTable(ctx, target, backup); err != nil {
		t.Fatal(err)
	}
	state, err := source.DocumentState(ctx, document.LotID)
	if err != nil || !state.Found || state.LotVersion != document.LotVersion {
		t.Fatalf("promoted state=%+v error=%v", state, err)
	}
}

func dsnWithSearchPath(dsn, searchPath string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", searchPath)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
