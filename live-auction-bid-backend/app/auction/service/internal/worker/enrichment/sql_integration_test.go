package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

func TestIntegrationSQLStorePersistsReadySnapshotsAndRejectsIdentityConflict(t *testing.T) {
	db := integrationEnrichmentMySQL(t)
	const (
		causationID = "01890f3e-8b7a-7cc2-98c4-dc0c0c073991"
		orderID     = "integration-enrichment-ready-order"
		lotID       = "integration-enrichment-ready-lot"
	)
	record := integrationEnrichmentRecord(t, causationID, orderID, lotID, "")
	seedIntegrationEnrichment(t, db, record, true)

	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_100 }
	result, err := store.Apply(context.Background(), record, 1)
	if err != nil {
		t.Fatalf("Apply ready enrichment: %v", err)
	}
	if result.Duplicate || result.Status != orderenrichment.StatusReady {
		t.Fatalf("ready result=%+v", result)
	}

	var (
		sourceMessageID string
		payloadHash     string
		addressJSON     []byte
		shopJSON        []byte
		status          orderenrichment.Status
		attempts        int
		lastError       string
	)
	err = db.QueryRow(`
SELECT source_message_id, payload_hash, address_snapshot, shop_snapshot, status, attempts, last_error
FROM auction_order_enrichments
WHERE order_id = ?`, orderID).Scan(
		&sourceMessageID, &payloadHash, &addressJSON, &shopJSON, &status, &attempts, &lastError,
	)
	if err != nil {
		t.Fatalf("read ready enrichment: %v", err)
	}
	if sourceMessageID != record.MessageID || payloadHash != record.PayloadHash || status != orderenrichment.StatusReady || attempts != 1 || lastError != "" {
		t.Fatalf("ready enrichment identity/status=(%q,%q,%q,%d,%q)", sourceMessageID, payloadHash, status, attempts, lastError)
	}
	var address orderenrichment.AddressSnapshot
	if err := json.Unmarshal(addressJSON, &address); err != nil || address.AddressID != integrationAddressID(orderID) || address.FullAddress == "" {
		t.Fatalf("ready address=%+v error=%v", address, err)
	}
	var shop orderenrichment.ShopSnapshot
	if err := json.Unmarshal(shopJSON, &shop); err != nil || shop.ShopID != integrationMainAccountID(orderID) || shop.ShopName != "Integration Shop" {
		t.Fatalf("ready shop=%+v error=%v", shop, err)
	}

	duplicate, err := store.Apply(context.Background(), record, 2)
	if err != nil || !duplicate.Duplicate || duplicate.Status != orderenrichment.StatusReady {
		t.Fatalf("duplicate Apply=(%+v,%v)", duplicate, err)
	}
	assertIntegrationEnrichmentScalar(t, db, "SELECT COUNT(*) FROM auction_order_enrichments WHERE order_id = ?", orderID, 1)
	assertIntegrationEnrichmentScalar(t, db, "SELECT attempts FROM auction_order_enrichments WHERE order_id = ?", orderID, 1)

	conflict := integrationEnrichmentRecord(t, causationID, orderID, lotID, integrationAddressID(orderID))
	if _, err := store.Apply(context.Background(), conflict, 3); !errors.Is(err, ErrMessageIdentityConflict) {
		t.Fatalf("same message ID with different payload error=%v", err)
	}
	assertIntegrationEnrichmentScalar(t, db, "SELECT COUNT(*) FROM auction_order_enrichments WHERE order_id = ?", orderID, 1)
}

func TestIntegrationSQLStorePersistsPartialWithoutOptionalSources(t *testing.T) {
	db := integrationEnrichmentMySQL(t)
	const (
		causationID = "01890f3e-8b7a-7cc2-98c4-dc0c0c073992"
		orderID     = "integration-enrichment-partial-order"
		lotID       = "integration-enrichment-partial-lot"
	)
	record := integrationEnrichmentRecord(t, causationID, orderID, lotID, "")
	seedIntegrationEnrichment(t, db, record, false)

	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_200 }
	result, err := store.Apply(context.Background(), record, 3)
	if err != nil {
		t.Fatalf("Apply partial enrichment: %v", err)
	}
	if result.Duplicate || result.Status != orderenrichment.StatusPartial {
		t.Fatalf("partial result=%+v", result)
	}

	var status orderenrichment.Status
	var attempts, addressIsNull, shopIsNull int
	var lastError string
	err = db.QueryRow(`
SELECT status, attempts, last_error, address_snapshot IS NULL, shop_snapshot IS NULL
FROM auction_order_enrichments
WHERE order_id = ?`, orderID).Scan(&status, &attempts, &lastError, &addressIsNull, &shopIsNull)
	if err != nil {
		t.Fatalf("read partial enrichment: %v", err)
	}
	if status != orderenrichment.StatusPartial || attempts != 3 || lastError != "address_not_found;shop_not_found" || addressIsNull != 1 || shopIsNull != 1 {
		t.Fatalf("partial enrichment=(%q,%d,%q,%d,%d)", status, attempts, lastError, addressIsNull, shopIsNull)
	}
}

