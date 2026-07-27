package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

func TestLoadSortsAndChecksumsPairedMigrations(t *testing.T) {
	source := migrationFS(
		"000002_second.up.sql", "UP TWO",
		"000002_second.down.sql", "DOWN TWO",
		"000001_first.up.sql", "UP ONE",
		"000001_first.down.sql", "DOWN ONE",
		"legacy.sql", "ignored",
	)

	got, err := Load(source)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("migrations not sorted by version: %+v", got)
	}
	if got[0].Name != "first" || got[0].UpSQL != "UP ONE" || got[0].DownSQL != "DOWN ONE" {
		t.Fatalf("unexpected first migration: %+v", got[0])
	}
	if got[0].Checksum != checksum("UP ONE", "DOWN ONE") || len(got[0].Checksum) != 64 {
		t.Fatalf("unexpected checksum: %q", got[0].Checksum)
	}
}

func TestLoadRejectsInvalidMigrationSets(t *testing.T) {
	tests := []struct {
		name    string
		source  fs.FS
		wantErr string
	}{
		{name: "nil filesystem", source: nil, wantErr: "filesystem is required"},
		{name: "no versioned migrations", source: fstest.MapFS{}, wantErr: "no versioned migrations"},
		{name: "empty file", source: migrationFS("000001_empty.up.sql", " ", "000001_empty.down.sql", "DOWN"), wantErr: "is empty"},
		{name: "zero version", source: migrationFS("000000_zero.up.sql", "UP", "000000_zero.down.sql", "DOWN"), wantErr: "invalid migration version"},
		{name: "missing down", source: migrationFS("000001_first.up.sql", "UP"), wantErr: "must have paired"},
		{name: "conflicting names", source: migrationFS("000001_first.up.sql", "UP", "000001_other.down.sql", "DOWN"), wantErr: "conflicting names"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.source)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewRunnerValidatesDependencies(t *testing.T) {
	if _, err := NewRunner(nil, migrationFS("000001_first.up.sql", "UP", "000001_first.down.sql", "DOWN")); err == nil {
		t.Fatal("NewRunner should reject a nil database")
	}
	db, _ := openFakeDB(t)
	if _, err := NewRunner(db, fstest.MapFS{}); err == nil {
		t.Fatal("NewRunner should reject an invalid migration filesystem")
	}
}

func TestVerifierRequiresCurrentReadOnlySchema(t *testing.T) {
	db, state := openFakeDB(t)
	verifier := newTestVerifier(t)

	if err := verifier.VerifyCurrent(context.Background(), db); err == nil || !strings.Contains(err.Error(), "database schema is behind") {
		t.Fatalf("empty schema VerifyCurrent error = %v", err)
	}
	state.mu.Lock()
	if len(state.migrationStatements) != 0 {
		t.Fatalf("schema verifier executed DDL: %v", state.migrationStatements)
	}
	state.mu.Unlock()

	for _, migration := range verifier.migrations {
		state.applied[migration.Version] = AppliedMigration{
			Version:  migration.Version,
			Name:     migration.Name,
			Checksum: migration.Checksum,
		}
	}
	if err := verifier.VerifyCurrent(context.Background(), db); err != nil {
		t.Fatalf("current schema VerifyCurrent: %v", err)
	}
}

func TestVerifierRejectsUnsafeMigrationHistory(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*fakeState, *Verifier)
		wantErr string
	}{
		{
			name: "checksum drift",
			arrange: func(state *fakeState, verifier *Verifier) {
				first := verifier.migrations[0]
				state.applied[first.Version] = AppliedMigration{Version: first.Version, Name: first.Name, Checksum: "changed"}
			},
			wantErr: "checksum drift",
		},
		{
			name: "non-contiguous history",
			arrange: func(state *fakeState, verifier *Verifier) {
				second := verifier.migrations[1]
				state.applied[second.Version] = AppliedMigration{Version: second.Version, Name: second.Name, Checksum: second.Checksum}
			},
			wantErr: "follows a missing migration",
		},
		{
			name: "unknown applied version",
			arrange: func(state *fakeState, _ *Verifier) {
				state.applied[99] = AppliedMigration{Version: 99, Name: "unknown", Checksum: "unknown"}
			},
			wantErr: "not present in the binary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, state := openFakeDB(t)
			verifier := newTestVerifier(t)
			tt.arrange(state, verifier)
			if err := verifier.VerifyCurrent(context.Background(), db); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("VerifyCurrent error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestVerifierValidatesDependenciesAndContext(t *testing.T) {
	if _, err := NewVerifier(fstest.MapFS{}); err == nil {
		t.Fatal("NewVerifier should reject an invalid migration filesystem")
	}
	verifier := newTestVerifier(t)
	if err := verifier.VerifyCurrent(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("nil database VerifyCurrent error = %v", err)
	}
	db, _ := openFakeDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifier.VerifyCurrent(ctx, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled VerifyCurrent error = %v", err)
	}
}

func TestRunnerUpStatusAndIdempotencyUseOneConnection(t *testing.T) {
	db, state := openFakeDB(t)
	runner := newTestRunner(t, db)

	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	items, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(items) != 2 || items[0].Version != 2 || items[1].Version != 1 {
		t.Fatalf("unexpected status: %+v", items)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if got := strings.Join(state.migrationStatements, ","); got != "UP ONE,UP TWO" {
		t.Fatalf("migration statements = %q", got)
	}
	if state.connectionViolation {
		t.Fatal("migration work escaped the advisory-lock connection")
	}
	if len(state.lockConnections) != 2 || len(state.releaseConnections) != 2 {
		t.Fatalf("lock/release calls = %v/%v", state.lockConnections, state.releaseConnections)
	}
	for index := range state.lockConnections {
		if state.lockConnections[index] != state.releaseConnections[index] {
			t.Fatalf("lock and release used different connections: %v/%v", state.lockConnections, state.releaseConnections)
		}
	}
}

func TestRunnerDownRollsBackNewestMigration(t *testing.T) {
	db, state := openFakeDB(t)
	runner := newTestRunner(t, db)
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := runner.Down(context.Background(), 1); err != nil {
		t.Fatalf("Down: %v", err)
	}

	items, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(items) != 1 || items[0].Version != 1 {
		t.Fatalf("unexpected status after down: %+v", items)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if got := strings.Join(state.migrationStatements, ","); got != "UP ONE,UP TWO,DOWN TWO" {
		t.Fatalf("migration statements = %q", got)
	}
}

func TestRunnerRejectsUnsafeOrDriftedOperations(t *testing.T) {
	t.Run("non-positive down steps", func(t *testing.T) {
		db, _ := openFakeDB(t)
		runner := newTestRunner(t, db)
		if err := runner.Down(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Fatalf("Down error = %v", err)
		}
	})

	t.Run("checksum drift", func(t *testing.T) {
		db, state := openFakeDB(t)
		runner := newTestRunner(t, db)
		state.applied[1] = AppliedMigration{Version: 1, Name: "first", Checksum: "changed"}
		if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum drift") {
			t.Fatalf("Up error = %v", err)
		}
	})

	t.Run("unknown applied migration", func(t *testing.T) {
		db, state := openFakeDB(t)
		runner := newTestRunner(t, db)
		state.applied[99] = AppliedMigration{Version: 99, Name: "unknown", Checksum: "unknown"}
		if err := runner.Down(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "not present in the binary") {
			t.Fatalf("Down error = %v", err)
		}
	})

	t.Run("unknown applied migration blocks up", func(t *testing.T) {
		db, state := openFakeDB(t)
		runner := newTestRunner(t, db)
		state.applied[99] = AppliedMigration{Version: 99, Name: "unknown", Checksum: "unknown"}
		if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "not present in the binary") {
			t.Fatalf("Up error = %v", err)
		}
	})

	t.Run("non-contiguous history blocks up", func(t *testing.T) {
		db, state := openFakeDB(t)
		runner := newTestRunner(t, db)
		second := runner.migrations[1]
		state.applied[second.Version] = AppliedMigration{
			Version:  second.Version,
			Name:     second.Name,
			Checksum: second.Checksum,
		}
		if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "follows a missing migration") {
			t.Fatalf("Up error = %v", err)
		}
	})

	t.Run("lock not acquired", func(t *testing.T) {
		db, state := openFakeDB(t)
		state.acquireResult = 0
		runner := newTestRunner(t, db)
		if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "not acquired") {
			t.Fatalf("Up error = %v", err)
		}
	})

	t.Run("lock release failure", func(t *testing.T) {
		db, state := openFakeDB(t)
		state.failRelease = true
		runner := newTestRunner(t, db)
		if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "release migration lock") {
			t.Fatalf("Up error = %v", err)
		}
	})

	t.Run("migration statement failure", func(t *testing.T) {
		db, state := openFakeDB(t)
		state.failExecContains = "UP ONE"
		runner := newTestRunner(t, db)
		if err := runner.Up(context.Background()); err == nil || !strings.Contains(err.Error(), "apply migration") {
			t.Fatalf("Up error = %v", err)
		}
		if len(state.applied) != 0 {
			t.Fatalf("failed migration was recorded: %+v", state.applied)
		}
	})
}

