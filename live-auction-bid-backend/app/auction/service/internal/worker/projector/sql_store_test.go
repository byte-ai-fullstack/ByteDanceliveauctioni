package projector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestSQLStoreEnsurePartitionOffsetPreservesDatabaseAuthority(t *testing.T) {
	state := &scriptedDBState{
		execRows:  []int64{0},
		queryRows: [][][]driver.Value{{{int64(41)}}},
	}
	db := sql.OpenDB(scriptedConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_000 }
	next, err := store.EnsurePartitionOffset(context.Background(), "auction.runtime.projection.v1", 2, 10)
	if err != nil {
		t.Fatalf("EnsurePartitionOffset: %v", err)
	}
	if next != 41 {
		t.Fatalf("next offset=%d want 41", next)
	}
	state.assertDrained(t)

	if _, err := store.EnsurePartitionOffset(context.Background(), "other", 2, 10); !errors.Is(err, ErrInvalidApplyRecord) {
		t.Fatalf("invalid topic error=%v", err)
	}
}

func TestSQLStoreApplyCommitsAndRollsBackAtTransactionBoundary(t *testing.T) {
	record := decodedRuntimeFixture(t)
	metadata := projectionLotMetadata(t)
	state := &scriptedDBState{
		execRows: []int64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		queryRows: [][][]driver.Value{
			{{record.Offset}},
			{},
			{{record.Fact.GetRoomId(), nil, record.Fact.GetPrevLotVersion(), "", false, int64(0)}},
			{{metadata.LotID, metadata.RoomID, metadata.MainAccountID, metadata.Title, metadata.Description,
				metadata.ImageURL, metadata.LotPayloadJSON, record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion()}},
		},
	}
	db := sql.OpenDB(scriptedConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_100 }
	result, err := store.Apply(context.Background(), record)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.NextOffset != record.Offset+1 {
		t.Fatalf("result=%+v", result)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction counts begin=%d commit=%d rollback=%d", state.begins, state.commits, state.rollbacks)
	}
	state.assertDrained(t)

	gapState := &scriptedDBState{queryRows: [][][]driver.Value{{{record.Offset - 1}}}}
	gapDB := sql.OpenDB(scriptedConnector{state: gapState})
	t.Cleanup(func() { _ = gapDB.Close() })
	gapStore, _ := NewSQLStore(gapDB)
	if _, err := gapStore.Apply(context.Background(), record); !errors.Is(err, ErrPartitionOffsetGap) {
		t.Fatalf("gap Apply error=%v", err)
	}
	if gapState.begins != 1 || gapState.commits != 0 || gapState.rollbacks != 1 {
		t.Fatalf("gap transaction counts begin=%d commit=%d rollback=%d", gapState.begins, gapState.commits, gapState.rollbacks)
	}
}

