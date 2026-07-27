package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestAddMySQLActiveLotIDsIncludesDurableProjectionInFailoverAudit(t *testing.T) {
	connector := &activeLotConnector{lotIDs: []string{"lot-mysql-live", " lot-shared ", ""}}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })

	reconciler := &RuntimeReconciler{db: db}
	seen := map[string]struct{}{"lot-shared": {}, "lot-redis-only": {}}
	if err := reconciler.addMySQLActiveLotIDs(context.Background(), seen); err != nil {
		t.Fatalf("addMySQLActiveLotIDs: %v", err)
	}
	for _, lotID := range []string{"lot-mysql-live", "lot-shared", "lot-redis-only"} {
		if _, ok := seen[lotID]; !ok {
			t.Fatalf("active lot %q was omitted: %+v", lotID, seen)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("active lot union=%+v", seen)
	}
	if !connector.queried {
		t.Fatal("MySQL active projection was not queried")
	}
}

func TestAddMySQLActiveLotIDsRejectsMissingDependencies(t *testing.T) {
	if err := (*RuntimeReconciler)(nil).addMySQLActiveLotIDs(context.Background(), map[string]struct{}{}); err == nil {
		t.Fatal("nil reconciler should fail")
	}
	if err := (&RuntimeReconciler{}).addMySQLActiveLotIDs(context.Background(), nil); err == nil {
		t.Fatal("nil database and lot set should fail")
	}
}

type activeLotConnector struct {
	lotIDs  []string
	queried bool
}

func (connector *activeLotConnector) Connect(context.Context) (driver.Conn, error) {
	return &activeLotConn{connector: connector}, nil
}

func (connector *activeLotConnector) Driver() driver.Driver { return activeLotDriver{} }

type activeLotDriver struct{}

func (activeLotDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("active lot test driver requires Connector")
}

type activeLotConn struct{ connector *activeLotConnector }

func (connection *activeLotConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (connection *activeLotConn) Close() error { return nil }

func (connection *activeLotConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (connection *activeLotConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "FROM auction_lots") || !strings.Contains(query, "WHERE status IN") {
		return nil, errors.New("unexpected active lot query")
	}
	if len(args) != 2 || args[0].Value != int64(v1.LotStatus_LOT_STATUS_LIVE) || args[1].Value != int64(v1.LotStatus_LOT_STATUS_EXTENDED) {
		return nil, errors.New("unexpected active lot statuses")
	}
	connection.connector.queried = true
	return &activeLotRows{lotIDs: append([]string(nil), connection.connector.lotIDs...)}, nil
}

type activeLotRows struct {
	lotIDs []string
	index  int
}

func (rows *activeLotRows) Columns() []string { return []string{"id"} }
func (rows *activeLotRows) Close() error      { return nil }

func (rows *activeLotRows) Next(values []driver.Value) error {
	if rows.index >= len(rows.lotIDs) {
		return io.EOF
	}
	values[0] = rows.lotIDs[rows.index]
	rows.index++
	return nil
}