func TestRunnerHonorsCanceledContexts(t *testing.T) {
	db, _ := openFakeDB(t)
	runner := newTestRunner(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Up(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Up error = %v", err)
	}
	if err := runner.Down(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Down error = %v", err)
	}
	if _, err := runner.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Status error = %v", err)
	}
}

func migrationFS(files ...string) fstest.MapFS {
	result := make(fstest.MapFS, len(files)/2)
	for index := 0; index < len(files); index += 2 {
		result[files[index]] = &fstest.MapFile{Data: []byte(files[index+1])}
	}
	return result
}

func newTestRunner(t *testing.T, db *sql.DB) *Runner {
	t.Helper()
	runner, err := NewRunner(db, migrationFS(
		"000002_second.up.sql", "UP TWO",
		"000002_second.down.sql", "DOWN TWO",
		"000001_first.up.sql", "UP ONE",
		"000001_first.down.sql", "DOWN ONE",
	))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func newTestVerifier(t *testing.T) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(migrationFS(
		"000002_second.up.sql", "UP TWO",
		"000002_second.down.sql", "DOWN TWO",
		"000001_first.up.sql", "UP ONE",
		"000001_first.down.sql", "DOWN ONE",
	))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return verifier
}

var fakeDriverID uint64

func openFakeDB(t *testing.T) (*sql.DB, *fakeState) {
	t.Helper()
	state := &fakeState{
		acquireResult: 1,
		releaseResult: 1,
		applied:       make(map[int64]AppliedMigration),
	}
	name := fmt.Sprintf("migration_fake_%d", atomic.AddUint64(&fakeDriverID, 1))
	sql.Register(name, &fakeDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open fake database: %v", err)
	}
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return db, state
}

