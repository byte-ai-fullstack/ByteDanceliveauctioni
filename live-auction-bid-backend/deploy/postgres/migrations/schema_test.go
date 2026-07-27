package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestSearchSchemaMigrationContainsVersionAndEmbeddingIdentity(t *testing.T) {
	payload, err := os.ReadFile("000001_search_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(payload)
	for _, required := range []string{
		"CREATE EXTENSION IF NOT EXISTS vector",
		"CREATE TABLE IF NOT EXISTS auction_lot_search_docs",
		"lot_version BIGINT NOT NULL",
		"last_event_id VARCHAR(64) NOT NULL",
		"content_hash CHAR(64) NOT NULL",
		"embedding_provider VARCHAR(64) NOT NULL",
		"embedding_model_version VARCHAR(128) NOT NULL",
		"embedding vector",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("search schema is missing %q", required)
		}
	}
	if strings.Contains(sql, "embedding vector(1024)") {
		t.Fatal("search schema hard-codes one embedding dimension")
	}
}

func TestSearchSchemaHasExplicitRollback(t *testing.T) {
	payload, err := os.ReadFile("000001_search_schema.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "DROP TABLE IF EXISTS auction_lot_search_docs") {
		t.Fatal("search schema rollback does not remove its owned table")
	}
}
