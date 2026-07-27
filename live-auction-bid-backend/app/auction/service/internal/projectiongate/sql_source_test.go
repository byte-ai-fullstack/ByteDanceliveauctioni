package projectiongate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type projectionGateDBConnector struct {
	state *projectionGateDBState
}

func (connector projectionGateDBConnector) Connect(context.Context) (driver.Conn, error) {
	return &projectionGateDBConn{state: connector.state}, nil
}

func (connector projectionGateDBConnector) Driver() driver.Driver { return projectionGateDBDriver{} }

type projectionGateDBDriver struct{}

func (projectionGateDBDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("projection gate test driver requires connector")
}

type projectionGateDBState struct {
	mu       sync.Mutex
	rows     [][]driver.Value
	queryErr error
	query    string
	args     []driver.NamedValue
}

type projectionGateDBConn struct {
	state *projectionGateDBState
}

func (conn *projectionGateDBConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are unsupported")
}

func (conn *projectionGateDBConn) Close() error { return nil }

func (conn *projectionGateDBConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are unsupported")
}

func (conn *projectionGateDBConn) QueryContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	conn.state.query = query
	conn.state.args = append([]driver.NamedValue(nil), args...)
	if conn.state.queryErr != nil {
		return nil, conn.state.queryErr
	}
	return &projectionGateDBRows{rows: conn.state.rows}, nil
}

type projectionGateDBRows struct {
	rows  [][]driver.Value
	index int
}

func (rows *projectionGateDBRows) Columns() []string {
	return []string{"kafka_partition", "next_offset", "updated_at_ms"}
}

func (rows *projectionGateDBRows) Close() error { return nil }

func (rows *projectionGateDBRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(destination, rows.rows[rows.index])
	rows.index++
	return nil
}

func TestSQLSourceReadsAllRuntimeProjectionOffsets(t *testing.T) {
	t.Parallel()

	state := &projectionGateDBState{rows: [][]driver.Value{
		{int64(0), int64(10), int64(100)},
		{int64(2), int64(30), int64(300)},
	}}
	database := sql.OpenDB(projectionGateDBConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	source, err := NewSQLSource(database)
	if err != nil {
		t.Fatalf("NewSQLSource() error = %v", err)
	}
	offsets, err := source.Offsets(context.Background())
	if err != nil {
		t.Fatalf("Offsets() error = %v", err)
	}
	want := map[int32]ProjectionOffset{
		0: {NextOffset: 10, UpdatedAtMs: 100},
		2: {NextOffset: 30, UpdatedAtMs: 300},
	}
	if !reflect.DeepEqual(offsets, want) {
		t.Fatalf("Offsets() = %v, want %v", offsets, want)
	}
	if !strings.Contains(state.query, "auction_projection_partition_offsets") ||
		!strings.Contains(state.query, "ORDER BY kafka_partition") {
		t.Fatalf("query = %q", state.query)
	}
	if len(state.args) != 1 || state.args[0].Value != "auction.runtime.projection.v1" {
		t.Fatalf("query args = %+v", state.args)
	}
}

func TestSQLSourceReturnsUnsafeRowsForGuardClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		row       []driver.Value
		partition int32
		want      ProjectionOffset
	}{
		{name: "negative partition", row: []driver.Value{int64(-1), int64(0), int64(1)}, partition: -1, want: ProjectionOffset{NextOffset: 0, UpdatedAtMs: 1}},
		{name: "negative offset", row: []driver.Value{int64(0), int64(-1), int64(1)}, partition: 0, want: ProjectionOffset{NextOffset: -1, UpdatedAtMs: 1}},
		{name: "missing update timestamp", row: []driver.Value{int64(0), int64(0), int64(0)}, partition: 0, want: ProjectionOffset{NextOffset: 0, UpdatedAtMs: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &projectionGateDBState{rows: [][]driver.Value{test.row}}
			database := sql.OpenDB(projectionGateDBConnector{state: state})
			t.Cleanup(func() { _ = database.Close() })
			source, _ := NewSQLSource(database)
			offsets, err := source.Offsets(context.Background())
			if err != nil || offsets[test.partition] != test.want {
				t.Fatalf("Offsets() = %v, error = %v; want unsafe row preserved", offsets, err)
			}
		})
	}
	duplicateState := &projectionGateDBState{rows: [][]driver.Value{
		{int64(0), int64(1), int64(1)},
		{int64(0), int64(2), int64(2)},
	}}
	duplicateDB := sql.OpenDB(projectionGateDBConnector{state: duplicateState})
	t.Cleanup(func() { _ = duplicateDB.Close() })
	duplicateSource, _ := NewSQLSource(duplicateDB)
	if _, err := duplicateSource.Offsets(context.Background()); err == nil {
		t.Fatal("Offsets() accepted a duplicate partition")
	}

	wantErr := errors.New("database unavailable")
	state := &projectionGateDBState{queryErr: wantErr}
	database := sql.OpenDB(projectionGateDBConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	source, _ := NewSQLSource(database)
	if _, err := source.Offsets(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Offsets() error = %v, want query failure", err)
	}
}

func TestSQLSourceRejectsMissingDatabase(t *testing.T) {
	t.Parallel()

	if _, err := NewSQLSource(nil); err == nil {
		t.Fatal("NewSQLSource(nil) error = nil")
	}
	if _, err := (*SQLSource)(nil).Offsets(context.Background()); err == nil {
		t.Fatal("nil SQLSource.Offsets() error = nil")
	}
}

var _ driver.Connector = projectionGateDBConnector{}
var _ driver.QueryerContext = (*projectionGateDBConn)(nil)
