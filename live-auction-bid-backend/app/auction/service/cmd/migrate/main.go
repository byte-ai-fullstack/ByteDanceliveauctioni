package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/migration"
	migrationsfs "live-auction-bid/backend/deploy/mysql/migrations"
)

type commandRunner interface {
	Up(context.Context) error
	Down(context.Context, int) error
	Status(context.Context) ([]migration.AppliedMigration, error)
}

type runnerFactory func(context.Context, string) (commandRunner, func() error, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	return runWithFactory(ctx, args, os.Stdout, openRunner)
}

func runWithFactory(ctx context.Context, args []string, output io.Writer, factory runnerFactory) (returnErr error) {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(output)
	dsn := flags.String("dsn", os.Getenv("AUCTION_MYSQL_DSN"), "MySQL DSN; defaults to AUCTION_MYSQL_DSN")
	steps := flags.Int("steps", 1, "number of migrations to roll back for down")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("mysql dsn is required via --dsn or AUCTION_MYSQL_DSN")
	}
	command := "up"
	if flags.NArg() > 0 {
		command = flags.Arg(0)
	}
	if flags.NArg() > 1 {
		return errors.New("usage: migrate [--dsn DSN] [--steps N] {up|down|status}")
	}
	if command != "up" && command != "down" && command != "status" {
		return fmt.Errorf("unknown migration command %q", command)
	}

	configuredDSN, err := migrationDSN(*dsn)
	if err != nil {
		return err
	}
	runner, cleanup, err := factory(ctx, configuredDSN)
	if err != nil {
		return err
	}
	defer func() {
		if err := cleanup(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close migration runner: %w", err)
		}
	}()

	switch command {
	case "up":
		return runner.Up(ctx)
	case "down":
		return runner.Down(ctx, *steps)
	case "status":
		items, err := runner.Status(ctx)
		if err != nil {
			return err
		}
		for _, item := range items {
			if _, err := fmt.Fprintf(output, "%06d %s %s %d\n", item.Version, item.Name, item.Checksum, item.AppliedAtMs); err != nil {
				return fmt.Errorf("write migration status: %w", err)
			}
		}
		return nil
	default:
		panic("migration command was validated before runner creation")
	}
}

func openRunner(ctx context.Context, configuredDSN string) (commandRunner, func() error, error) {
	db, err := sql.Open("mysql", configuredDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("open mysql: %w", err)
	}
	cleanup := db.Close
	if err := db.PingContext(ctx); err != nil {
		_ = cleanup()
		return nil, nil, fmt.Errorf("ping mysql: %w", err)
	}
	source, err := migrationsfs.Open()
	if err != nil {
		_ = cleanup()
		return nil, nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	runner, err := migration.NewRunner(db, source)
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	return runner, cleanup, nil
}

func migrationDSN(raw string) (string, error) {
	config, err := mysql.ParseDSN(raw)
	if err != nil {
		return "", fmt.Errorf("parse mysql dsn: %w", err)
	}
	config.MultiStatements = true
	config.ParseTime = true
	return config.FormatDSN(), nil
}