type fakeState struct {
	mu                   sync.Mutex
	nextConnection       int
	activeLockConnection int
	acquireResult        int64
	releaseResult        int64
	failRelease          bool
	failExecContains     string
	connectionViolation  bool
	lockConnections      []int
	releaseConnections   []int
	migrationStatements  []string
	applied              map[int64]AppliedMigration
}

type fakeDriver struct {
	state *fakeState
}

func (d *fakeDriver) Open(string) (driver.Conn, error) {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	d.state.nextConnection++
	return &fakeConn{id: d.state.nextConnection, state: d.state}, nil
}

type fakeConn struct {
	id    int
	state *fakeState
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported by migration fake")
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by migration fake")
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if err := c.state.verifyLockedConnection(c.id); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if c.state.failExecContains != "" && strings.Contains(query, c.state.failExecContains) {
		return nil, errors.New("injected migration execution failure")
	}
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS auction_schema_migrations"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(query, "INSERT INTO auction_schema_migrations"):
		item := AppliedMigration{
			Version:     args[0].Value.(int64),
			Name:        args[1].Value.(string),
			Checksum:    args[2].Value.(string),
			AppliedAtMs: args[3].Value.(int64),
		}
		c.state.applied[item.Version] = item
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(query, "DELETE FROM auction_schema_migrations"):
		delete(c.state.applied, args[0].Value.(int64))
		return driver.RowsAffected(1), nil
	default:
		c.state.migrationStatements = append(c.state.migrationStatements, query)
		return driver.RowsAffected(1), nil
	}
}

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	query = strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(query, "SELECT GET_LOCK"):
		if c.state.acquireResult == 1 {
			c.state.activeLockConnection = c.id
			c.state.lockConnections = append(c.state.lockConnections, c.id)
		}
		return &fakeRows{columns: []string{"GET_LOCK"}, values: [][]driver.Value{{c.state.acquireResult}}}, nil
	case strings.HasPrefix(query, "SELECT RELEASE_LOCK"):
		if c.state.failRelease {
			return nil, errors.New("injected release failure")
		}
		if err := c.state.verifyLockedConnection(c.id); err != nil {
			return nil, err
		}
		c.state.releaseConnections = append(c.state.releaseConnections, c.id)
		c.state.activeLockConnection = 0
		return &fakeRows{columns: []string{"RELEASE_LOCK"}, values: [][]driver.Value{{c.state.releaseResult}}}, nil
	case strings.HasPrefix(query, "SELECT version, name, checksum, applied_at_ms"):
		if err := c.state.verifyLockedConnection(c.id); err != nil {
			return nil, err
		}
		versions := make([]int64, 0, len(c.state.applied))
		for version := range c.state.applied {
			versions = append(versions, version)
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
		values := make([][]driver.Value, 0, len(versions))
		for _, version := range versions {
			item := c.state.applied[version]
			values = append(values, []driver.Value{item.Version, item.Name, item.Checksum, item.AppliedAtMs})
		}
		return &fakeRows{columns: []string{"version", "name", "checksum", "applied_at_ms"}, values: values}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (s *fakeState) verifyLockedConnection(connectionID int) error {
	if s.activeLockConnection != 0 && s.activeLockConnection != connectionID {
		s.connectionViolation = true
		return errors.New("query used a connection that does not own the migration lock")
	}
	return nil
}

type fakeRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *fakeRows) Columns() []string { return r.columns }

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
