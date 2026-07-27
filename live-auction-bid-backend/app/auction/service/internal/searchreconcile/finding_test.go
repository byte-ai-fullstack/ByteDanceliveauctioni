package searchreconcile

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestSQLFindingStoreInsertsAndRefreshesStableFinding(t *testing.T) {
	database := &fakeFindingDatabase{results: []sql.Result{driver.RowsAffected(1)}}
	store := &SQLFindingStore{db: database, nowMs: func() int64 { return 1_700_000_000_000 }}
	if err := store.Record(context.Background(), findingFixture()); err != nil {
		t.Fatal(err)
	}
	if len(database.calls) != 1 || !strings.Contains(database.calls[0], "auction_reconcile_findings") {
		t.Fatalf("calls=%+v", database.calls)
	}
	database.results = []sql.Result{driver.RowsAffected(0), driver.RowsAffected(1)}
	database.calls = nil
	if err := store.Record(context.Background(), findingFixture()); err != nil || len(database.calls) != 2 || !strings.Contains(database.calls[1], "UPDATE") {
		t.Fatalf("calls=%+v error=%v", database.calls, err)
	}
}

func TestSQLFindingStoreRejectsInvalidAndDatabaseFailures(t *testing.T) {
	store := &SQLFindingStore{db: &fakeFindingDatabase{}, nowMs: func() int64 { return 1 }}
	if err := store.Record(context.Background(), Finding{}); err == nil {
		t.Fatal("invalid finding was accepted")
	}
	database := &fakeFindingDatabase{err: errors.New("mysql unavailable")}
	store = &SQLFindingStore{db: database, nowMs: func() int64 { return 1 }}
	if err := store.Record(context.Background(), findingFixture()); err == nil {
		t.Fatal("database failure was ignored")
	}
}

type fakeFindingDatabase struct {
	calls   []string
	results []sql.Result
	err     error
}

func (database *fakeFindingDatabase) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	database.calls = append(database.calls, query)
	if database.err != nil {
		return nil, database.err
	}
	if len(database.results) == 0 {
		return driver.RowsAffected(1), nil
	}
	result := database.results[0]
	database.results = database.results[1:]
	return result, nil
}

func findingFixture() Finding {
	return Finding{
		Sink: SinkElasticsearch, Result: ResultConflict, LotID: "lot-1",
		Expected: Identity{Found: true, LotVersion: 7, LastEventID: "event-1", ContentHash: strings.Repeat("a", 64)},
		Actual:   Identity{Found: true, LotVersion: 7, LastEventID: "event-2", ContentHash: strings.Repeat("b", 64)},
	}
}
