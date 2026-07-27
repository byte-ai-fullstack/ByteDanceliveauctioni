package domainrelay

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestIntegrationSQLStoreClaimsFencesRetriesAndPublishes(t *testing.T) {
	dsn := os.Getenv("AUCTION_DOMAIN_RELAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AUCTION_DOMAIN_RELAY_TEST_MYSQL_DSN is not configured")
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

	message := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	cleanupIntegrationDomainMessage(t, db, message.MessageID)
	insertIntegrationDomainMessage(t, db, message)

	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(message.CreatedAtMs + 1_000)
	claimed, err := store.Claim(ctx, "integration-relay-a", now, 1, 30*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first Claim=(%d,%v)", len(claimed), err)
	}
	first := claimed[0]
	if first.MessageID != message.MessageID || !validClaimToken(first.LockToken) {
		t.Fatalf("first claim=%+v", first)
	}
	stats, err := store.Stats(ctx, now)
	if err != nil || stats.Pending < 1 || stats.OldestAgeMs < 1_000 {
		t.Fatalf("Stats=(%+v,%v)", stats, err)
	}

	nextAttempt := now.Add(2 * time.Second)
	if err := store.MarkFailed(ctx, first, now, nextAttempt, 1, "broker\nunavailable"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	claimed, err = store.Claim(ctx, "integration-relay-b", now.Add(time.Second), 1, 30*time.Second)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("early Claim=(%d,%v)", len(claimed), err)
	}
	claimed, err = store.Claim(ctx, "integration-relay-b", nextAttempt, 1, 30*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("retry Claim=(%d,%v)", len(claimed), err)
	}
	second := claimed[0]
	if second.LockToken == first.LockToken || second.Attempts != 1 || second.LastError != "broker unavailable" {
		t.Fatalf("second claim=%+v", second)
	}
	if err := store.MarkPublished(ctx, first, nextAttempt); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale MarkPublished error=%v", err)
	}
	if err := store.MarkPublished(ctx, second, nextAttempt); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	stats, err = store.Stats(ctx, nextAttempt)
	if err != nil || stats.Pending != 0 || stats.OldestAgeMs != 0 {
		t.Fatalf("final Stats=(%+v,%v)", stats, err)
	}
	assertIntegrationDomainScalar(t, db, "SELECT published_at_ms > 0 FROM auction_domain_outbox WHERE message_id = ?", message.MessageID, 1)
	assertIntegrationDomainScalar(t, db, "SELECT attempts FROM auction_domain_outbox WHERE message_id = ?", message.MessageID, 1)
}

func TestIntegrationSQLStoreBlocksSameRouteFollowerUntilPredecessorPublishes(t *testing.T) {
	dsn := os.Getenv("AUCTION_DOMAIN_RELAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AUCTION_DOMAIN_RELAY_TEST_MYSQL_DSN is not configured")
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

	first := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	second := domainMessageFixture(t, eventcontract.BidAcceptedTopicV1)
	routeKey := "route-" + first.CausationID
	first.PartitionKey = routeKey
	second.PartitionKey = routeKey
	cleanupIntegrationDomainMessage(t, db, first.MessageID)
	cleanupIntegrationDomainMessage(t, db, second.MessageID)
	insertIntegrationDomainMessage(t, db, first)
	insertIntegrationDomainMessage(t, db, second)

	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(first.CreatedAtMs + 1_000)
	claimed, err := store.Claim(ctx, "integration-relay-a", now, 2, 30*time.Second)
	if err != nil || len(claimed) != 1 || claimed[0].MessageID != first.MessageID {
		t.Fatalf("first Claim=(%+v,%v)", claimed, err)
	}
	blocked, err := store.Claim(ctx, "integration-relay-b", now, 2, 30*time.Second)
	if err != nil || len(blocked) != 0 {
		t.Fatalf("blocked follower Claim=(%+v,%v)", blocked, err)
	}
	if err := store.MarkPublished(ctx, claimed[0], now); err != nil {
		t.Fatalf("publish predecessor: %v", err)
	}
	claimed, err = store.Claim(ctx, "integration-relay-b", now, 2, 30*time.Second)
	if err != nil || len(claimed) != 1 || claimed[0].MessageID != second.MessageID {
		t.Fatalf("follower Claim=(%+v,%v)", claimed, err)
	}
	if err := store.MarkPublished(ctx, claimed[0], now); err != nil {
		t.Fatalf("publish follower: %v", err)
	}
}

func insertIntegrationDomainMessage(t *testing.T, db *sql.DB, message Message) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO auction_domain_outbox
  (message_id, causation_id, topic, partition_key, payload, headers_json, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		message.MessageID, message.CausationID, message.Topic, message.PartitionKey,
		message.Payload, message.HeadersJSON, message.CreatedAtMs); err != nil {
		t.Fatalf("insert domain outbox: %v", err)
	}
}

func cleanupIntegrationDomainMessage(t *testing.T, db *sql.DB, messageID string) {
	t.Helper()
	cleanup := func() { _, _ = db.Exec("DELETE FROM auction_domain_outbox WHERE message_id = ?", messageID) }
	cleanup()
	t.Cleanup(cleanup)
}

func assertIntegrationDomainScalar(t *testing.T, db *sql.DB, query string, argument any, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(query, argument).Scan(&got); err != nil {
		t.Fatalf("query scalar: %v", err)
	}
	if got != want {
		t.Fatalf("scalar=%d want=%d for %s", got, want, query)
	}
}
