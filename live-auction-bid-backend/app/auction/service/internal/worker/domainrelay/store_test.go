package domainrelay

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestSQLStoreClaimCommitsFencedRows(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	tx := &storeTxStub{rows: &storeRowsStub{values: [][]any{
		{int64(11), "message-11", "cause-1", "auction.lot.state.v1", "lot-1", []byte("payload-1"), []byte(`{"message_id":"message-11"}`), now.Add(-time.Second).UnixMilli(), 0, ""},
		{int64(12), "message-12", "cause-2", "auction.bid.accepted.v1", "lot-2", []byte("payload-2"), []byte(`{"message_id":"message-12"}`), now.UnixMilli(), 2, "previous"},
	}}, execResults: []sql.Result{storeResult(1), storeResult(1)}}
	database := &storeDatabaseStub{tx: tx}
	tokens := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
	store, err := newSQLStore(database, func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	messages, err := store.Claim(context.Background(), "domain-relay-1", now, 2, 15*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !tx.committed || tx.rolledBack || len(messages) != 2 || len(tx.execCalls) != 2 {
		t.Fatalf("tx committed=%t rolled_back=%t messages=%d updates=%d", tx.committed, tx.rolledBack, len(messages), len(tx.execCalls))
	}
	if messages[0].LockToken != strings.Repeat("a", 32) || messages[1].LockToken != strings.Repeat("b", 32) {
		t.Fatalf("tokens=%q,%q", messages[0].LockToken, messages[1].LockToken)
	}
	if messages[0].LockedBy != "domain-relay-1" || messages[0].LockedUntilMs != now.Add(15*time.Second).UnixMilli() {
		t.Fatalf("claim metadata=%+v", messages[0])
	}
	for _, required := range []string{
		"FOR UPDATE SKIP LOCKED",
		"NOT EXISTS",
		"predecessor.topic = candidate.topic",
		"predecessor.partition_key = candidate.partition_key",
		"predecessor.id < candidate.id",
	} {
		if !strings.Contains(tx.query, required) {
			t.Fatalf("claim query is missing %q: %s", required, tx.query)
		}
	}
	if !tx.rows.(*storeRowsStub).closed {
		t.Fatalf("claim query=%q rows_closed=%t", tx.query, tx.rows.(*storeRowsStub).closed)
	}
	if got := tx.execCalls[0].args[1]; got != strings.Repeat("a", 32) {
		t.Fatalf("first update token=%v", got)
	}
}

func TestSQLStoreClaimRollsBackOnTokenOrUpdateFailure(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	row := []any{int64(11), "message-11", "cause-1", "auction.lot.state.v1", "lot-1", []byte("payload"), []byte(`{}`), now.UnixMilli(), 0, ""}
	t.Run("token", func(t *testing.T) {
		tx := &storeTxStub{rows: &storeRowsStub{values: [][]any{row}}}
		store, _ := newSQLStore(&storeDatabaseStub{tx: tx}, func() (string, error) { return "", errors.New("entropy") })
		if _, err := store.Claim(context.Background(), "relay", now, 1, time.Second); err == nil || !tx.rolledBack {
			t.Fatalf("error=%v rolled_back=%t", err, tx.rolledBack)
		}
	})
	t.Run("fenced", func(t *testing.T) {
		tx := &storeTxStub{rows: &storeRowsStub{values: [][]any{row}}, execResults: []sql.Result{storeResult(0)}}
		store, _ := newSQLStore(&storeDatabaseStub{tx: tx}, func() (string, error) { return strings.Repeat("c", 32), nil })
		if _, err := store.Claim(context.Background(), "relay", now, 1, time.Second); !errors.Is(err, ErrClaimLost) || !tx.rolledBack {
			t.Fatalf("error=%v rolled_back=%t", err, tx.rolledBack)
		}
	})
}

func TestSQLStoreFencesPublishRetryAndDeadLetterUpdates(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	message := Message{ID: 7, Attempts: 2, LockedBy: "relay", LockToken: strings.Repeat("d", 32), LockedUntilMs: now.Add(time.Minute).UnixMilli()}
	database := &storeDatabaseStub{execResults: []sql.Result{storeResult(1), storeResult(1), storeResult(1), storeResult(0)}}
	store, _ := newSQLStore(database, randomClaimToken)

	if err := store.MarkPublished(context.Background(), message, now); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if err := store.MarkFailed(context.Background(), message, now, now.Add(time.Second), 3, "broker\nfailed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if err := store.MarkDeadLettered(context.Background(), message, now, 3, strings.Repeat("界", 300)); err != nil {
		t.Fatalf("MarkDeadLettered: %v", err)
	}
	if len(database.execCalls) != 3 {
		t.Fatalf("exec calls=%d", len(database.execCalls))
	}
	if failure := database.execCalls[1].args[2].(string); strings.ContainsAny(failure, "\r\n") || failure != "broker failed" {
		t.Fatalf("sanitized failure=%q", failure)
	}
	if failure := database.execCalls[2].args[2].(string); len(failure) > maxErrorBytes {
		t.Fatalf("dead letter failure bytes=%d", len(failure))
	}
	if err := store.MarkPublished(context.Background(), message, now); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale token error=%v", err)
	}
}

func TestSQLStoreStatsCalculatesOldestAge(t *testing.T) {
	now := time.UnixMilli(1_700_000_010_000)
	database := &storeDatabaseStub{row: storeRowStub{values: []any{int64(4), now.Add(-10 * time.Second).UnixMilli(), now.Add(-7 * time.Second).UnixMilli()}}}
	store, _ := newSQLStore(database, randomClaimToken)
	stats, err := store.Stats(context.Background(), now)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Pending != 4 || stats.OldestAgeMs != 10_000 || stats.OrderVisibilityLagMs != 7_000 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestSQLStoreRejectsInvalidArgumentsAndUninitializedUse(t *testing.T) {
	if _, err := NewSQLStore(nil); !errors.Is(err, ErrInvalidStoreArgument) {
		t.Fatalf("NewSQLStore nil error=%v", err)
	}
	if _, err := newSQLStore(nil, randomClaimToken); !errors.Is(err, ErrInvalidStoreArgument) {
		t.Fatalf("newSQLStore nil error=%v", err)
	}
	now := time.UnixMilli(1_700_000_000_000)
	store, _ := newSQLStore(&storeDatabaseStub{tx: &storeTxStub{}}, randomClaimToken)
	for _, test := range []struct {
		instance string
		limit    int
		lease    time.Duration
	}{
		{"", 1, time.Second},
		{"bad:owner", 1, time.Second},
		{"relay", 0, time.Second},
		{"relay", maxClaimLimit + 1, time.Second},
		{"relay", 1, 0},
	} {
		if _, err := store.Claim(context.Background(), test.instance, now, test.limit, test.lease); !errors.Is(err, ErrInvalidStoreArgument) {
			t.Fatalf("Claim(%+v) error=%v", test, err)
		}
	}
	expired := Message{ID: 1, LockedBy: "relay", LockToken: strings.Repeat("a", 32), LockedUntilMs: now.Add(-time.Second).UnixMilli()}
	if err := store.MarkPublished(context.Background(), expired, now); !errors.Is(err, ErrInvalidStoreArgument) {
		t.Fatalf("expired publish error=%v", err)
	}
	live := expired
	live.LockedUntilMs = now.Add(time.Second).UnixMilli()
	live.Attempts = 2
	if err := store.MarkFailed(context.Background(), live, now, now, 2, "failure"); !errors.Is(err, ErrInvalidStoreArgument) {
		t.Fatalf("non-advancing retry error=%v", err)
	}
	var nilStore *SQLStore
	if _, err := nilStore.Claim(context.Background(), "relay", now, 1, time.Second); err == nil {
		t.Fatal("nil Claim returned no error")
	}
	if err := nilStore.MarkPublished(context.Background(), live, now); err == nil {
		t.Fatal("nil MarkPublished returned no error")
	}
}

func TestNewSQLStoreAndRandomTokenConstructValidStore(t *testing.T) {
	database, err := sql.Open("mysql", "auction:secret@tcp(127.0.0.1:3306)/live_auction")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	store, err := NewSQLStore(database)
	if err != nil || store == nil {
		t.Fatalf("NewSQLStore=(%v,%v)", store, err)
	}
	left, err := randomClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	right, err := randomClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	if !validClaimToken(left) || !validClaimToken(right) || left == right || validClaimToken("not-a-token") {
		t.Fatalf("tokens=%q,%q", left, right)
	}
}

type storeExecCall struct {
	query string
	args  []any
}

type storeDatabaseStub struct {
	tx          *storeTxStub
	beginErr    error
	execResults []sql.Result
	execErr     error
	execCalls   []storeExecCall
	row         sqlRow
}

func (database *storeDatabaseStub) BeginTx(context.Context, *sql.TxOptions) (sqlTransaction, error) {
	if database.beginErr != nil {
		return nil, database.beginErr
	}
	return database.tx, nil
}

func (database *storeDatabaseStub) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	database.execCalls = append(database.execCalls, storeExecCall{query: query, args: append([]any(nil), args...)})
	if database.execErr != nil {
		return nil, database.execErr
	}
	if len(database.execResults) == 0 {
		return storeResult(1), nil
	}
	result := database.execResults[0]
	database.execResults = database.execResults[1:]
	return result, nil
}

func (database *storeDatabaseStub) QueryRowContext(context.Context, string, ...any) sqlRow {
	return database.row
}

type storeTxStub struct {
	rows        sqlRows
	query       string
	queryErr    error
	execResults []sql.Result
	execErr     error
	execCalls   []storeExecCall
	committed   bool
	rolledBack  bool
}

func (tx *storeTxStub) QueryContext(_ context.Context, query string, _ ...any) (sqlRows, error) {
	tx.query = query
	return tx.rows, tx.queryErr
}

func (tx *storeTxStub) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	tx.execCalls = append(tx.execCalls, storeExecCall{query: query, args: append([]any(nil), args...)})
	if tx.execErr != nil {
		return nil, tx.execErr
	}
	result := tx.execResults[0]
	tx.execResults = tx.execResults[1:]
	return result, nil
}

