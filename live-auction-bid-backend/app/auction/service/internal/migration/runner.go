package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	lockName       = "live_auction_schema_migrate"
	lockTimeoutSec = 30
)

var migrationNamePattern = regexp.MustCompile(`^([0-9]{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

type Migration struct {
	Version  int64
	Name     string
	UpSQL    string
	DownSQL  string
	Checksum string
}

type AppliedMigration struct {
	Version     int64
	Name        string
	Checksum    string
	AppliedAtMs int64
}

type Runner struct {
	db         *sql.DB
	migrations []Migration
}

// Verifier checks that an application database has exactly the migration set
// embedded in the running binary. Unlike Runner, it never creates or alters
// schema objects.
type Verifier struct {
	migrations []Migration
}

type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func NewRunner(db *sql.DB, source fs.FS) (*Runner, error) {
	if db == nil {
		return nil, errors.New("migration database is required")
	}
	migrations, err := Load(source)
	if err != nil {
		return nil, err
	}
	return &Runner{db: db, migrations: migrations}, nil
}

func NewVerifier(source fs.FS) (*Verifier, error) {
	migrations, err := Load(source)
	if err != nil {
		return nil, err
	}
	return &Verifier{migrations: migrations}, nil
}

// VerifyCurrent fails when a migration is missing, unknown, non-contiguous, or
// has drifted from the checksum embedded in the application binary.
func (v *Verifier) VerifyCurrent(ctx context.Context, db *sql.DB) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || len(v.migrations) == 0 {
		return errors.New("schema verifier is not initialized")
	}
	if db == nil {
		return errors.New("schema verification database is required")
	}
	items, err := queryStatus(ctx, db)
	if err != nil {
		return fmt.Errorf("read schema migration history; run auction-migrate up before starting applications: %w", err)
	}
	applied := make(map[int64]AppliedMigration, len(items))
	for _, item := range items {
		applied[item.Version] = item
	}
	next, err := unappliedIndex(v.migrations, applied)
	if err != nil {
		return fmt.Errorf("verify schema migration history: %w", err)
	}
	if next != len(v.migrations) {
		missing := v.migrations[next]
		return fmt.Errorf("database schema is behind: migration %06d_%s is not applied; run auction-migrate up", missing.Version, missing.Name)
	}
	return nil
}

func Load(source fs.FS) ([]Migration, error) {
	if source == nil {
		return nil, errors.New("migration filesystem is required")
	}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	type pair struct {
		name string
		up   string
		down string
	}
	pairs := make(map[int64]*pair)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %s", entry.Name())
		}
		content, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("migration %s is empty", entry.Name())
		}
		item := pairs[version]
		if item == nil {
			item = &pair{name: match[2]}
			pairs[version] = item
		}
		if item.name != match[2] {
			return nil, fmt.Errorf("migration version %06d has conflicting names", version)
		}
		switch match[3] {
		case "up":
			if item.up != "" {
				return nil, fmt.Errorf("migration version %06d has duplicate up files", version)
			}
			item.up = string(content)
		case "down":
			if item.down != "" {
				return nil, fmt.Errorf("migration version %06d has duplicate down files", version)
			}
			item.down = string(content)
		}
	}
	versions := make([]int64, 0, len(pairs))
	for version := range pairs {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	migrations := make([]Migration, 0, len(versions))
	for _, version := range versions {
		item := pairs[version]
		if item.up == "" || item.down == "" {
			return nil, fmt.Errorf("migration %06d_%s must have paired up and down files", version, item.name)
		}
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     item.name,
			UpSQL:    item.up,
			DownSQL:  item.down,
			Checksum: checksum(item.up, item.down),
		})
	}
	if len(migrations) == 0 {
		return nil, errors.New("no versioned migrations found")
	}
	return migrations, nil
}

func (r *Runner) Up(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.withLock(ctx, func(conn executor) error {
		applied, err := r.applied(ctx, conn)
		if err != nil {
			return err
		}
		next, err := unappliedIndex(r.migrations, applied)
		if err != nil {
			return err
		}
		for _, migration := range r.migrations[next:] {
			if _, err := conn.ExecContext(ctx, migration.UpSQL); err != nil {
				return fmt.Errorf("apply migration %06d_%s: %w", migration.Version, migration.Name, err)
			}
			if _, err := conn.ExecContext(ctx, `
INSERT INTO auction_schema_migrations (version, name, checksum, applied_at_ms)
VALUES (?, ?, ?, ?)`, migration.Version, migration.Name, migration.Checksum, time.Now().UnixMilli()); err != nil {
				return fmt.Errorf("record migration %06d_%s: %w", migration.Version, migration.Name, err)
			}
		}
		return nil
	})
}

func (r *Runner) Down(ctx context.Context, steps int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if steps <= 0 {
		return errors.New("down steps must be positive")
	}
	return r.withLock(ctx, func(conn executor) error {
		applied, err := r.status(ctx, conn)
		if err != nil {
			return err
		}
		byVersion := make(map[int64]Migration, len(r.migrations))
		for _, migration := range r.migrations {
			byVersion[migration.Version] = migration
		}
		for index, existing := range applied {
			if index >= steps {
				break
			}
			migration, ok := byVersion[existing.Version]
			if !ok {
				return fmt.Errorf("applied migration %06d is not present in the binary", existing.Version)
			}
			if existing.Name != migration.Name || existing.Checksum != migration.Checksum {
				return fmt.Errorf("migration %06d checksum drift", migration.Version)
			}
			if _, err := conn.ExecContext(ctx, migration.DownSQL); err != nil {
				return fmt.Errorf("rollback migration %06d_%s: %w", migration.Version, migration.Name, err)
			}
			if _, err := conn.ExecContext(ctx, "DELETE FROM auction_schema_migrations WHERE version = ?", migration.Version); err != nil {
				return fmt.Errorf("delete migration version %06d: %w", migration.Version, err)
			}
		}
		return nil
	})
}

// Status returns applied migrations in reverse application order.
func (r *Runner) Status(ctx context.Context) ([]AppliedMigration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.status(ctx, r.db)
}

func (r *Runner) status(ctx context.Context, conn executor) ([]AppliedMigration, error) {
	if err := r.ensureVersionTable(ctx, conn); err != nil {
		return nil, err
	}
	return queryStatus(ctx, conn)
}

func queryStatus(ctx context.Context, conn executor) ([]AppliedMigration, error) {
	rows, err := conn.QueryContext(ctx, `
SELECT version, name, checksum, applied_at_ms
FROM auction_schema_migrations
ORDER BY version DESC`)
	if err != nil {
		return nil, fmt.Errorf("query migration status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []AppliedMigration
	for rows.Next() {
		var item AppliedMigration
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum, &item.AppliedAtMs); err != nil {
			return nil, fmt.Errorf("scan migration status: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration status: %w", err)
	}
	return result, nil
}

func (r *Runner) withLock(ctx context.Context, operation func(executor) error) (returnErr error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close migration connection: %w", err)
		}
	}()

	var acquired int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, lockTimeoutSec).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if acquired != 1 {
		return errors.New("migration lock was not acquired")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		if err := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
			if returnErr == nil {
				returnErr = fmt.Errorf("release migration lock: %w", err)
			}
			return
		}
		if (!released.Valid || released.Int64 != 1) && returnErr == nil {
			returnErr = errors.New("migration lock was not released")
		}
	}()
	return operation(conn)
}

func (r *Runner) ensureVersionTable(ctx context.Context, conn executor) error {
	_, err := conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS auction_schema_migrations (
  version       BIGINT       NOT NULL,
  name          VARCHAR(128) NOT NULL,
  checksum      CHAR(64)     NOT NULL,
  applied_at_ms BIGINT       NOT NULL,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`)
	if err != nil {
		return fmt.Errorf("ensure migration version table: %w", err)
	}
	return nil
}

func (r *Runner) applied(ctx context.Context, conn executor) (map[int64]AppliedMigration, error) {
	items, err := r.status(ctx, conn)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]AppliedMigration, len(items))
	for _, item := range items {
		result[item.Version] = item
	}
	return result, nil
}

func checksum(upSQL, downSQL string) string {
	sum := sha256.Sum256([]byte(upSQL + "\x00" + downSQL))
	return hex.EncodeToString(sum[:])
}

func unappliedIndex(migrations []Migration, applied map[int64]AppliedMigration) (int, error) {
	known := make(map[int64]struct{}, len(migrations))
	next := len(migrations)
	missingSeen := false
	for index, migration := range migrations {
		known[migration.Version] = struct{}{}
		existing, ok := applied[migration.Version]
		if !ok {
			if !missingSeen {
				next = index
				missingSeen = true
			}
			continue
		}
		if existing.Name != migration.Name || existing.Checksum != migration.Checksum {
			return 0, fmt.Errorf("migration %06d checksum drift", migration.Version)
		}
		if missingSeen {
			return 0, fmt.Errorf("applied migration %06d follows a missing migration", migration.Version)
		}
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return 0, fmt.Errorf("applied migration %06d is not present in the binary", version)
		}
	}
	return next, nil
}
