package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/migration"
)

func TestMigrationDSNEnablesRequiredDriverOptions(t *testing.T) {
	got, err := migrationDSN("auction:secret@tcp(127.0.0.1:3306)/live_auction?charset=utf8mb4")
	if err != nil {
		t.Fatalf("migrationDSN: %v", err)
	}
	config, err := mysql.ParseDSN(got)
	if err != nil {
		t.Fatalf("parse configured DSN: %v", err)
	}
	if !config.MultiStatements {
		t.Fatal("MultiStatements must be enabled for versioned SQL files")
	}
	if !config.ParseTime {
		t.Fatal("ParseTime must be enabled")
	}
	if config.Passwd != "secret" {
		t.Fatal("migrationDSN must preserve credentials")
	}
}

func TestMigrationDSNRejectsInvalidInputWithoutLeakingIt(t *testing.T) {
	secret := "top-secret-password"
	_, err := migrationDSN("%%%" + secret)
	if err == nil {
		t.Fatal("invalid DSN should fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks DSN secret: %v", err)
	}
}

func TestRunWithFactoryRoutesCommands(t *testing.T) {
	t.Setenv("AUCTION_MYSQL_DSN", "")
	tests := []struct {
		name       string
		args       []string
		wantUp     int
		wantDown   int
		wantStatus int
		wantOutput string
	}{
		{name: "default up", args: []string{"--dsn", "auction:secret@tcp(localhost:3306)/auction"}, wantUp: 1},
		{name: "explicit up", args: []string{"--dsn", "auction:secret@tcp(localhost:3306)/auction", "up"}, wantUp: 1},
		{name: "down steps", args: []string{"--dsn", "auction:secret@tcp(localhost:3306)/auction", "--steps", "2", "down"}, wantDown: 2},
		{name: "status", args: []string{"--dsn", "auction:secret@tcp(localhost:3306)/auction", "status"}, wantStatus: 1, wantOutput: "000002 core_schema abc123 42\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeCommandRunner{status: []migration.AppliedMigration{{Version: 2, Name: "core_schema", Checksum: "abc123", AppliedAtMs: 42}}}
			cleanups := 0
			factory := func(_ context.Context, dsn string) (commandRunner, func() error, error) {
				config, err := mysql.ParseDSN(dsn)
				if err != nil {
					t.Fatalf("factory received invalid DSN: %v", err)
				}
				if !config.MultiStatements || !config.ParseTime {
					t.Fatalf("factory DSN missing required options: %s", dsn)
				}
				return fake, func() error { cleanups++; return nil }, nil
			}
			var output bytes.Buffer
			if err := runWithFactory(context.Background(), tt.args, &output, factory); err != nil {
				t.Fatalf("runWithFactory: %v", err)
			}
			if fake.upCalls != tt.wantUp || fake.downSteps != tt.wantDown || fake.statusCalls != tt.wantStatus {
				t.Fatalf("calls up/down/status = %d/%d/%d", fake.upCalls, fake.downSteps, fake.statusCalls)
			}
			if output.String() != tt.wantOutput {
				t.Fatalf("output = %q, want %q", output.String(), tt.wantOutput)
			}
			if cleanups != 1 {
				t.Fatalf("cleanup calls = %d, want 1", cleanups)
			}
		})
	}
}

func TestRunWithFactoryRejectsInvalidCLIWithoutOpeningDatabase(t *testing.T) {
	t.Setenv("AUCTION_MYSQL_DSN", "")
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing dsn", args: nil, wantErr: "mysql dsn is required"},
		{name: "unknown command", args: []string{"--dsn", "auction@/auction", "sideways"}, wantErr: "unknown migration command"},
		{name: "too many commands", args: []string{"--dsn", "auction@/auction", "up", "down"}, wantErr: "usage:"},
		{name: "invalid dsn", args: []string{"--dsn", "%%%"}, wantErr: "parse mysql dsn"},
		{name: "invalid steps flag", args: []string{"--steps", "many"}, wantErr: "invalid value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factoryCalls := 0
			factory := func(context.Context, string) (commandRunner, func() error, error) {
				factoryCalls++
				return nil, nil, errors.New("database must not be opened")
			}
			var output bytes.Buffer
			err := runWithFactory(context.Background(), tt.args, &output, factory)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
			if factoryCalls != 0 {
				t.Fatalf("factory calls = %d, want 0", factoryCalls)
			}
		})
	}
}

func TestRunWithFactoryPropagatesFactoryAndRunnerErrors(t *testing.T) {
	t.Setenv("AUCTION_MYSQL_DSN", "")
	dsnArgs := []string{"--dsn", "auction:secret@tcp(localhost:3306)/auction"}
	t.Run("factory", func(t *testing.T) {
		want := errors.New("database unavailable")
		err := runWithFactory(context.Background(), dsnArgs, &bytes.Buffer{}, func(context.Context, string) (commandRunner, func() error, error) {
			return nil, nil, want
		})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	})

	for _, command := range []string{"up", "down", "status"} {
		t.Run(command, func(t *testing.T) {
			want := fmt.Errorf("%s failed", command)
			fake := &fakeCommandRunner{err: want}
			args := append(append([]string{}, dsnArgs...), command)
			err := runWithFactory(context.Background(), args, &bytes.Buffer{}, func(context.Context, string) (commandRunner, func() error, error) {
				return fake, func() error { return nil }, nil
			})
			if !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type fakeCommandRunner struct {
	upCalls     int
	downSteps   int
	statusCalls int
	status      []migration.AppliedMigration
	err         error
}

func (r *fakeCommandRunner) Up(context.Context) error {
	r.upCalls++
	return r.err
}

func (r *fakeCommandRunner) Down(_ context.Context, steps int) error {
	r.downSteps = steps
	return r.err
}

func (r *fakeCommandRunner) Status(context.Context) ([]migration.AppliedMigration, error) {
	r.statusCalls++
	return r.status, r.err
}