func integrationEnrichmentMySQL(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("AUCTION_ENRICHMENT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AUCTION_ENRICHMENT_TEST_MYSQL_DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping MySQL: %v", err)
	}
	return db
}

func integrationEnrichmentRecord(t *testing.T, causationID, orderID, lotID, addressID string) Record {
	t.Helper()
	messageID, err := eventcontract.DomainMessageID(causationID, eventcontract.OrderEnrichmentTopicV1)
	if err != nil {
		t.Fatal(err)
	}
	event := &v1.OrderEnrichmentRequestedDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{
			MessageId: messageID, CausationId: causationID, TraceId: "trace-integration-enrichment",
			SchemaVersion: 1, OccurredAtUnixMs: 1_700_000_000_000,
		},
		OrderId: orderID, LotId: lotID, AddressId: addressID,
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeRecord(&kgo.Record{
		Topic: eventcontract.OrderEnrichmentTopicV1, Partition: 2, Offset: 11, Key: []byte(orderID), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: eventcontract.RuntimeHeaderContentType, Value: []byte(eventcontract.DomainEventContentType)},
			{Key: eventcontract.DomainHeaderMessageID, Value: []byte(messageID)},
			{Key: eventcontract.DomainHeaderCausationID, Value: []byte(causationID)},
			{Key: eventcontract.RuntimeHeaderTraceID, Value: []byte(event.GetMetadata().GetTraceId())},
			{Key: eventcontract.RuntimeHeaderSchemaVersion, Value: []byte("1")},
		},
	})
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	return record
}

func seedIntegrationEnrichment(t *testing.T, db *sql.DB, record Record, withOptionalSources bool) {
	t.Helper()
	orderID := record.Event.GetOrderId()
	lotID := record.Event.GetLotId()
	buyerID := integrationBuyerID(orderID)
	mainAccountID := integrationMainAccountID(orderID)
	cleanup := func() {
		_, _ = db.Exec("DELETE FROM auction_order_enrichments WHERE order_id = ?", orderID)
		_, _ = db.Exec("DELETE FROM user_order_items WHERE order_id = ?", orderID)
		_, _ = db.Exec("DELETE FROM user_orders WHERE id = ?", orderID)
		_, _ = db.Exec("DELETE FROM user_delivery_addresses WHERE user_id = ?", buyerID)
		_, _ = db.Exec("DELETE FROM auction_users WHERE id = ?", mainAccountID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := db.Exec(`
INSERT INTO user_orders
  (id, source, source_order_id, main_account_id, user_id, status, payment_status, total_amount, currency, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'auction', ?, ?, ?, 'PENDING_PAYMENT', 'UNPAID', 10100, 'CNY', 1700000000000, 1700000000000)`,
		orderID, "source-"+orderID, mainAccountID, buyerID); err != nil {
		t.Fatalf("insert core order: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO user_order_items
  (id, order_id, source, source_item_id, lot_id, title, quantity, unit_amount, total_amount, currency)
VALUES (?, ?, 'auction', ?, ?, 'Integration lot', 1, 10100, 10100, 'CNY')`,
		"item-"+orderID, orderID, lotID, lotID); err != nil {
		t.Fatalf("insert core order item: %v", err)
	}
	if !withOptionalSources {
		return
	}
	if _, err := db.Exec(`
INSERT INTO auction_users
  (id, username, nickname, password_hash, main_account_id, status, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 'Integration Shop', 'integration-hash', ?, 1, 1700000000000, 1700000000000)`,
		mainAccountID, "shop-"+orderID, mainAccountID); err != nil {
		t.Fatalf("insert shop identity: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO user_delivery_addresses
  (id, user_id, receiver_name, phone, province, city, district, street, detail, postal_code, is_default, status, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 'Integration Buyer', '13800000000', '浙江省', '杭州市', '余杭区', '文一西路', '1号', '310000', TRUE, 'active', 1700000000000, 1700000000000)`,
		integrationAddressID(orderID), buyerID); err != nil {
		t.Fatalf("insert delivery address: %v", err)
	}
}

func integrationBuyerID(orderID string) string       { return "buyer-" + orderID }
func integrationMainAccountID(orderID string) string { return "main-" + orderID }
func integrationAddressID(orderID string) string     { return "address-" + orderID }

func assertIntegrationEnrichmentScalar(t *testing.T, db *sql.DB, query string, argument any, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(query, argument).Scan(&got); err != nil {
		t.Fatalf("query enrichment scalar: %v", err)
	}
	if got != want {
		t.Fatalf("scalar=%d want=%d for %s", got, want, query)
	}
}
