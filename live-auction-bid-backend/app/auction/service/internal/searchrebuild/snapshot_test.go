package searchrebuild

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestDecodeSnapshotLotRowReturnsCanonicalDocument(t *testing.T) {
	row := snapshotRowFixture(t, v1.LotStatus_LOT_STATUS_QUEUED, 7)
	document, skipped, err := decodeSnapshotLotRow(row)
	if err != nil || skipped || document.LotID != row.LotID || document.LotVersion != row.LotVersion || !document.PublicVisible {
		t.Fatalf("document=%+v skipped=%t error=%v", document, skipped, err)
	}
}

func TestSnapshotRecordPayloadIsIndependent(t *testing.T) {
	row := snapshotRowFixture(t, v1.LotStatus_LOT_STATUS_QUEUED, 7)
	document, skipped, err := decodeSnapshotLotRow(row)
	if err != nil || skipped {
		t.Fatalf("document=%+v skipped=%t error=%v", document, skipped, err)
	}
	record := SnapshotRecord{Document: document, Payload: append([]byte(nil), row.DomainPayload...)}
	record.Payload[0] ^= 0xff
	if record.Payload[0] == row.DomainPayload[0] {
		t.Fatal("snapshot record payload aliases the database row buffer")
	}
}

func TestDecodeSnapshotLotRowRejectsPublicMissingAndForkedEvents(t *testing.T) {
	missing := snapshotRowFixture(t, v1.LotStatus_LOT_STATUS_QUEUED, 7)
	missing.DomainPayload = nil
	if _, _, err := decodeSnapshotLotRow(missing); !errors.Is(err, ErrSnapshotIdentityMissing) {
		t.Fatalf("missing error=%v", err)
	}
	forked := snapshotRowFixture(t, v1.LotStatus_LOT_STATUS_LIVE, 8)
	forked.Title = "different authoritative title"
	if _, _, err := decodeSnapshotLotRow(forked); !errors.Is(err, ErrSnapshotIdentityFork) {
		t.Fatalf("fork error=%v", err)
	}
}

func TestDecodeSnapshotLotRowSkipsIncompleteOrAdvancedHiddenDraft(t *testing.T) {
	missing := snapshotRowFixture(t, v1.LotStatus_LOT_STATUS_DRAFT, 1)
	missing.DomainPayload = nil
	if _, skipped, err := decodeSnapshotLotRow(missing); err != nil || !skipped {
		t.Fatalf("missing draft skipped=%t error=%v", skipped, err)
	}
	advanced := snapshotRowFixture(t, v1.LotStatus_LOT_STATUS_DRAFT, 2)
	if _, skipped, err := decodeSnapshotLotRow(advanced); err != nil || !skipped {
		t.Fatalf("advanced draft skipped=%t error=%v", skipped, err)
	}
}

func snapshotRowFixture(t *testing.T, status v1.LotStatus, mysqlVersion int64) snapshotLotRow {
	t.Helper()
	causationID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := eventcontract.DomainMessageID(causationID, eventcontract.LotStateTopicV1)
	if err != nil {
		t.Fatal(err)
	}
	lot := &v1.Lot{Id: "lot-1", Category: "珠宝", Tags: []string{"翡翠", "手镯"}}
	lotPayload, err := protojson.Marshal(lot)
	if err != nil {
		t.Fatal(err)
	}
	eventVersion := mysqlVersion
	if status == v1.LotStatus_LOT_STATUS_DRAFT && mysqlVersion > 1 {
		eventVersion = mysqlVersion - 1
	}
	event := &v1.LotStateDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{
			MessageId: messageID, CausationId: causationID, TraceId: "trace-1", SchemaVersion: 1, OccurredAtUnixMs: 1_700_000_000_000,
		},
		LotId: "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: eventVersion,
		Status: status, Title: "翡翠手镯", Description: "冰糯种", Category: lot.GetCategory(), Tags: lot.GetTags(),
		ImageUrl: "https://example.test/lot.jpg", StartPriceFen: 10_000, CurrentPriceFen: 12_000,
		Currency: "CNY", StartsAtUnixMs: 100, EndsAtUnixMs: 200,
	}
	event.ContentHash, err = eventcontract.LotStateContentHash(event)
	if err != nil {
		t.Fatal(err)
	}
	domainPayload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(event.ContentHash) != 64 || strings.Trim(event.ContentHash, "0123456789abcdef") != "" {
		t.Fatal("invalid fixture hash")
	}
	return snapshotLotRow{
		LotID: "lot-1", RoomID: "room-1", MainAccountID: "merchant-1", LotVersion: mysqlVersion, Status: int32(status),
		Title: event.Title, Description: event.Description, ImageURL: event.ImageUrl, Currency: event.Currency,
		StartPriceFen: event.StartPriceFen, CurrentPriceFen: event.CurrentPriceFen,
		StartsAtUnixMs: event.StartsAtUnixMs, EndsAtUnixMs: event.EndsAtUnixMs,
		LotPayload: lotPayload, DomainPayload: domainPayload,
	}
}
