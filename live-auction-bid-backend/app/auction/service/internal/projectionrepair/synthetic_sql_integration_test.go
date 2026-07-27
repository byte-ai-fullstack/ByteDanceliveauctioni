package projectionrepair

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestIntegrationSyntheticAuditInterruptsAndCompletes(t *testing.T) {
	dsn := os.Getenv("AUCTION_PROJECTOR_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AUCTION_PROJECTOR_TEST_MYSQL_DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping MySQL: %v", err)
	}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := newRepairID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := newRepairID()
	if err != nil {
		t.Fatal(err)
	}
	metadata := SyntheticBundleMetadata{
		Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 127, FromOffset: 900, ToOffsetExclusive: 902,
		PreparedBy: "integration-preparer", ChangeTicket: "INTEGRATION-REPAIR",
		RepairReason: "verify synthetic audit transaction", RecordCount: 2, BundleSHA256: strings.Repeat("a", 64),
	}
	cleanup := func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM auction_projection_repair_audit WHERE repair_id IN (?, ?)", firstID, secondID)
	}
	cleanup()
	t.Cleanup(cleanup)
	request := SyntheticRequest{Execute: true, ExecutedBy: "integration-executor"}

	if interrupted, err := store.BeginSyntheticAudit(ctx, firstID, request, metadata, map[string]any{"attempt": 1}); err != nil || interrupted != 0 {
		t.Fatalf("first BeginSyntheticAudit interrupted=%d error=%v", interrupted, err)
	}
	if interrupted, err := store.BeginSyntheticAudit(ctx, secondID, request, metadata, map[string]any{"attempt": 2}); err != nil || interrupted != 1 {
		t.Fatalf("second BeginSyntheticAudit interrupted=%d error=%v", interrupted, err)
	}
	assertSyntheticAuditStatus(t, db, firstID, "FAILED", true)
	assertSyntheticAuditStatus(t, db, secondID, "STARTED", false)
	if err := store.FinishSyntheticAudit(ctx, secondID, true, map[string]any{"verified": true}, nil); err != nil {
		t.Fatalf("FinishSyntheticAudit: %v", err)
	}
	assertSyntheticAuditStatus(t, db, secondID, "SUCCEEDED", true)
}

func assertSyntheticAuditStatus(t *testing.T, db *sql.DB, repairID, wantStatus string, wantCompleted bool) {
	t.Helper()
	var (
		status        string
		bundleDigest  string
		preparedBy    string
		changeTicket  string
		recordCount   int
		completedAtMs int64
	)
	if err := db.QueryRow(`
SELECT status, bundle_sha256, prepared_by, change_ticket, record_count, completed_at_ms
FROM auction_projection_repair_audit
WHERE repair_id = ?`, repairID).Scan(
		&status, &bundleDigest, &preparedBy, &changeTicket, &recordCount, &completedAtMs,
	); err != nil {
		t.Fatalf("read synthetic audit: %v", err)
	}
	if status != wantStatus || bundleDigest != strings.Repeat("a", 64) || preparedBy != "integration-preparer" ||
		changeTicket != "INTEGRATION-REPAIR" || recordCount != 2 || (completedAtMs > 0) != wantCompleted {
		t.Fatalf("status=%s digest=%s preparer=%s ticket=%s count=%d completed=%d",
			status, bundleDigest, preparedBy, changeTicket, recordCount, completedAtMs)
	}
}
