package projector

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestIntegrationSQLStoreAppliesAndDeduplicatesRuntimeFact(t *testing.T) {
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

	record := runtimeRecordFixture(t)
	record.Partition = 0
	record.Offset = 0
	decoded, err := DecodeRecord(record)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	metadata := projectionLotMetadata(t)
	cleanupIntegrationProjection(t, db, decoded)
	seedIntegrationProjection(t, db, decoded, metadata)

	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := store.EnsurePartitionOffset(ctx, decoded.Topic, decoded.Partition, 0); err != nil || next != 0 {
		t.Fatalf("EnsurePartitionOffset=(%d,%v)", next, err)
	}
	result, err := store.Apply(ctx, decoded)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.NextOffset != 1 {
		t.Fatalf("result=%+v", result)
	}
	assertIntegrationScalar(t, db, "SELECT version FROM auction_lots WHERE id = ?", metadata.LotID, decoded.Fact.GetLotVersion())
	assertIntegrationScalar(t, db, "SELECT COUNT(*) FROM auction_bids WHERE id = ?", decoded.Fact.GetAcceptedBid().GetBidId(), 1)
	assertIntegrationScalar(t, db, "SELECT COUNT(*) FROM auction_projection_inbox WHERE event_id = ?", decoded.Fact.GetEventId(), 1)
	assertIntegrationScalar(t, db, "SELECT COUNT(*) FROM auction_domain_outbox WHERE causation_id = ?", decoded.Fact.GetEventId(), 2)
	assertIntegrationScalar(t, db, "SELECT next_offset FROM auction_projection_partition_offsets WHERE topic = ? AND kafka_partition = ?", []any{decoded.Topic, decoded.Partition}, 1)

	record.Offset = 1
	duplicate, err := DecodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	result, err = store.Apply(ctx, duplicate)
	if err != nil {
		t.Fatalf("Apply duplicate: %v", err)
	}
	if !result.DuplicateEvent || result.NextOffset != 2 {
		t.Fatalf("duplicate result=%+v", result)
	}
	assertIntegrationScalar(t, db, "SELECT COUNT(*) FROM auction_bids WHERE id = ?", decoded.Fact.GetAcceptedBid().GetBidId(), 1)
	assertIntegrationScalar(t, db, "SELECT COUNT(*) FROM auction_domain_outbox WHERE causation_id = ?", decoded.Fact.GetEventId(), 2)
	assertIntegrationScalar(t, db, "SELECT next_offset FROM auction_projection_partition_offsets WHERE topic = ? AND kafka_partition = ?", []any{decoded.Topic, decoded.Partition}, 2)

	conflictFact := proto.Clone(decoded.Fact).(*v1.RuntimeFactV1)
	conflictFact.TraceId = "trace-conflict"
	conflictPayload, err := eventcontract.MarshalRuntimeFactBinary(conflictFact)
	if err != nil {
		t.Fatalf("MarshalRuntimeFactBinary conflict: %v", err)
	}
	conflictRecord := &kgo.Record{
		Topic:     record.Topic,
		Partition: record.Partition,
		Offset:    2,
		Key:       append([]byte(nil), record.Key...),
		Value:     conflictPayload,
		Headers:   cloneIntegrationHeaders(record.Headers),
	}
	replaceHeader(eventcontract.RuntimeHeaderTraceID, conflictFact.GetTraceId())(conflictRecord)
	conflict, err := DecodeRecord(conflictRecord)
	if err != nil {
		t.Fatalf("DecodeRecord conflict: %v", err)
	}
	if _, err := store.Apply(ctx, conflict); !errors.Is(err, ErrEventIdentityConflict) {
		t.Fatalf("same event ID with different payload error=%v", err)
	}
	assertIntegrationScalar(t, db, "SELECT next_offset FROM auction_projection_partition_offsets WHERE topic = ? AND kafka_partition = ?", []any{decoded.Topic, decoded.Partition}, 2)
}

