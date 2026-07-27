package v1

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestRuntimeFactV1FieldNumbersAreStable(t *testing.T) {
	descriptor := (&RuntimeFactV1{}).ProtoReflect().Descriptor()
	want := map[protoreflect.Name]protoreflect.FieldNumber{
		"event_id":            1,
		"trace_id":            2,
		"lot_id":              3,
		"room_id":             4,
		"prev_lot_version":    5,
		"lot_version":         6,
		"occurred_at_unix_ms": 7,
		"config_version":      8,
		"command":             9,
		"state_after":         10,
		"accepted_bid":        11,
		"order_draft":         12,
		"idempotency_key":     13,
		"schema_version":      14,
	}
	for name, number := range want {
		field := descriptor.Fields().ByName(name)
		if field == nil {
			t.Fatalf("RuntimeFactV1 field %q is missing", name)
		}
		if field.Number() != number {
			t.Fatalf("RuntimeFactV1 field %q number=%d want=%d", name, field.Number(), number)
		}
	}
}

func TestLotRuntimeStateV1ConfigFieldNumbersAreStable(t *testing.T) {
	descriptor := (&LotRuntimeStateV1{}).ProtoReflect().Descriptor()
	want := map[protoreflect.Name]protoreflect.FieldNumber{
		"duration_ms":          25,
		"anti_snipe_window_ms": 26,
		"anti_snipe_extend_ms": 27,
	}
	for name, number := range want {
		field := descriptor.Fields().ByName(name)
		if field == nil {
			t.Fatalf("LotRuntimeStateV1 field %q is missing", name)
		}
		if field.Number() != number {
			t.Fatalf("LotRuntimeStateV1 field %q number=%d want=%d", name, field.Number(), number)
		}
		if !field.HasOptionalKeyword() {
			t.Fatalf("LotRuntimeStateV1 field %q must remain optional for old V1 records", name)
		}
	}
}

func TestEventContractsUseIntegerFenAmounts(t *testing.T) {
	files := []protoreflect.FileDescriptor{
		File_auction_service_v1_runtime_proto,
		File_auction_service_v1_domain_proto,
		File_auction_service_v1_realtime_proto,
	}
	for _, file := range files {
		messages := file.Messages()
		for index := 0; index < messages.Len(); index++ {
			checkMessageAmountFields(t, messages.Get(index))
		}
	}
}

func TestPublicRealtimePayloadCannotSerializeStableUserIdentity(t *testing.T) {
	payload, err := protojson.Marshal(&RoomSnapshotPublicV1{
		RoomId:     "room-1",
		LotId:      "lot-1",
		LotVersion: 7,
		Status:     LotStatus_LOT_STATUS_LIVE,
		TopRanking: []*PublicRankingItemV1{{
			Rank:           1,
			MaskedNickname: "用***甲",
			AmountFen:      12_000,
		}},
	})
	if err != nil {
		t.Fatalf("marshal public snapshot: %v", err)
	}
	if bytes.Contains(payload, []byte("user-secret-123")) || bytes.Contains(payload, []byte("userId")) {
		t.Fatalf("public payload contains a stable user identity: %s", payload)
	}
}

func checkMessageAmountFields(t *testing.T, message protoreflect.MessageDescriptor) {
	t.Helper()
	fields := message.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if field.Kind() == protoreflect.FloatKind || field.Kind() == protoreflect.DoubleKind {
			t.Fatalf("%s.%s uses forbidden floating-point type", message.FullName(), field.Name())
		}
		name := string(field.Name())
		if strings.Contains(name, "amount") || strings.Contains(name, "price") {
			if !strings.HasSuffix(name, "_fen") || field.Kind() != protoreflect.Int64Kind {
				t.Fatalf("%s.%s must be int64 *_fen", message.FullName(), field.Name())
			}
		}
	}
	nested := message.Messages()
	for index := 0; index < nested.Len(); index++ {
		checkMessageAmountFields(t, nested.Get(index))
	}
}
