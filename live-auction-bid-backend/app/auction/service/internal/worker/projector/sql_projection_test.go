package projector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestApplyRuntimeRecordCommitsCompleteBidProjection(t *testing.T) {
	record := decodedRuntimeFixture(t)
	metadata := projectionLotMetadata(t)
	tx := &scriptedProjectionTx{t: t,
		queries: []scriptedQuery{
			{contains: "FROM auction_projection_partition_offsets", values: []any{record.Offset}},
			{contains: "FROM auction_projection_inbox", err: sql.ErrNoRows},
			{contains: "FROM auction_lot_projection_state", values: []any{record.Fact.GetRoomId(), nil, record.Fact.GetPrevLotVersion(), "", false, int64(0)}},
			{contains: "FROM auction_lots", values: []any{
				metadata.LotID, metadata.RoomID, metadata.MainAccountID, metadata.Title, metadata.Description,
				metadata.ImageURL, metadata.LotPayloadJSON, record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion(),
			}},
		},
		execs: []scriptedExec{
			{contains: "INSERT IGNORE INTO auction_lot_projection_state", rows: 1},
			{contains: "UPDATE auction_lots", rows: 1, check: func(args []any) {
				payload, ok := args[25].([]byte)
				if !ok {
					t.Fatalf("lot payload argument type=%T", args[25])
				}
				lot := new(v1.Lot)
				if err := protojson.Unmarshal(payload, lot); err != nil {
					t.Fatalf("decode projected lot payload: %v", err)
				}
				if lot.GetVersion() != record.Fact.GetLotVersion() || lot.GetCurrentPrice().GetAmount() != 12_000 {
					t.Fatalf("projected lot=%v", lot)
				}
			}},
			{contains: "UPDATE auction_room_states", rows: 1},
			{contains: "INSERT INTO auction_bids", rows: 1},
			{contains: "INSERT IGNORE INTO auction_lot_participants", rows: 1},
			{contains: "INSERT INTO auction_lot_stats", rows: 1},
			{contains: "UPDATE auction_lot_projection_state", rows: 1},
			{contains: "INSERT INTO auction_projection_inbox", rows: 1},
			{contains: "INSERT INTO auction_domain_outbox", rows: 1},
			{contains: "INSERT INTO auction_domain_outbox", rows: 1},
			{contains: "UPDATE auction_projection_partition_offsets", rows: 1},
		},
	}

	result, err := applyRuntimeRecord(context.Background(), tx, record, 1_700_000_000_100)
	if err != nil {
		t.Fatalf("applyRuntimeRecord: %v", err)
	}
	if result.NextOffset != record.Offset+1 || result.AlreadyAdvanced || result.DuplicateEvent {
		t.Fatalf("result=%+v", result)
	}
	tx.assertDrained()
}

func TestApplyRuntimeRecordHandlesOffsetAndInboxIdempotenceBeforeVersion(t *testing.T) {
	record := decodedRuntimeFixture(t)
	tests := []struct {
		name       string
		nextOffset int64
		inbox      *scriptedQuery
		want       ApplyResult
		wantErr    error
		execs      []scriptedExec
	}{
		{name: "already advanced", nextOffset: record.Offset + 1, want: ApplyResult{NextOffset: record.Offset + 1, AlreadyAdvanced: true}},
		{name: "offset gap", nextOffset: record.Offset - 1, wantErr: ErrPartitionOffsetGap},
		{name: "duplicate event", nextOffset: record.Offset,
			inbox: &scriptedQuery{contains: "FROM auction_projection_inbox", values: []any{record.PayloadHash}},
			want:  ApplyResult{NextOffset: record.Offset + 1, DuplicateEvent: true},
			execs: []scriptedExec{{contains: "UPDATE auction_projection_partition_offsets", rows: 1}}},
		{name: "identity conflict", nextOffset: record.Offset,
			inbox:   &scriptedQuery{contains: "FROM auction_projection_inbox", values: []any{strings.Repeat("0", 64)}},
			wantErr: ErrEventIdentityConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := []scriptedQuery{{contains: "FROM auction_projection_partition_offsets", values: []any{test.nextOffset}}}
			if test.inbox != nil {
				queries = append(queries, *test.inbox)
			}
			tx := &scriptedProjectionTx{t: t, queries: queries, execs: test.execs}
			got, err := applyRuntimeRecord(context.Background(), tx, record, 1)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result=%+v want=%+v", got, test.want)
			}
			tx.assertDrained()
		})
	}
}

