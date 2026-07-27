package closeworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/observability"
)

type RuntimeStore interface {
	ScanDueRuntimeLotIDs(ctx context.Context, limit int64) ([]string, error)
	ExecuteCloseIfExpired(ctx context.Context, lotID, orderID, traceID string) (*v1.RuntimeFactV1, error)
	PingRuntime(ctx context.Context) error
}

type Config struct {
	Interval         time.Duration
	BatchLimit       int64
	Concurrency      int
	OperationTimeout time.Duration
}

type Summary struct {
	Candidates int   `json:"candidates"`
	Settled    int   `json:"settled"`
	Failed     int   `json:"failed"`
	NotExpired int   `json:"not_expired"`
	Duplicates int   `json:"duplicates"`
	Errors     int   `json:"errors"`
	DurationMs int64 `json:"duration_ms"`
}

type Stats struct {
	Ready           bool      `json:"ready"`
	LastAttemptAt   time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt   time.Time `json:"last_success_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	LastSummary     Summary   `json:"last_summary"`
	ConsecutiveFail int       `json:"consecutive_failures"`
}

type Worker struct {
	store  RuntimeStore
	config Config
	logger *slog.Logger
	ready  atomic.Bool

	mu    sync.RWMutex
	stats Stats
}

func New(store RuntimeStore, config Config, logger *slog.Logger) (*Worker, error) {
	if store == nil {
		return nil, errors.New("close worker runtime store is required")
	}
	if logger == nil {
		return nil, errors.New("close worker logger is required")
	}
	if config.Interval <= 0 || config.Interval > time.Minute {
		return nil, errors.New("close worker interval must be between 1ns and 1m")
	}
	if config.BatchLimit <= 0 || config.BatchLimit > 1000 {
		return nil, errors.New("close worker batch limit must be between 1 and 1000")
	}
	if config.Concurrency <= 0 || config.Concurrency > 64 {
		return nil, errors.New("close worker concurrency must be between 1 and 64")
	}
	if config.OperationTimeout <= 0 || config.OperationTimeout > time.Minute {
		return nil, errors.New("close worker operation timeout must be between 1ns and 1m")
	}
	return &Worker{store: store, config: config, logger: logger}, nil
}

func (worker *Worker) Run(ctx context.Context) error {
	if worker == nil {
		return errors.New("close worker is nil")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			worker.ready.Store(false)
			return nil
		case <-timer.C:
			if _, err := worker.RunOnce(ctx); err != nil && ctx.Err() == nil {
				worker.logger.Error("runtime close batch failed", slog.Any("error", err))
			}
			timer.Reset(worker.config.Interval)
		}
	}
}

func (worker *Worker) RunOnce(ctx context.Context) (Summary, error) {
	started := time.Now()
	worker.recordAttempt(started)
	candidates, err := worker.store.ScanDueRuntimeLotIDs(ctx, worker.config.BatchLimit)
	if err != nil {
		worker.recordFailure(err, Summary{DurationMs: time.Since(started).Milliseconds()})
		observability.RecordRuntimeCloseBatch(0, time.Since(started))
		return Summary{}, err
	}

	results := make(chan closeResult, len(candidates))
	semaphore := make(chan struct{}, worker.config.Concurrency)
	var group sync.WaitGroup
	for _, lotID := range candidates {
		lotID := lotID
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- closeResult{err: ctx.Err()}
				return
			}
			results <- worker.closeOne(ctx, lotID)
		}()
	}
	group.Wait()
	close(results)

	summary := Summary{Candidates: len(candidates)}
	var failures []error
	for result := range results {
		switch result.kind {
		case resultSettled:
			summary.Settled++
		case resultFailed:
			summary.Failed++
		case resultNotExpired:
			summary.NotExpired++
		case resultDuplicate:
			summary.Duplicates++
		default:
			summary.Errors++
			if result.err != nil {
				failures = append(failures, result.err)
			}
		}
	}
	summary.DurationMs = time.Since(started).Milliseconds()
	observability.RecordRuntimeCloseBatch(len(candidates), time.Since(started))
	if len(failures) > 0 {
		err = errors.Join(failures...)
		worker.recordFailure(err, summary)
		return summary, err
	}
	worker.recordSuccess(summary)
	return summary, nil
}

func (worker *Worker) Ready() bool {
	return worker != nil && worker.ready.Load()
}

func (worker *Worker) Ping(ctx context.Context) error {
	if worker == nil || worker.store == nil {
		return errors.New("close worker is not initialized")
	}
	return worker.store.PingRuntime(ctx)
}

func (worker *Worker) Stats() Stats {
	if worker == nil {
		return Stats{}
	}
	worker.mu.RLock()
	defer worker.mu.RUnlock()
	return worker.stats
}

type resultKind string

const (
	resultSettled    resultKind = "settled"
	resultFailed     resultKind = "failed"
	resultNotExpired resultKind = "not_expired"
	resultDuplicate  resultKind = "duplicate"
	resultError      resultKind = "error"
)

type closeResult struct {
	kind resultKind
	err  error
}

func (worker *Worker) closeOne(parent context.Context, lotID string) closeResult {
	orderID, err := eventcontract.RuntimeOrderID(lotID)
	if err != nil {
		observability.RecordRuntimeCloseResult(string(resultError))
		return closeResult{kind: resultError, err: err}
	}
	ctx, cancel := context.WithTimeout(parent, worker.config.OperationTimeout)
	defer cancel()
	fact, err := worker.store.ExecuteCloseIfExpired(ctx, lotID, orderID, "")
	if err != nil {
		var rejection *auction.RuntimeDecisionError
		if errors.As(err, &rejection) {
			switch rejection.Code {
			case auction.RuntimeCodeNotExpired:
				observability.RecordRuntimeCloseResult(string(resultNotExpired))
				return closeResult{kind: resultNotExpired}
			case auction.RuntimeCodeNotLive, auction.RuntimeCodeAlreadyTerminal:
				observability.RecordRuntimeCloseResult(string(resultDuplicate))
				return closeResult{kind: resultDuplicate}
			}
		}
		observability.RecordRuntimeCloseResult(string(resultError))
		return closeResult{kind: resultError, err: fmt.Errorf("close runtime lot %q: %w", lotID, err)}
	}
	if fact == nil || fact.GetStateAfter() == nil {
		observability.RecordRuntimeCloseResult(string(resultError))
		return closeResult{kind: resultError, err: fmt.Errorf("close runtime lot %q returned an incomplete fact", lotID)}
	}
	switch fact.GetStateAfter().GetStatus() {
	case v1.LotStatus_LOT_STATUS_SETTLED:
		observability.RecordRuntimeCloseResult(string(resultSettled))
		return closeResult{kind: resultSettled}
	case v1.LotStatus_LOT_STATUS_FAILED:
		observability.RecordRuntimeCloseResult(string(resultFailed))
		return closeResult{kind: resultFailed}
	default:
		observability.RecordRuntimeCloseResult(string(resultError))
		return closeResult{kind: resultError, err: fmt.Errorf("close runtime lot %q returned status %s", lotID, fact.GetStateAfter().GetStatus())}
	}
}

func (worker *Worker) recordAttempt(at time.Time) {
	worker.mu.Lock()
	worker.stats.LastAttemptAt = at
	worker.mu.Unlock()
}

func (worker *Worker) recordSuccess(summary Summary) {
	worker.ready.Store(true)
	worker.mu.Lock()
	worker.stats.Ready = true
	worker.stats.LastSuccessAt = time.Now()
	worker.stats.LastError = ""
	worker.stats.LastSummary = summary
	worker.stats.ConsecutiveFail = 0
	worker.mu.Unlock()
}

func (worker *Worker) recordFailure(err error, summary Summary) {
	worker.ready.Store(false)
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	worker.mu.Lock()
	worker.stats.Ready = false
	worker.stats.LastError = message
	worker.stats.LastSummary = summary
	worker.stats.ConsecutiveFail++
	worker.mu.Unlock()
}
