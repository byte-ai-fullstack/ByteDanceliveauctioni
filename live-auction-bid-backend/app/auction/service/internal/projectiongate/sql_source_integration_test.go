package projectiongate

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestIntegrationSQLSourceReadsProjectionOffsets(t *testing.T) {
	dsn := os.Getenv("AUCTION_PROJECTOR_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AUCTION_PROJECTOR_TEST_MYSQL_DSN is not configured")
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if err := database.PingContext(ctx); err != nil {
		t.Fatalf("ping MySQL: %v", err)
	}

	const partition int32 = 77
	cleanup := func() {
		_, _ = database.ExecContext(ctx, `
DELETE FROM auction_projection_partition_offsets
WHERE topic = ? AND kafka_partition = ?`, eventcontract.RuntimeProjectionTopicV1, partition)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := database.ExecContext(ctx, `
INSERT INTO auction_projection_partition_offsets
  (topic, kafka_partition, next_offset, updated_at_ms)
VALUES (?, ?, ?, ?)`, eventcontract.RuntimeProjectionTopicV1, partition, int64(123), int64(456)); err != nil {
		t.Fatalf("insert projection offset: %v", err)
	}

	source, err := NewSQLSource(database)
	if err != nil {
		t.Fatal(err)
	}
	offsets, err := source.Offsets(ctx)
	if err != nil {
		t.Fatalf("Offsets() error = %v", err)
	}
	if got, exists := offsets[partition]; !exists || got.NextOffset != 123 || got.UpdatedAtMs != 456 {
		t.Fatalf("integration offset = %+v, exists = %t", got, exists)
	}
}
