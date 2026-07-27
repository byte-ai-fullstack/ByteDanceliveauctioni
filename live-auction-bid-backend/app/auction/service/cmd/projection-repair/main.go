package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"live-auction-bid/backend/app/auction/service/internal/kafkaclient"
	"live-auction-bid/backend/app/auction/service/internal/mysqlschema"
	"live-auction-bid/backend/app/auction/service/internal/projectionrepair"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

type commandConfig struct {
	Command            string
	Partition          int32
	Before             int64
	After              int64
	ExpectedNextOffset int64
	ThroughOffset      int64
	Execute            bool
	Operator           string
	Reason             string
	Confirm            string
	BundlePath         string
	ExpectedSHA256     string
	ExecutedBy         string
	OperationTimeout   time.Duration
}

type repairRunner interface {
	Diagnose(context.Context, projectionrepair.DiagnoseRequest) (projectionrepair.DiagnoseReport, error)
	Replay(context.Context, projectionrepair.ReplayRequest) (projectionrepair.ReplayReport, error)
	Synthetic(context.Context, projectionrepair.SyntheticRequest) (projectionrepair.SyntheticReport, error)
}

type runnerFactory func(context.Context, func(string) string) (repairRunner, func(), error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, openRunner); err != nil {
		logger.Error("projection repair failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	output io.Writer,
	factory runnerFactory,
) error {
	if getenv == nil || output == nil || factory == nil {
		return errors.New("projection repair command dependencies are required")
	}
	config, err := parseCommandConfig(args, getenv)
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, config.OperationTimeout)
	defer cancel()
	runner, closeRunner, err := factory(operationCtx, getenv)
	if err != nil {
		return err
	}
	defer closeRunner()
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	switch config.Command {
	case "diagnose":
		report, err := runner.Diagnose(operationCtx, projectionrepair.DiagnoseRequest{
			Partition: config.Partition, Before: config.Before, After: config.After,
		})
		if encodeErr := encoder.Encode(report); encodeErr != nil {
			return errors.Join(err, fmt.Errorf("write projection repair diagnostic report: %w", encodeErr))
		}
		return err
	case "replay":
		report, err := runner.Replay(operationCtx, projectionrepair.ReplayRequest{
			Partition: config.Partition, ExpectedNextOffset: config.ExpectedNextOffset,
			ThroughOffset: config.ThroughOffset, Execute: config.Execute,
			Operator: config.Operator, Reason: config.Reason,
		})
		if encodeErr := encoder.Encode(report); encodeErr != nil {
			return errors.Join(err, fmt.Errorf("write projection repair replay report: %w", encodeErr))
		}
		return err
	case "synthetic":
		report, err := runner.Synthetic(operationCtx, projectionrepair.SyntheticRequest{
			BundlePath: config.BundlePath, ExpectedSHA256: config.ExpectedSHA256,
			Execute: config.Execute, ExecutedBy: config.ExecutedBy, Confirm: config.Confirm,
		})
		if encodeErr := encoder.Encode(report); encodeErr != nil {
			return errors.Join(err, fmt.Errorf("write projection repair synthetic report: %w", encodeErr))
		}
		return err
	default:
		panic("projection repair command was validated")
	}
}

