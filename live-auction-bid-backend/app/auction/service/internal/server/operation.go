package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"

	"github.com/go-kratos/kratos/v2/middleware/recovery"
	httptransport "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type HealthChecker interface {
	Ping(ctx context.Context) error
}

type AdmissionHealthChecker interface {
	AdmissionPing(ctx context.Context) error
}

type RuntimeProjectionMetricsProvider interface {
	MetricsSnapshot(ctx context.Context) map[string]any
}

type Readiness struct {
	Store             HealthChecker
	AdmissionGate     HealthChecker
	CommandService    HealthChecker
	Gateway           HealthChecker
	ProjectionMetrics RuntimeProjectionMetricsProvider
}

func (r Readiness) Ping(ctx context.Context) error {
	checks := []struct {
		name    string
		checker HealthChecker
	}{
		{name: "store", checker: r.Store},
	}
	if r.CommandService != nil {
		checks = append(checks, struct {
			name    string
			checker HealthChecker
		}{name: "auction_command_service", checker: r.CommandService})
	}
	if r.Gateway != nil {
		checks = append(checks, struct {
			name    string
			checker HealthChecker
		}{name: "gateway", checker: r.Gateway})
	}
	for _, check := range checks {
		if check.checker == nil {
			return fmt.Errorf("%s health checker is missing", check.name)
		}
		if err := check.checker.Ping(ctx); err != nil {
			return fmt.Errorf("%s not ready: %w", check.name, err)
		}
	}
	return nil
}

func (r Readiness) AdmissionPing(ctx context.Context) error {
	if r.AdmissionGate == nil {
		return fmt.Errorf("runtime admission gate health checker is missing")
	}
	if err := r.AdmissionGate.Ping(ctx); err != nil {
		return fmt.Errorf("runtime admission gate closed: %w", err)
	}
	return nil
}

func (r Readiness) MetricsSnapshot(ctx context.Context) map[string]any {
	if r.ProjectionMetrics == nil {
		return map[string]any{"status": "runtime projection metrics unavailable"}
	}
	return r.ProjectionMetrics.MetricsSnapshot(ctx)
}

func NewOperationsHTTPServer(addr, serviceName string, health HealthChecker) *httptransport.Server {
	srv := httptransport.NewServer(
		httptransport.Address(addr),
		httptransport.Middleware(recovery.Recovery()),
		httptransport.Filter(requestctx.HTTPMiddleware),
		httptransport.ErrorEncoder(resultEnvelopeErrorEncoder),
	)
	registerOperationHTTP(srv, health, serviceName)
	return srv
}

func registerOperationHTTP(srv *httptransport.Server, health HealthChecker, serviceName string) {
	if serviceName == "" {
		serviceName = "auction-backend"
	}
	srv.Handle("/metrics", promhttp.Handler())
	live := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
	srv.HandleFunc("/healthz", live)
	srv.HandleFunc("/livez", live)
	srv.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if health == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "missing health checker"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := health.Ping(ctx); err != nil {
			slog.Error("auction readiness check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready", "error": "dependency not ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	srv.HandleFunc("/admissionz", func(w http.ResponseWriter, r *http.Request) {
		checker, ok := health.(AdmissionHealthChecker)
		if !ok || checker == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "admission unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := checker.AdmissionPing(ctx); err != nil {
			slog.Warn("auction runtime admission gate closed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "closed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "open"})
	})
	srv.HandleFunc("/metrics/runtime-projection", func(w http.ResponseWriter, r *http.Request) {
		provider, ok := health.(RuntimeProjectionMetricsProvider)
		if !ok || provider == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"status": "runtime projection metrics unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, provider.MetricsSnapshot(ctx))
	})
	srv.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": serviceName, "transport": "kratos-http", "status": "ok"})
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