func (tx *storeTxStub) Commit() error {
	tx.committed = true
	return nil
}

func (tx *storeTxStub) Rollback() error {
	if !tx.committed {
		tx.rolledBack = true
	}
	return nil
}

type storeRowsStub struct {
	values [][]any
	index  int
	err    error
	closed bool
}

func (rows *storeRowsStub) Next() bool { return rows.index < len(rows.values) }

func (rows *storeRowsStub) Scan(dest ...any) error {
	values := rows.values[rows.index]
	rows.index++
	return assignStoreValues(dest, values)
}

func (rows *storeRowsStub) Err() error   { return rows.err }
func (rows *storeRowsStub) Close() error { rows.closed = true; return nil }

type storeRowStub struct {
	values []any
	err    error
}

func (row storeRowStub) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	return assignStoreValues(dest, row.values)
}

func assignStoreValues(dest, values []any) error {
	if len(dest) != len(values) {
		return errors.New("scan arity mismatch")
	}
	for index := range dest {
		switch target := dest[index].(type) {
		case *int:
			*target = values[index].(int)
		case *int64:
			*target = values[index].(int64)
		case *string:
			*target = values[index].(string)
		case *[]byte:
			*target = append([]byte(nil), values[index].([]byte)...)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

type storeResult int64

func (storeResult) LastInsertId() (int64, error) { return 0, nil }
func (result storeResult) RowsAffected() (int64, error) {
	return int64(result), nil
}