func cloneIntegrationHeaders(headers []kgo.RecordHeader) []kgo.RecordHeader {
	cloned := make([]kgo.RecordHeader, len(headers))
	for index, header := range headers {
		cloned[index] = kgo.RecordHeader{Key: header.Key, Value: append([]byte(nil), header.Value...)}
	}
	return cloned
}

func seedIntegrationProjection(t *testing.T, db *sql.DB, record DecodedRecord, metadata LotMetadata) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO auction_rooms
  (id, main_account_id, name, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 'Integration room', ?, ?)`, metadata.RoomID, metadata.MainAccountID, record.Fact.GetOccurredAtUnixMs(), record.Fact.GetOccurredAtUnixMs()); err != nil {
		t.Fatalf("insert room: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO auction_room_states
  (room_id, main_account_id, active_lot_id, active_lot_version, updated_at_unix_ms)
VALUES (?, ?, ?, ?, ?)`, metadata.RoomID, metadata.MainAccountID, metadata.LotID, record.Fact.GetPrevLotVersion(), record.Fact.GetOccurredAtUnixMs()); err != nil {
		t.Fatalf("insert room state: %v", err)
	}
	state := record.Fact.GetStateAfter()
	if _, err := db.ExecContext(ctx, `
INSERT INTO auction_lots
  (id, main_account_id, room_id, title, description, image_url, status, queue_status, queue_position,
   currency, start_price_amount, min_increment_amount, cap_price_amount, duration_seconds,
   anti_snipe_window_seconds, anti_snipe_extend_seconds, max_extend_count, current_price_amount,
   leading_user_id, leading_nickname, started_at_unix_ms, ends_at_unix_ms, version, config_version,
   playbook_stage, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, 0, ?, ?, ?, NULL, 60, 10, 30, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		metadata.LotID, metadata.MainAccountID, metadata.RoomID, metadata.Title, metadata.Description, metadata.ImageURL,
		state.GetStatus(), state.GetCurrency(), state.GetStartPriceFen(), state.GetMinIncrementFen(), state.GetMaxExtendCount(),
		state.GetStartPriceFen(), "", "", state.GetStartedAtUnixMs(), state.GetEndsAtUnixMs(),
		record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion(), metadata.LotPayloadJSON); err != nil {
		t.Fatalf("insert lot: %v", err)
	}
}

func cleanupIntegrationProjection(t *testing.T, db *sql.DB, record DecodedRecord) {
	t.Helper()
	cleanup := func() {
		statements := []struct {
			query string
			args  []any
		}{
			{"DELETE FROM auction_domain_outbox WHERE causation_id = ?", []any{record.Fact.GetEventId()}},
			{"DELETE FROM auction_projection_inbox WHERE event_id = ?", []any{record.Fact.GetEventId()}},
			{"DELETE FROM auction_lot_participants WHERE lot_id = ?", []any{record.Fact.GetLotId()}},
			{"DELETE FROM auction_lot_stats WHERE lot_id = ?", []any{record.Fact.GetLotId()}},
			{"DELETE FROM auction_bids WHERE lot_id = ?", []any{record.Fact.GetLotId()}},
			{"DELETE FROM auction_lot_projection_state WHERE lot_id = ?", []any{record.Fact.GetLotId()}},
			{"DELETE FROM auction_projection_partition_offsets WHERE topic = ? AND kafka_partition = ?", []any{record.Topic, record.Partition}},
			{"DELETE FROM auction_lots WHERE id = ?", []any{record.Fact.GetLotId()}},
			{"DELETE FROM auction_room_states WHERE room_id = ?", []any{record.Fact.GetRoomId()}},
			{"DELETE FROM auction_rooms WHERE id = ?", []any{record.Fact.GetRoomId()}},
		}
		for _, statement := range statements {
			_, _ = db.Exec(statement.query, statement.args...)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
}

func assertIntegrationScalar(t *testing.T, db *sql.DB, query string, argument any, want int64) {
	t.Helper()
	args, ok := argument.([]any)
	if !ok {
		args = []any{argument}
	}
	var got int64
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query scalar: %v", err)
	}
	if got != want {
		t.Fatalf("scalar=%d want=%d for %s", got, want, query)
	}
}
