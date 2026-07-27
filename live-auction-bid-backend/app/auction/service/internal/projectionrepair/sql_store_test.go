package projectionrepair

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestSQLStoreReadsOffsetInboxAndLotStates(t *testing.T) {
	state := &repairDBState{queryRows: [][][]driver.Value{
		{{int64(10), int64(100)}},
		{{"event-1", int64(9), "lot-1", int64(7), "payload-hash", int64(99)}},
		{{"lot-1", true, "event-1", int64(7), "state-hash", false, int64(99), int64(7)}},
	}}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := store.ReadPartitionOffset(context.Background(), eventcontract.RuntimeProjectionTopicV1, 2)
	if err != nil || !offset.Found || offset.NextOffset != 10 || offset.UpdatedAtMs != 100 {
		t.Fatalf("offset=%+v error=%v", offset, err)
	}
	inbox, err := store.ReadInboxRange(context.Background(), eventcontract.RuntimeProjectionTopicV1, 2, 9, 10)
	if err != nil || len(inbox) != 1 || inbox[0].EventID != "event-1" || inbox[0].LotVersion != 7 {
		t.Fatalf("inbox=%+v error=%v", inbox, err)
	}
	states, err := store.ReadLotStates(context.Background(), []string{"lot-1", "lot-1"})
	if err != nil || len(states) != 1 || !states["lot-1"].ProjectionStateFound || states["lot-1"].LotVersion != 7 {
		t.Fatalf("states=%+v error=%v", states, err)
	}
	state.assertDrained(t)
}

func TestSQLStoreMissingOffsetAndEmptyLotSet(t *testing.T) {
	state := &repairDBState{queryRows: [][][]driver.Value{{}}}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	offset, err := store.ReadPartitionOffset(context.Background(), eventcontract.RuntimeProjectionTopicV1, 0)
	if err != nil || offset.Found {
		t.Fatalf("offset=%+v error=%v", offset, err)
	}
	states, err := store.ReadLotStates(context.Background(), nil)
	if err != nil || len(states) != 0 {
		t.Fatalf("states=%v error=%v", states, err)
	}
	state.assertDrained(t)
}

func TestSQLStoreReadsSyntheticInboxIdentitiesAndAuditHistory(t *testing.T) {
	state := &repairDBState{queryRows: [][][]driver.Value{
		{{"event-1", int64(9), "lot-1", int64(7), "payload-hash", int64(99)}},
		{{int64(1), int64(2), int64(0)}},
	}}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	inbox, err := store.ReadInboxByEventIDs(context.Background(), []string{"event-1", "event-1"})
	if err != nil || len(inbox) != 1 || inbox["event-1"].Offset != 9 {
		t.Fatalf("inbox=%+v error=%v", inbox, err)
	}
	history, err := store.ReadSyntheticAuditHistory(context.Background(), SyntheticBundleMetadata{
		Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, FromOffset: 10, ToOffsetExclusive: 12,
		BundleSHA256: strings.Repeat("a", 64),
	})
	if err != nil || history.Started != 1 || history.Failed != 2 || history.Succeeded != 0 {
		t.Fatalf("history=%+v error=%v", history, err)
	}
	state.assertDrained(t)
}

