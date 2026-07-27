package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/projectionrepair"
)

func TestParseCommandConfigDiagnoseAndReplay(t *testing.T) {
	getenv := func(string) string { return "" }
	diagnose, err := parseCommandConfig([]string{"diagnose", "--partition=2", "--before=4", "--after=6"}, getenv)
	if err != nil || diagnose.Command != "diagnose" || diagnose.Partition != 2 || diagnose.Before != 4 || diagnose.After != 6 {
		t.Fatalf("diagnose=%+v error=%v", diagnose, err)
	}
	dryRun, err := parseCommandConfig([]string{
		"replay", "--partition=3", "--expected-next-offset=10", "--through-offset=12",
	}, getenv)
	if err != nil || dryRun.Execute || dryRun.ExpectedNextOffset != 10 || dryRun.ThroughOffset != 12 {
		t.Fatalf("dryRun=%+v error=%v", dryRun, err)
	}
	confirmation := replayConfirmation(3, 10, 12)
	execute, err := parseCommandConfig([]string{
		"replay", "--partition=3", "--expected-next-offset=10", "--through-offset=12", "--execute",
		"--operator=operator-1", "--reason=approved gap recovery", "--confirm=" + confirmation,
	}, getenv)
	if err != nil || !execute.Execute || execute.Confirm != confirmation {
		t.Fatalf("execute=%+v error=%v", execute, err)
	}
}

func TestParseCommandConfigSynthetic(t *testing.T) {
	getenv := func(string) string { return "" }
	digest := strings.Repeat("a", 64)
	dryRun, err := parseCommandConfig([]string{
		"synthetic", "--bundle=/evidence/repair.json", "--expected-sha256=" + digest,
	}, getenv)
	if err != nil || dryRun.Execute || dryRun.BundlePath != "/evidence/repair.json" || dryRun.ExpectedSHA256 != digest {
		t.Fatalf("dryRun=%+v error=%v", dryRun, err)
	}
	execute, err := parseCommandConfig([]string{
		"synthetic", "--bundle=/evidence/repair.json", "--expected-sha256=" + digest,
		"--execute", "--executed-by=engineer-b", "--confirm=exact-confirmation",
	}, getenv)
	if err != nil || !execute.Execute || execute.ExecutedBy != "engineer-b" {
		t.Fatalf("execute=%+v error=%v", execute, err)
	}
}

func TestParseCommandConfigRejectsUnsafeExecutionAndInvalidRanges(t *testing.T) {
	getenv := func(string) string { return "" }
	values := [][]string{
		nil,
		{"other"},
		{"diagnose", "--partition=-1"},
		{"diagnose", "--partition=0", "--before=1000", "--after=1"},
		{"replay", "--partition=0", "--expected-next-offset=10", "--through-offset=9"},
		{"replay", "--partition=0", "--expected-next-offset=10", "--through-offset=10", "--execute", "--operator=op", "--reason=gap"},
		{"replay", "--partition=0", "--expected-next-offset=10", "--through-offset=10", "--confirm=unexpected"},
		{"synthetic", "--expected-sha256=" + strings.Repeat("a", 64)},
		{"synthetic", "--bundle=/evidence/bundle.json", "--expected-sha256=" + strings.Repeat("a", 64), "--executed-by=engineer-b"},
		{"synthetic", "--bundle=/evidence/bundle.json", "--expected-sha256=" + strings.Repeat("a", 64), "--execute"},
	}
	for _, args := range values {
		if _, err := parseCommandConfig(args, getenv); err == nil {
			t.Fatalf("args=%v were accepted", args)
		}
	}
	if _, err := parseCommandConfig([]string{"diagnose", "--partition=0"}, func(name string) string {
		if name == "AUCTION_PROJECTION_REPAIR_OPERATION_TIMEOUT" {
			return "invalid"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid operation timeout was accepted")
	}
}

func TestRunWritesDiagnosticAndReplayReportsAndClosesResources(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "diagnose", args: []string{"diagnose", "--partition=1"}},
		{name: "replay", args: []string{"replay", "--partition=1", "--expected-next-offset=10", "--through-offset=10"}},
		{name: "synthetic", args: []string{"synthetic", "--bundle=/evidence/bundle.json", "--expected-sha256=" + strings.Repeat("a", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			closed := false
			runner := &repairRunnerStub{}
			var output bytes.Buffer
			err := run(context.Background(), test.args, getenv, &output, func(context.Context, func(string) string) (repairRunner, func(), error) {
				return runner, func() { closed = true }, nil
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !closed || (test.name != "synthetic" && !strings.Contains(output.String(), `"partition": 1`)) {
				t.Fatalf("closed=%t output=%s", closed, output.String())
			}
			if test.name == "diagnose" && runner.diagnoseCalls != 1 {
				t.Fatalf("diagnose calls=%d", runner.diagnoseCalls)
			}
			if test.name == "replay" && runner.replayCalls != 1 {
				t.Fatalf("replay calls=%d", runner.replayCalls)
			}
			if test.name == "synthetic" && runner.syntheticCalls != 1 {
				t.Fatalf("synthetic calls=%d", runner.syntheticCalls)
			}
		})
	}
}

func TestRunPreservesReportWhenRunnerFails(t *testing.T) {
	var output bytes.Buffer
	wantErr := errors.New("diagnostic failed")
	err := run(context.Background(), []string{"diagnose", "--partition=1"}, func(string) string { return "" }, &output,
		func(context.Context, func(string) string) (repairRunner, func(), error) {
			return &repairRunnerStub{err: wantErr}, func() {}, nil
		})
	if !errors.Is(err, wantErr) || !strings.Contains(output.String(), `"partition": 1`) {
		t.Fatalf("error=%v output=%s", err, output.String())
	}
}

type repairRunnerStub struct {
	diagnoseCalls  int
	replayCalls    int
	syntheticCalls int
	err            error
}

func (stub *repairRunnerStub) Diagnose(_ context.Context, request projectionrepair.DiagnoseRequest) (projectionrepair.DiagnoseReport, error) {
	stub.diagnoseCalls++
	return projectionrepair.DiagnoseReport{Partition: request.Partition}, stub.err
}

func (stub *repairRunnerStub) Replay(_ context.Context, request projectionrepair.ReplayRequest) (projectionrepair.ReplayReport, error) {
	stub.replayCalls++
	return projectionrepair.ReplayReport{Partition: request.Partition}, stub.err
}

func (stub *repairRunnerStub) Synthetic(_ context.Context, request projectionrepair.SyntheticRequest) (projectionrepair.SyntheticReport, error) {
	stub.syntheticCalls++
	return projectionrepair.SyntheticReport{BundleSHA256: request.ExpectedSHA256}, stub.err
}