func TestSQLStoreApplyRetriesDeadlockAsACompleteTransaction(t *testing.T) {
	record := decodedRuntimeFixture(t)
	metadata := projectionLotMetadata(t)
	state := &scriptedDBState{
		execRows:   []int64{0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		execErrors: []error{&mysql.MySQLError{Number: 1213, Message: "deadlock"}},
		queryRows: [][][]driver.Value{
			{{record.Offset}}, {},
			{{record.Offset}}, {},
			{{record.Fact.GetRoomId(), nil, record.Fact.GetPrevLotVersion(), "", false, int64(0)}},
			{{metadata.LotID, metadata.RoomID, metadata.MainAccountID, metadata.Title, metadata.Description,
				metadata.ImageURL, metadata.LotPayloadJSON, record.Fact.GetPrevLotVersion(), record.Fact.GetConfigVersion()}},
		},
	}
	db := sql.OpenDB(scriptedConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db, WithRetryPolicy(2, time.Millisecond, time.Millisecond))
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	waits := 0
	store.retryJitter = func(time.Duration) time.Duration { return 0 }
	store.retryWait = func(context.Context, time.Duration) error {
		waits++
		return nil
	}
	result, err := store.Apply(context.Background(), record)
	if err != nil {
		t.Fatalf("Apply after retry: %v", err)
	}
	if result.NextOffset != record.Offset+1 || waits != 1 || state.begins != 2 || state.rollbacks != 1 || state.commits != 1 {
		t.Fatalf("result=%+v waits=%d begin=%d rollback=%d commit=%d", result, waits, state.begins, state.rollbacks, state.commits)
	}
	state.assertDrained(t)
}

func TestTransientDatabaseErrorClassificationAndRetryConfiguration(t *testing.T) {
	if !IsTransientDatabaseError(&mysql.MySQLError{Number: 1205}) || !IsTransientDatabaseError(driver.ErrBadConn) {
		t.Fatal("expected lock/connection errors to be transient")
	}
	if IsTransientDatabaseError(&mysql.MySQLError{Number: 1062}) || IsTransientDatabaseError(context.Canceled) || IsTransientDatabaseError(nil) {
		t.Fatal("duplicate, cancellation, and nil errors must not be transient")
	}
	db := sql.OpenDB(scriptedConnector{state: &scriptedDBState{}})
	t.Cleanup(func() { _ = db.Close() })
	if _, err := NewSQLStore(db, WithRetryPolicy(0, time.Millisecond, time.Second)); err == nil {
		t.Fatal("invalid retry attempts were accepted")
	}
	if _, err := NewSQLStore(db, nil); err == nil {
		t.Fatal("nil SQL store option was accepted")
	}
	store, err := NewSQLStore(db, WithRetryPolicy(3, 10*time.Millisecond, 15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	store.retryJitter = func(limit time.Duration) time.Duration { return limit }
	if got := store.retryDelay(3); got != 15*time.Millisecond {
		t.Fatalf("capped retry delay=%s", got)
	}
}

type scriptedConnector struct {
	state *scriptedDBState
}

func (connector scriptedConnector) Connect(context.Context) (driver.Conn, error) {
	return &scriptedDBConn{state: connector.state}, nil
}

func (connector scriptedConnector) Driver() driver.Driver { return scriptedDBDriver{} }

type scriptedDBDriver struct{}

func (scriptedDBDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("scripted driver must be opened through its connector")
}

type scriptedDBState struct {
	mu         sync.Mutex
	execRows   []int64
	execErrors []error
	queryRows  [][][]driver.Value
	begins     int
	commits    int
	rollbacks  int
	closed     bool
}

func (state *scriptedDBState) assertDrained(t *testing.T) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.execRows) != 0 || len(state.execErrors) != 0 || len(state.queryRows) != 0 {
		t.Fatalf("unconsumed DB script: %d execs, %d exec errors, %d queries", len(state.execRows), len(state.execErrors), len(state.queryRows))
	}
}

type scriptedDBConn struct {
	state *scriptedDBState
}

func (conn *scriptedDBConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (conn *scriptedDBConn) Close() error {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	conn.state.closed = true
	return nil
}

func (conn *scriptedDBConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *scriptedDBConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	conn.state.begins++
	return &scriptedDBTx{state: conn.state}, nil
}

func (conn *scriptedDBConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	if len(conn.state.execRows) == 0 {
		return nil, errors.New("unexpected database exec")
	}
	rows := conn.state.execRows[0]
	conn.state.execRows = conn.state.execRows[1:]
	if len(conn.state.execErrors) > 0 {
		err := conn.state.execErrors[0]
		conn.state.execErrors = conn.state.execErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	return driver.RowsAffected(rows), nil
}

func (conn *scriptedDBConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	if len(conn.state.queryRows) == 0 {
		return nil, errors.New("unexpected database query")
	}
	rows := conn.state.queryRows[0]
	conn.state.queryRows = conn.state.queryRows[1:]
	columns := 1
	if len(rows) > 0 {
		columns = len(rows[0])
	}
	return &scriptedDBRows{columns: make([]string, columns), rows: rows}, nil
}

type scriptedDBTx struct {
	state *scriptedDBState
}

func (tx *scriptedDBTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.commits++
	return nil
}

func (tx *scriptedDBTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rollbacks++
	return nil
}

type scriptedDBRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (rows *scriptedDBRows) Columns() []string { return rows.columns }
func (rows *scriptedDBRows) Close() error      { return nil }

func (rows *scriptedDBRows) Next(dest []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(dest, rows.rows[rows.index])
	rows.index++
	return nil
}

var _ driver.Connector = scriptedConnector{}
var _ driver.ConnBeginTx = (*scriptedDBConn)(nil)
var _ driver.ExecerContext = (*scriptedDBConn)(nil)
var _ driver.QueryerContext = (*scriptedDBConn)(nil)