func TestApplyRuntimeRecordRejectsMissingOffsetAndInvalidStateChain(t *testing.T) {
	record := decodedRuntimeFixture(t)
	metadata := projectionLotMetadata(t)
	tests := []struct {
		name    string
		queries []scriptedQuery
		execs   []scriptedExec
		wantErr error
	}{
		{name: "offset missing", queries: []scriptedQuery{{contains: "FROM auction_projection_partition_offsets", err: sql.ErrNoRows}}, wantErr: ErrPartitionOffsetMissing},
		{name: "frozen", queries: baseProjectionQueries(record, metadata, record.Fact.GetPrevLotVersion(), true, record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion())[:3], execs: []scriptedExec{{contains: "INSERT IGNORE INTO auction_lot_projection_state", rows: 0}}, wantErr: ErrProjectionLotFrozen},
		{name: "state gap", queries: baseProjectionQueries(record, metadata, record.Fact.GetPrevLotVersion()-1, false, record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion())[:3], execs: []scriptedExec{{contains: "INSERT IGNORE INTO auction_lot_projection_state", rows: 0}}, wantErr: ErrRuntimeProjectionGap},
		{name: "lot version gap", queries: baseProjectionQueries(record, metadata, record.Fact.GetPrevLotVersion(), false, record.Fact.GetPrevLotVersion()-1, record.Fact.GetConfigVersion()), execs: []scriptedExec{{contains: "INSERT IGNORE INTO auction_lot_projection_state", rows: 0}}, wantErr: ErrRuntimeProjectionGap},
		{name: "config version", queries: baseProjectionQueries(record, metadata, record.Fact.GetPrevLotVersion(), false, record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion()-1), execs: []scriptedExec{{contains: "INSERT IGNORE INTO auction_lot_projection_state", rows: 0}}, wantErr: ErrProjectionConfigVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &scriptedProjectionTx{t: t, queries: test.queries, execs: test.execs}
			_, err := applyRuntimeRecord(context.Background(), tx, record, 1)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
			tx.assertDrained()
		})
	}
}

func TestProjectLotPayloadPreservesDocumentAndRequiresExactSeconds(t *testing.T) {
	fact := runtimeRecordFact(t)
	metadata := projectionLotMetadata(t)
	payload, duration, window, extension, err := projectLotPayload(metadata.LotPayloadJSON, fact)
	if err != nil {
		t.Fatalf("projectLotPayload: %v", err)
	}
	if duration != 60 || window != 10 || extension != 30 {
		t.Fatalf("seconds=(%d,%d,%d)", duration, window, extension)
	}
	lot := new(v1.Lot)
	if err := protojson.Unmarshal(payload, lot); err != nil {
		t.Fatalf("decode projected payload: %v", err)
	}
	if lot.GetCategory() != "jewelry" || lot.GetVersion() != 7 || lot.GetStats().GetBidCount() != 4 {
		t.Fatalf("projected lot=%v", lot)
	}

	nonExact := int64(1_001)
	fact.StateAfter.DurationMs = &nonExact
	if _, _, _, _, err := projectLotPayload(metadata.LotPayloadJSON, fact); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("non-exact duration error=%v", err)
	}
	fact.StateAfter.DurationMs = nil
	if _, _, _, _, err := projectLotPayload(metadata.LotPayloadJSON, fact); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("missing duration error=%v", err)
	}
	if _, _, _, _, err := projectLotPayload(nil, runtimeRecordFact(t)); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("empty payload error=%v", err)
	}
}

func TestProjectLotPayloadClearsQueueAndDuelOnTerminalState(t *testing.T) {
	fact := runtimeRecordFact(t)
	fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED
	fact.AcceptedBid = nil
	fact.StateAfter.Status = v1.LotStatus_LOT_STATUS_SETTLED
	metadata := projectionLotMetadata(t)
	lot := new(v1.Lot)
	if err := protojson.Unmarshal(metadata.LotPayloadJSON, lot); err != nil {
		t.Fatal(err)
	}
	lot.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_NEXT
	lot.QueuePosition = 2
	lot.DuelState = &v1.DuelState{Active: true}
	metadata.LotPayloadJSON, _ = protojson.Marshal(lot)
	payload, _, _, _, err := projectLotPayload(metadata.LotPayloadJSON, fact)
	if err != nil {
		t.Fatalf("projectLotPayload: %v", err)
	}
	if err := protojson.Unmarshal(payload, lot); err != nil {
		t.Fatal(err)
	}
	if lot.GetQueueStatus() != v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE || lot.GetQueuePosition() != 0 || lot.GetDuelState().GetActive() {
		t.Fatalf("terminal lot queue/duel=%v", lot)
	}
}