func TestSQLStoreWritesReplayAuditAndResolvesGapFindings(t *testing.T) {
	state := &repairDBState{execRows: []int64{1, 1, 2}}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	store.nowMs = func() int64 { return 123 }
	request := ReplayRequest{
		Partition: 2, ExpectedNextOffset: 10, ThroughOffset: 11, Execute: true,
		Operator: "operator", Reason: "documented gap",
	}
	if err := store.BeginReplayAudit(context.Background(), "repair-1", request, 12, map[string]string{"phase": "start"}); err != nil {
		t.Fatalf("BeginReplayAudit: %v", err)
	}
	if err := store.FinishReplayAudit(context.Background(), "repair-1", true, map[string]string{"phase": "done"}, []string{"lot-1", "lot-1"}); err != nil {
		t.Fatalf("FinishReplayAudit: %v", err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transactions begin=%d commit=%d rollback=%d", state.begins, state.commits, state.rollbacks)
	}
	state.assertDrained(t)
}

func TestSQLStoreWritesSyntheticAuditInterruptsStaleAndResolvesFindings(t *testing.T) {
	state := &repairDBState{execRows: []int64{2, 1, 1, 3}}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	store.nowMs = func() int64 { return 123 }
	metadata := SyntheticBundleMetadata{
		Topic: eventcontract.RuntimeProjectionTopicV1, Partition: 2, FromOffset: 10, ToOffsetExclusive: 12,
		PreparedBy: "engineer-a", ChangeTicket: "INC-42", RepairReason: "retention cliff",
		RecordCount: 2, BundleSHA256: strings.Repeat("a", 64),
	}
	interrupted, err := store.BeginSyntheticAudit(context.Background(), "repair-2", SyntheticRequest{
		Execute: true, ExecutedBy: "engineer-b",
	}, metadata, map[string]string{"phase": "start"})
	if err != nil || interrupted != 2 {
		t.Fatalf("interrupted=%d error=%v", interrupted, err)
	}
	if err := store.FinishSyntheticAudit(context.Background(), "repair-2", true, map[string]string{"phase": "done"}, []string{"lot-1"}); err != nil {
		t.Fatalf("FinishSyntheticAudit: %v", err)
	}
	if state.begins != 2 || state.commits != 2 || state.rollbacks != 0 {
		t.Fatalf("transactions begin=%d commit=%d rollback=%d", state.begins, state.commits, state.rollbacks)
	}
	state.assertDrained(t)
}

func TestSQLStoreRecordsFailedReplayWithoutResolvingFindings(t *testing.T) {
	state := &repairDBState{execRows: []int64{1}}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	store.nowMs = func() int64 { return 123 }
	if err := store.FinishReplayAudit(context.Background(), "repair-1", false, map[string]string{"error": "failed"}, nil); err != nil {
		t.Fatalf("FinishReplayAudit: %v", err)
	}
	if state.begins != 1 || state.commits != 1 {
		t.Fatalf("transactions begin=%d commit=%d", state.begins, state.commits)
	}
	state.assertDrained(t)
}

func TestSQLStoreRejectsInvalidInputs(t *testing.T) {
	if _, err := NewSQLStore(nil); err == nil {
		t.Fatal("nil DB was accepted")
	}
	state := &repairDBState{}
	db := sql.OpenDB(repairDBConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	if _, err := store.ReadPartitionOffset(context.Background(), "other", 0); err == nil {
		t.Fatal("wrong topic was accepted")
	}
	if _, err := store.ReadInboxRange(context.Background(), eventcontract.RuntimeProjectionTopicV1, 0, 2, 1); err == nil {
		t.Fatal("backwards inbox range was accepted")
	}
	if _, err := store.ReadLotStates(context.Background(), []string{""}); err == nil {
		t.Fatal("empty lot ID was accepted")
	}
	if err := requireOneRow(driver.RowsAffected(2), "test"); err == nil {
		t.Fatal("two affected rows were accepted")
	}
	if values, err := normalizedLotIDs([]string{"lot-1", "lot-1", "lot-2"}); err != nil || len(values) != 2 {
		t.Fatalf("normalized=%v error=%v", values, err)
	}
	if values, err := normalizedEventIDs([]string{"event-1", "event-1", "event-2"}); err != nil || len(values) != 2 {
		t.Fatalf("events=%v error=%v", values, err)
	}
	if _, err := normalizedEventIDs([]string{"bad\nevent"}); err == nil {
		t.Fatal("invalid event ID was accepted")
	}
}

type repairDBConnector struct {
	state *repairDBState
}

func (connector repairDBConnector) Connect(context.Context) (driver.Conn, error) {
	return &repairDBConn{state: connector.state}, nil
}

func (connector repairDBConnector) Driver() driver.Driver { return repairDBDriver{} }

type repairDBDriver struct{}

func (repairDBDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("repair DB driver requires connector")
}

type repairDBState struct {
	mu        sync.Mutex
	execRows  []int64
	queryRows [][][]driver.Value
	begins    int
	commits   int
	rollbacks int
}

func (state *repairDBState) assertDrained(t *testing.T) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.execRows) != 0 || len(state.queryRows) != 0 {
		t.Fatalf("unconsumed DB script: exec=%d query=%d", len(state.execRows), len(state.queryRows))
	}
}

type repairDBConn struct {
	state *repairDBState
}

func (conn *repairDBConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are unsupported")
}

func (conn *repairDBConn) Close() error { return nil }

func (conn *repairDBConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *repairDBConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	conn.state.begins++
	return &repairDBTx{state: conn.state}, nil
}

func (conn *repairDBConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	if len(conn.state.execRows) == 0 {
		return nil, errors.New("unexpected repair DB exec")
	}
	rows := conn.state.execRows[0]
	conn.state.execRows = conn.state.execRows[1:]
	return driver.RowsAffected(rows), nil
}

func (conn *repairDBConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	if len(conn.state.queryRows) == 0 {
		return nil, errors.New("unexpected repair DB query")
	}
	values := conn.state.queryRows[0]
	conn.state.queryRows = conn.state.queryRows[1:]
	columns := 1
	if len(values) > 0 {
		columns = len(values[0])
	}
	return &repairDBRows{columns: make([]string, columns), rows: values}, nil
}

type repairDBTx struct {
	state *repairDBState
}

func (tx *repairDBTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.commits++
	return nil
}

func (tx *repairDBTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rollbacks++
	return nil
}

type repairDBRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (rows *repairDBRows) Columns() []string { return rows.columns }
func (rows *repairDBRows) Close() error      { return nil }

func (rows *repairDBRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(destination, rows.rows[rows.index])
	rows.index++
	return nil
}

var _ driver.Connector = repairDBConnector{}
var _ driver.ConnBeginTx = (*repairDBConn)(nil)
var _ driver.ExecerContext = (*repairDBConn)(nil)
var _ driver.QueryerContext = (*repairDBConn)(nil)