func parseCommandConfig(args []string, getenv func(string) string) (commandConfig, error) {
	if getenv == nil {
		return commandConfig{}, errors.New("projection repair environment reader is required")
	}
	if len(args) == 0 || (args[0] != "diagnose" && args[0] != "replay" && args[0] != "synthetic") {
		return commandConfig{}, errors.New("usage: projection-repair {diagnose|replay|synthetic} [flags]")
	}
	operationTimeout, err := durationSetting(getenv, "AUCTION_PROJECTION_REPAIR_OPERATION_TIMEOUT", 2*time.Minute)
	if err != nil {
		return commandConfig{}, err
	}
	config := commandConfig{
		Command: args[0], Partition: -1, Before: 3, After: 5,
		ExpectedNextOffset: -1, ThroughOffset: -1, OperationTimeout: operationTimeout,
	}
	flags := flag.NewFlagSet("projection-repair "+config.Command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if config.Command == "synthetic" {
		flags.StringVar(&config.BundlePath, "bundle", "", "read-only SyntheticRepairBundleV1 path")
		flags.StringVar(&config.ExpectedSHA256, "expected-sha256", "", "exact lowercase SHA-256 of the bundle")
		flags.BoolVar(&config.Execute, "execute", false, "apply the verified repair; default is dry-run")
		flags.StringVar(&config.ExecutedBy, "executed-by", "", "executor identity distinct from the bundle preparer")
		flags.StringVar(&config.Confirm, "confirm", "", "exact execution confirmation string from the preview")
		if err := flags.Parse(args[1:]); err != nil {
			return commandConfig{}, err
		}
		if flags.NArg() != 0 || strings.TrimSpace(config.BundlePath) == "" || strings.TrimSpace(config.ExpectedSHA256) == "" {
			return commandConfig{}, errors.New("synthetic repair requires --bundle, --expected-sha256, and no positional arguments")
		}
		if config.Execute {
			if strings.TrimSpace(config.ExecutedBy) == "" || config.Confirm == "" {
				return commandConfig{}, errors.New("synthetic --execute requires --executed-by and --confirm")
			}
		} else if config.ExecutedBy != "" || config.Confirm != "" {
			return commandConfig{}, errors.New("synthetic --executed-by and --confirm require --execute")
		}
		return config, nil
	}
	partition := flags.Int64("partition", -1, "Runtime Topic partition")
	if config.Command == "diagnose" {
		flags.Int64Var(&config.Before, "before", config.Before, "records before the DB next_offset")
		flags.Int64Var(&config.After, "after", config.After, "records from the DB next_offset")
	} else {
		flags.Int64Var(&config.ExpectedNextOffset, "expected-next-offset", -1, "required DB offset precondition")
		flags.Int64Var(&config.ThroughOffset, "through-offset", -1, "inclusive last Kafka offset to replay")
		flags.BoolVar(&config.Execute, "execute", false, "apply the verified replay; default is dry-run")
		flags.StringVar(&config.Operator, "operator", "", "operator identity recorded in the repair audit")
		flags.StringVar(&config.Reason, "reason", "", "repair reason recorded in the audit")
		flags.StringVar(&config.Confirm, "confirm", "", "exact execution confirmation string")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return commandConfig{}, err
	}
	if flags.NArg() != 0 || *partition < 0 || *partition > int64(^uint32(0)>>1) {
		return commandConfig{}, errors.New("projection repair requires one non-negative --partition and no positional arguments")
	}
	config.Partition = int32(*partition)
	if config.Command == "diagnose" {
		if config.Before < 0 || config.After < 0 || config.Before+config.After+1 > projectionrepair.MaxReplayRecords {
			return commandConfig{}, errors.New("projection repair diagnostic window is invalid")
		}
		return config, nil
	}
	if config.ExpectedNextOffset < 0 || config.ThroughOffset < config.ExpectedNextOffset ||
		config.ThroughOffset-config.ExpectedNextOffset+1 > projectionrepair.MaxReplayRecords {
		return commandConfig{}, errors.New("projection repair replay range is invalid")
	}
	if config.Execute {
		expectedConfirmation := replayConfirmation(config.Partition, config.ExpectedNextOffset, config.ThroughOffset)
		if config.Confirm != expectedConfirmation {
			return commandConfig{}, fmt.Errorf("--execute requires --confirm=%s", expectedConfirmation)
		}
		if strings.TrimSpace(config.Operator) == "" || strings.TrimSpace(config.Reason) == "" {
			return commandConfig{}, errors.New("--execute requires --operator and --reason")
		}
	} else if config.Confirm != "" {
		return commandConfig{}, errors.New("--confirm is only valid together with --execute")
	}
	return config, nil
}

func replayConfirmation(partition int32, fromOffset, throughOffset int64) string {
	return fmt.Sprintf("REPLAY_PARTITION_%d_FROM_%d_THROUGH_%d", partition, fromOffset, throughOffset)
}

func openRunner(ctx context.Context, getenv func(string) string) (repairRunner, func(), error) {
	startupTimeout, err := durationSetting(getenv, "AUCTION_PROJECTION_REPAIR_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	db, err := openMySQL(startupCtx, getenv)
	if err != nil {
		return nil, nil, err
	}
	cleanupDB := func() { _ = db.Close() }
	stateStore, err := projectionrepair.NewSQLStore(db)
	if err != nil {
		cleanupDB()
		return nil, nil, err
	}
	applier, err := projector.NewSQLStore(db)
	if err != nil {
		cleanupDB()
		return nil, nil, err
	}
	kafkaConfig, err := kafkaclient.FromEnv(getenv, []string{"127.0.0.1:19092"}, "projection-repair")
	if err != nil {
		cleanupDB()
		return nil, nil, err
	}
	source, err := projectionrepair.NewKafkaSource(startupCtx, kafkaConfig)
	if err != nil {
		cleanupDB()
		return nil, nil, err
	}
	service, err := projectionrepair.NewService(stateStore, source, applier)
	if err != nil {
		source.Close()
		cleanupDB()
		return nil, nil, err
	}
	return service, func() {
		source.Close()
		cleanupDB()
	}, nil
}

func openMySQL(ctx context.Context, getenv func(string) string) (*sql.DB, error) {
	raw := strings.TrimSpace(getenv("AUCTION_MYSQL_DSN"))
	if raw == "" {
		raw = "auction:auction_dev@tcp(127.0.0.1:13306)/live_auction?parseTime=true&charset=utf8mb4&loc=Local"
	}
	config, err := mysql.ParseDSN(raw)
	if err != nil {
		return nil, fmt.Errorf("parse projection repair MySQL DSN: %w", err)
	}
	if strings.TrimSpace(config.DBName) == "" || config.MultiStatements {
		return nil, errors.New("projection repair MySQL DSN must name a database and disable multiStatements")
	}
	config.ParseTime = true
	config.RejectReadOnly = true
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open projection repair MySQL: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping projection repair MySQL: %w", err)
	}
	verifier, err := mysqlschema.NewVerifier()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifier.VerifyCurrent(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify projection repair MySQL schema: %w", err)
	}
	return db, nil
}

func durationSetting(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}