func TestValidateApplyRecordRejectsTampering(t *testing.T) {
	valid := decodedRuntimeFixture(t)
	if err := validateApplyRecord(valid); err != nil {
		t.Fatalf("valid record: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*DecodedRecord)
	}{
		{name: "source", mutate: func(record *DecodedRecord) { record.Topic = "other" }},
		{name: "fact", mutate: func(record *DecodedRecord) { record.Fact = nil }},
		{name: "payload", mutate: func(record *DecodedRecord) { record.Payload = nil }},
		{name: "hash", mutate: func(record *DecodedRecord) { record.PayloadHash = strings.Repeat("0", 64) }},
		{name: "fact differs", mutate: func(record *DecodedRecord) {
			record.Fact = proto.Clone(record.Fact).(*v1.RuntimeFactV1)
			record.Fact.TraceId = "changed"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if err := validateApplyRecord(record); !errors.Is(err, ErrInvalidApplyRecord) {
				t.Fatalf("error=%v want ErrInvalidApplyRecord", err)
			}
		})
	}
}

func TestProjectionSQLHelpersReportCASAndDatabaseErrors(t *testing.T) {
	if err := requireOneRow(scriptedResult(0), nil, "operation"); !errors.Is(err, ErrProjectionCAS) {
		t.Fatalf("zero rows error=%v", err)
	}
	if err := requireOneRow(nil, nil, "operation"); !errors.Is(err, ErrProjectionCAS) {
		t.Fatalf("nil result error=%v", err)
	}
	dbErr := errors.New("database unavailable")
	if err := requireOneRow(nil, dbErr, "operation"); !errors.Is(err, dbErr) {
		t.Fatalf("database error=%v", err)
	}
	if _, err := exactSeconds(nil, "field", true); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("nil seconds error=%v", err)
	}
	negative := int64(-1_000)
	if _, err := exactSeconds(&negative, "field", false); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("negative seconds error=%v", err)
	}
}

func TestInsertOrderDraftWritesCoreOrderAndItemWithoutExternalLookup(t *testing.T) {
	fact := settledRuntimeFactFixture(t)
	tx := &scriptedProjectionTx{t: t, execs: []scriptedExec{
		{contains: "INSERT INTO user_orders", rows: 1, check: func(args []any) {
			if args[0] != "order-1" || args[3] != "merchant-1" || args[4] != "buyer-1" {
				t.Fatalf("order args=%v", args)
			}
		}},
		{contains: "INSERT INTO user_order_items", rows: 1, check: func(args []any) {
			if args[0] != "auction_item_order-1" || args[1] != "order-1" || args[3] != fact.GetLotId() {
				t.Fatalf("order item args=%v", args)
			}
		}},
	}}
	if err := insertOrderDraft(context.Background(), tx, fact); err != nil {
		t.Fatalf("insertOrderDraft: %v", err)
	}
	tx.assertDrained()
	if err := insertOrderDraft(context.Background(), &scriptedProjectionTx{t: t}, runtimeRecordFact(t)); err != nil {
		t.Fatalf("nil draft should be a no-op: %v", err)
	}
}

func TestInsertDomainMessagesRejectsInvalidHeadersBeforeDatabaseWrite(t *testing.T) {
	err := insertDomainMessages(context.Background(), &scriptedProjectionTx{t: t}, []DomainMessage{{HeadersJSON: []byte("{")}})
	if !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("invalid headers error=%v", err)
	}
}

func TestNewSQLStoreAndPublicValidation(t *testing.T) {
	if _, err := NewSQLStore(nil); err == nil {
		t.Fatal("nil database was accepted")
	}
	var nilStore *SQLStore
	if _, err := nilStore.Apply(context.Background(), DecodedRecord{}); err == nil {
		t.Fatal("nil store Apply was accepted")
	}
	if _, err := nilStore.EnsurePartitionOffset(context.Background(), "", -1, -1); err == nil {
		t.Fatal("nil store EnsurePartitionOffset was accepted")
	}
}

func decodedRuntimeFixture(t *testing.T) DecodedRecord {
	t.Helper()
	decoded, err := DecodeRecord(runtimeRecordFixture(t))
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	return decoded
}

