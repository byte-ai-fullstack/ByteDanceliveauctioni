package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCoverageProfileDeduplicatesUsingMaximumCount(t *testing.T) {
	directory := t.TempDir()
	profile := filepath.Join(directory, "raw.out")
	content := "mode: atomic\n" +
		"example.test/project/internal/core/a.go:1.1,2.2 2 0\n" +
		"example.test/project/internal/core/a.go:1.1,2.2 2 3\n" +
		"example.test/project/internal/core/a.go:3.1,4.2 1 0\n"
	if err := os.WriteFile(profile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	mode, blocks, err := readCoverageProfile(profile)
	if err != nil {
		t.Fatalf("readCoverageProfile returned error: %v", err)
	}
	if mode != "atomic" || len(blocks) != 2 {
		t.Fatalf("profile mismatch: mode=%q blocks=%+v", mode, blocks)
	}
	if blocks["example.test/project/internal/core/a.go:1.1,2.2 2"].count != 3 {
		t.Fatalf("duplicate block did not retain maximum execution count: %+v", blocks)
	}
}

func TestEnforceMinimumUsesExactPackageTotals(t *testing.T) {
	blocks := map[string]coverageBlock{
		"covered": {location: "example.test/project/internal/core/a.go:1.1,2.2", statements: 8, count: 1},
		"missed":  {location: "example.test/project/internal/core/a.go:3.1,4.2", statements: 2, count: 0},
	}
	output, err := os.CreateTemp(t.TempDir(), "coverage-output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()

	if err := enforceMinimum(blocks, []string{"internal/core"}, 80, output); err != nil {
		t.Fatalf("80%% gate rejected exact 80%% coverage: %v", err)
	}
	if err := enforceMinimum(blocks, []string{"internal/core"}, 80.01, output); err == nil {
		t.Fatal("gate accepted coverage below the configured minimum")
	}
	if err := enforceMinimum(blocks, []string{"internal/missing"}, 1, output); err == nil || !strings.Contains(err.Error(), "missing from profile") {
		t.Fatalf("missing package error = %v", err)
	}
}

func TestEnforceMinimumReportsOutputFailure(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "closed-coverage-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	blocks := map[string]coverageBlock{
		"covered": {location: "example.test/project/internal/core/a.go:1.1,2.2", statements: 1, count: 1},
	}
	if err := enforceMinimum(blocks, []string{"internal/core"}, 80, output); err == nil || !strings.Contains(err.Error(), "write coverage result") {
		t.Fatalf("closed output error=%v", err)
	}
}

func TestWriteCoverageProfileProducesDeduplicatedGoProfile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "merged.out")
	blocks := map[string]coverageBlock{
		"second": {location: "example.test/project/internal/core/b.go:3.1,4.2", statements: 1, count: 0},
		"first":  {location: "example.test/project/internal/core/a.go:1.1,2.2", statements: 2, count: 3},
	}
	if err := writeCoverageProfile(filename, "atomic", blocks); err != nil {
		t.Fatalf("writeCoverageProfile returned error: %v", err)
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	want := "mode: atomic\n" +
		"example.test/project/internal/core/a.go:1.1,2.2 2 3\n" +
		"example.test/project/internal/core/b.go:3.1,4.2 1 0\n"
	if string(content) != want {
		t.Fatalf("merged profile = %q, want %q", content, want)
	}
}
