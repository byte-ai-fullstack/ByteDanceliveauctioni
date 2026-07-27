package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

type coverageBlock struct {
	location   string
	statements int64
	count      int64
}

type coverageTotals struct {
	covered int64
	total   int64
}

type packageFlags []string

func (values *packageFlags) String() string {
	return strings.Join(*values, ",")
}

func (values *packageFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("package path must not be empty")
	}
	*values = append(*values, strings.TrimSuffix(value, "/"))
	return nil
}

func main() {
	var packages packageFlags
	profilePath := flag.String("profile", "coverage-raw.out", "input Go coverage profile")
	outputPath := flag.String("output", "coverage.out", "deduplicated output profile")
	minimum := flag.Float64("min", 80, "minimum statement coverage for every selected package")
	flag.Var(&packages, "package", "package path to enforce; may be repeated")
	flag.Parse()

	if len(packages) == 0 {
		fatal(errors.New("at least one -package is required"))
	}
	mode, blocks, err := readCoverageProfile(*profilePath)
	if err != nil {
		fatal(err)
	}
	if err := writeCoverageProfile(*outputPath, mode, blocks); err != nil {
		fatal(err)
	}
	if err := enforceMinimum(blocks, packages, *minimum, os.Stdout); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "coverage gate:", err)
	os.Exit(1)
}

func readCoverageProfile(filename string) (string, map[string]coverageBlock, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", nil, fmt.Errorf("open profile: %w", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", nil, fmt.Errorf("read profile header: %w", err)
		}
		return "", nil, errors.New("coverage profile is empty")
	}
	header := strings.TrimSpace(scanner.Text())
	if !strings.HasPrefix(header, "mode: ") {
		return "", nil, fmt.Errorf("invalid coverage mode header %q", header)
	}
	mode := strings.TrimSpace(strings.TrimPrefix(header, "mode: "))
	blocks := make(map[string]coverageBlock)
	for lineNumber := 2; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return "", nil, fmt.Errorf("line %d: expected three fields", lineNumber)
		}
		statements, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || statements < 0 {
			return "", nil, fmt.Errorf("line %d: invalid statement count %q", lineNumber, fields[1])
		}
		count, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || count < 0 {
			return "", nil, fmt.Errorf("line %d: invalid execution count %q", lineNumber, fields[2])
		}
		key := fields[0] + " " + fields[1]
		if existing, exists := blocks[key]; !exists || count > existing.count {
			blocks[key] = coverageBlock{location: fields[0], statements: statements, count: count}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("read profile: %w", err)
	}
	return mode, blocks, nil
}

func writeCoverageProfile(filename, mode string, blocks map[string]coverageBlock) error {
	keys := make([]string, 0, len(blocks))
	for key := range blocks {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create merged profile: %w", err)
	}
	defer func() { _ = file.Close() }()
	writer := bufio.NewWriter(file)
	if _, err := fmt.Fprintf(writer, "mode: %s\n", mode); err != nil {
		return fmt.Errorf("write profile header: %w", err)
	}
	for _, key := range keys {
		block := blocks[key]
		if _, err := fmt.Fprintf(writer, "%s %d %d\n", block.location, block.statements, block.count); err != nil {
			return fmt.Errorf("write profile block: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush merged profile: %w", err)
	}
	return nil
}

func enforceMinimum(blocks map[string]coverageBlock, packages []string, minimum float64, output *os.File) error {
	if minimum < 0 || minimum > 100 {
		return fmt.Errorf("minimum must be between 0 and 100, got %.2f", minimum)
	}
	totals := make(map[string]coverageTotals, len(packages))
	for _, block := range blocks {
		filename, err := coverageFilename(block.location)
		if err != nil {
			return err
		}
		directory := path.Dir(filename)
		for _, packagePath := range packages {
			if directory != packagePath && !strings.HasSuffix(directory, "/"+packagePath) {
				continue
			}
			total := totals[packagePath]
			total.total += block.statements
			if block.count > 0 {
				total.covered += block.statements
			}
			totals[packagePath] = total
		}
	}

	failed := make([]string, 0)
	for _, packagePath := range packages {
		total := totals[packagePath]
		if total.total == 0 {
			failed = append(failed, packagePath+" (missing from profile)")
			continue
		}
		percentage := 100 * float64(total.covered) / float64(total.total)
		if _, err := fmt.Fprintf(output, "%6.2f%% %5d/%-5d %s\n", percentage, total.covered, total.total, packagePath); err != nil {
			return fmt.Errorf("write coverage result for %s: %w", packagePath, err)
		}
		if percentage+1e-9 < minimum {
			failed = append(failed, fmt.Sprintf("%s (%.2f%%)", packagePath, percentage))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("packages below %.2f%%: %s", minimum, strings.Join(failed, ", "))
	}
	return nil
}

func coverageFilename(location string) (string, error) {
	separator := strings.LastIndex(location, ":")
	if separator <= 0 {
		return "", fmt.Errorf("invalid coverage location %q", location)
	}
	return location[:separator], nil
}
