package migrations

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestOpenContainsPairedVersionedMigrations(t *testing.T) {
	source, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, name := range []string{
		"000001_event_backbone.up.sql",
		"000001_event_backbone.down.sql",
		"000002_core_schema.up.sql",
		"000002_core_schema.down.sql",
		"000003_room_display_lot.up.sql",
		"000003_room_display_lot.down.sql",
		"000004_order_enrichment_identity.up.sql",
		"000004_order_enrichment_identity.down.sql",
		"000005_lot_presentation.up.sql",
		"000005_lot_presentation.down.sql",
		"000006_domain_outbox_route_order.up.sql",
		"000006_domain_outbox_route_order.down.sql",
		"000007_projection_repair_audit.up.sql",
		"000007_projection_repair_audit.down.sql",
		"000008_synthetic_repair_audit.up.sql",
		"000008_synthetic_repair_audit.down.sql",
	} {
		info, err := fs.Stat(source, name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("migration %s is empty", name)
		}
	}
}

func TestTargetSchemaContainsEveryOwnedTableAndRollback(t *testing.T) {
	upSQL, downSQL := readMigrationDirections(t)
	requiredTables := []string{
		"auction_projection_inbox",
		"auction_lot_projection_state",
		"auction_projection_partition_offsets",
		"auction_domain_outbox",
		"auction_order_enrichments",
		"auction_reconcile_findings",
		"auction_projection_repair_audit",
		"auction_rooms",
		"auction_room_states",
		"auction_lots",
		"auction_lot_presentations",
		"auction_bids",
		"auction_lot_stats",
		"auction_lot_participants",
		"auction_events",
		"user_orders",
		"user_order_items",
		"user_order_payments",
		"auction_deposit_holds",
		"auction_users",
		"auction_roles",
		"auction_permissions",
		"auction_user_roles",
		"auction_role_permissions",
		"auction_user_permissions",
		"auction_user_sessions",
		"asset_files",
		"shop_products",
		"shop_skus",
		"user_delivery_addresses",
	}
	for _, table := range requiredTables {
		if count := strings.Count(upSQL, "CREATE TABLE "+table+" ("); count != 1 {
			t.Errorf("target schema creates %s %d times, want exactly once", table, count)
		}
		if count := strings.Count(downSQL, "DROP TABLE IF EXISTS "+table+";"); count != 1 {
			t.Errorf("rollback drops %s %d times, want exactly once", table, count)
		}
	}

	createPattern := regexp.MustCompile(`(?m)^CREATE TABLE ([a-z0-9_]+) \(`)
	if got := len(createPattern.FindAllStringSubmatch(upSQL, -1)); got != len(requiredTables) {
		t.Fatalf("target schema creates %d tables, want %d", got, len(requiredTables))
	}
}

func TestSyntheticRepairAuditMigrationOwnsRequiredMetadata(t *testing.T) {
	upSQL, downSQL := readMigrationDirections(t)
	for _, column := range []string{"bundle_sha256", "prepared_by", "change_ticket", "record_count"} {
		if !strings.Contains(upSQL, "ADD COLUMN "+column) {
			t.Fatalf("synthetic repair audit migration is missing column %s", column)
		}
		if !strings.Contains(downSQL, "DROP COLUMN "+column) {
			t.Fatalf("synthetic repair audit rollback is missing column %s", column)
		}
	}
	if !strings.Contains(upSQL, "repair_type IN ('ORIGINAL_REPLAY', 'SYNTHETIC_REPLAY')") {
		t.Fatal("synthetic repair audit migration is missing repair type constraint")
	}
	if !strings.Contains(upSQL, "bundle_sha256 IS NOT NULL") {
		t.Fatal("synthetic repair audit migration must reject missing bundle digests")
	}
}

func TestTargetSchemaEnforcesBlueprintInvariants(t *testing.T) {
	upSQL, _ := readMigrationDirections(t)
	for _, required := range []string{
		"currency CHAR(3) NOT NULL DEFAULT 'CNY'",
		"config_version BIGINT NOT NULL DEFAULT 1",
		"UNIQUE KEY idx_lot_user_idem (lot_id, user_id, idempotency_key)",
		"UNIQUE KEY uk_user_order_source_id (source, source_order_id)",
		"UNIQUE KEY uk_user_order_payment_idem (order_id, idempotency_key)",
		"UNIQUE KEY uk_deposit_idem (lot_id, buyer_user_id, idempotency_key)",
		"UNIQUE KEY uk_source_position (topic, kafka_partition, kafka_offset)",
		"UNIQUE KEY uk_message_id (message_id)",
		"source_message_id VARCHAR(128) NOT NULL",
		"payload_hash CHAR(64) NOT NULL",
		"UNIQUE KEY uk_source_message (source_message_id)",
		"ADD KEY idx_domain_outbox_route_order (topic, partition_key, published_at_ms, id)",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("target schema is missing invariant %q", required)
		}
	}
	for _, forbidden := range []string{
		"auction_runtime_" + "projection_offsets",
		"auction_runtime_" + "projection_shard_offsets",
		"streamed_at_" + "unix_ms",
		"last_" + "stream_error",
		"start_price_currency",
		"min_increment_currency",
		"cap_price_currency",
		"current_price_currency",
		"final_price_currency",
		" FLOAT",
		" DOUBLE",
		" DECIMAL",
	} {
		if strings.Contains(strings.ToUpper(upSQL), strings.ToUpper(forbidden)) {
			t.Errorf("target schema contains forbidden legacy/type fragment %q", forbidden)
		}
	}
}

func readMigrationDirections(t *testing.T) (string, string) {
	t.Helper()
	source, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var up, down strings.Builder
	for _, entry := range entries {
		content, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", entry.Name(), err)
		}
		switch {
		case strings.HasSuffix(entry.Name(), ".up.sql"):
			up.Write(content)
		case strings.HasSuffix(entry.Name(), ".down.sql"):
			down.Write(content)
		}
	}
	return up.String(), down.String()
}