func settledRuntimeFactFixture(t *testing.T) *v1.RuntimeFactV1 {
	t.Helper()
	fact := runtimeRecordFact(t)
	fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED
	fact.AcceptedBid = nil
	fact.IdempotencyKey = ""
	fact.StateAfter.Status = v1.LotStatus_LOT_STATUS_SETTLED
	fact.StateAfter.WinnerUserId = "buyer-1"
	fact.StateAfter.WinnerNickname = "Buyer"
	fact.StateAfter.FinalPriceFen = fact.StateAfter.CurrentPriceFen
	fact.StateAfter.SettledAtUnixMs = fact.OccurredAtUnixMs
	fact.StateAfter.OrderId = "order-1"
	fact.OrderDraft = &v1.OrderDraftV1{
		OrderId:         "order-1",
		LotId:           fact.LotId,
		RoomId:          fact.RoomId,
		MainAccountId:   "merchant-1",
		BuyerUserId:     "buyer-1",
		BuyerNickname:   "Buyer",
		Title:           "Jade vase",
		ImageUrl:        "https://example.test/lot.png",
		TotalAmountFen:  fact.StateAfter.FinalPriceFen,
		Currency:        fact.StateAfter.Currency,
		CreatedAtUnixMs: fact.OccurredAtUnixMs,
	}
	return fact
}

func baseProjectionQueries(record DecodedRecord, metadata LotMetadata, stateVersion int64, frozen bool, lotVersion, configVersion int64) []scriptedQuery {
	return []scriptedQuery{
		{contains: "FROM auction_projection_partition_offsets", values: []any{record.Offset}},
		{contains: "FROM auction_projection_inbox", err: sql.ErrNoRows},
		{contains: "FROM auction_lot_projection_state", values: []any{record.Fact.GetRoomId(), nil, stateVersion, "", frozen, int64(0)}},
		{contains: "FROM auction_lots", values: []any{
			metadata.LotID, metadata.RoomID, metadata.MainAccountID, metadata.Title, metadata.Description,
			metadata.ImageURL, metadata.LotPayloadJSON, lotVersion, configVersion,
		}},
	}
}

type scriptedProjectionTx struct {
	t       *testing.T
	queries []scriptedQuery
	execs   []scriptedExec
}

type scriptedQuery struct {
	contains string
	values   []any
	err      error
}

type scriptedExec struct {
	contains string
	rows     int64
	err      error
	check    func([]any)
}

func (tx *scriptedProjectionTx) QueryRowContext(_ context.Context, query string, _ ...any) rowScanner {
	tx.t.Helper()
	if len(tx.queries) == 0 {
		tx.t.Fatalf("unexpected query: %s", compactSQL(query))
	}
	next := tx.queries[0]
	tx.queries = tx.queries[1:]
	if !strings.Contains(query, next.contains) {
		tx.t.Fatalf("query=%s want fragment=%q", compactSQL(query), next.contains)
	}
	return scriptedRow{values: next.values, err: next.err}
}

func (tx *scriptedProjectionTx) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	tx.t.Helper()
	if len(tx.execs) == 0 {
		tx.t.Fatalf("unexpected exec: %s", compactSQL(query))
	}
	next := tx.execs[0]
	tx.execs = tx.execs[1:]
	if !strings.Contains(query, next.contains) {
		tx.t.Fatalf("exec=%s want fragment=%q", compactSQL(query), next.contains)
	}
	if next.check != nil {
		next.check(args)
	}
	if next.err != nil {
		return nil, next.err
	}
	return scriptedResult(next.rows), nil
}

func (tx *scriptedProjectionTx) assertDrained() {
	tx.t.Helper()
	if len(tx.queries) != 0 || len(tx.execs) != 0 {
		tx.t.Fatalf("unconsumed script: %d queries, %d execs", len(tx.queries), len(tx.execs))
	}
}

type scriptedRow struct {
	values []any
	err    error
}

func (row scriptedRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return fmt.Errorf("scan destination count=%d values=%d", len(dest), len(row.values))
	}
	for index, value := range row.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *bool:
			*target = value.(bool)
		case *[]byte:
			*target = append((*target)[:0], value.([]byte)...)
		case *sql.NullString:
			if value == nil {
				*target = sql.NullString{}
			} else {
				*target = sql.NullString{String: value.(string), Valid: true}
			}
		default:
			return fmt.Errorf("unsupported scan destination %T", dest[index])
		}
	}
	return nil
}

type scriptedResult int64

func (result scriptedResult) LastInsertId() (int64, error) { return 0, nil }
func (result scriptedResult) RowsAffected() (int64, error) { return int64(result), nil }

func compactSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

var _ sqlProjectionTx = (*scriptedProjectionTx)(nil)
