package esindex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

func TestSQLFindingStoreRecordsIdempotentP0Evidence(t *testing.T) {
	record, err := searchstate.DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	database := &findingDatabaseStub{results: []sql.Result{findingResult(1)}}
	store, err := newSQLFindingStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_000 }
	if err := store.RecordIdentityConflict(context.Background(), record, errors.New("same version\ndifferent hash")); err != nil {
		t.Fatal(err)
	}
	if len(database.calls) != 1 || !strings.Contains(database.calls[0].query, "auction_reconcile_findings") ||
		database.calls[0].args[0] != ElasticsearchVersionConflictFinding || database.calls[0].args[1] != "lot-1" {
		t.Fatalf("calls=%+v", database.calls)
	}
	var detail map[string]any
	if err := json.Unmarshal(database.calls[0].args[2].([]byte), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["consumer"] != "index-es" || strings.ContainsAny(detail["error"].(string), "\r\n") {
		t.Fatalf("detail=%v", detail)
	}
}

func TestSQLFindingStoreRefreshesExistingUnresolvedFinding(t *testing.T) {
	record, err := searchstate.DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	database := &findingDatabaseStub{results: []sql.Result{findingResult(0), findingResult(1)}}
	store, err := newSQLFindingStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_000 }
	if err := store.RecordIdentityConflict(context.Background(), record, errors.New("conflict")); err != nil {
		t.Fatal(err)
	}
	if len(database.calls) != 2 || !strings.Contains(database.calls[1].query, "UPDATE auction_reconcile_findings") {
		t.Fatalf("calls=%+v", database.calls)
	}
}

func TestSQLFindingStoreRejectsInvalidInputAndWriteFailure(t *testing.T) {
	if _, err := NewSQLFindingStore(nil); err == nil {
		t.Fatal("nil SQL database was accepted")
	}
	database := &findingDatabaseStub{errs: []error{errors.New("database unavailable")}}
	store, err := newSQLFindingStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordIdentityConflict(context.Background(), searchstate.Record{}, nil); err == nil {
		t.Fatal("invalid finding was accepted")
	}
	record, err := searchstate.DecodeRecord(validLotStateKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordIdentityConflict(context.Background(), record, errors.New("conflict")); err == nil {
		t.Fatal("database failure was accepted")
	}
}

type findingCall struct {
	query string
	args  []any
}

type findingDatabaseStub struct {
	results []sql.Result
	errs    []error
	calls   []findingCall
}

func (database *findingDatabaseStub) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	database.calls = append(database.calls, findingCall{query: query, args: append([]any(nil), args...)})
	if len(database.errs) > 0 {
		err := database.errs[0]
		database.errs = database.errs[1:]
		return nil, err
	}
	if len(database.results) == 0 {
		return nil, errors.New("unexpected ExecContext")
	}
	result := database.results[0]
	database.results = database.results[1:]
	return result, nil
}

type findingResult int64

func (findingResult) LastInsertId() (int64, error) { return 0, nil }
func (result findingResult) RowsAffected() (int64, error) {
	return int64(result), nil
}
