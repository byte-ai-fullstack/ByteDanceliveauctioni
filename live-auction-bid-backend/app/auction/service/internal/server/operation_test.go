package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessIncludesGatewayDrainState(t *testing.T) {
	readiness := Readiness{
		Store:   okHealthChecker{},
		Gateway: failingHealthChecker{err: errors.New("draining")},
	}
	if err := readiness.Ping(context.Background()); err == nil {
		t.Fatal("gateway drain state must make readiness fail")
	}
}

func TestReadinessIncludesAuctionCommandService(t *testing.T) {
	readiness := Readiness{
		Store:          okHealthChecker{},
		CommandService: failingHealthChecker{err: errors.New("connection unavailable")},
	}
	if err := readiness.Ping(context.Background()); err == nil {
		t.Fatal("auction command transport failure must make gateway readiness fail")
	}
}

func TestReadinessKeepsRuntimeAdmissionGateOutOfPlatformReadiness(t *testing.T) {
	readiness := Readiness{
		Store:         okHealthChecker{},
		AdmissionGate: failingHealthChecker{err: errors.New("projection lag exceeded")},
	}
	if err := readiness.Ping(context.Background()); err != nil {
		t.Fatalf("closed runtime admission gate must not remove the pod from service endpoints: %v", err)
	}
	if err := readiness.AdmissionPing(context.Background()); err == nil {
		t.Fatal("closed runtime admission gate must fail the independent admission check")
	}
}

func TestReadinessAdmissionPingRequiresGate(t *testing.T) {
	readiness := Readiness{Store: okHealthChecker{}}
	if err := readiness.AdmissionPing(context.Background()); err == nil {
		t.Fatal("missing runtime admission gate must fail closed")
	}
}

func TestReadinessUsesPlatformServiceDiscovery(t *testing.T) {
	readiness := Readiness{Store: okHealthChecker{}}
	if err := readiness.Ping(context.Background()); err != nil {
		t.Fatalf("platform service discovery added a readiness dependency: %v", err)
	}
}

func TestOperationsHTTPServerExposesServiceIdentityAndReadiness(t *testing.T) {
	srv := httptest.NewServer(NewOperationsHTTPServer("", "auction-service", okHealthChecker{}))
	defer srv.Close()
	for path, wantService := range map[string]string{"/": "auction-service", "/readyz": ""} {
		response, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			_ = response.Body.Close()
			t.Fatalf("decode %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d payload=%v", path, response.StatusCode, payload)
		}
		if wantService != "" && payload["service"] != wantService {
			t.Fatalf("GET %s service=%v", path, payload["service"])
		}
	}
}

func TestOperationsLivenessDoesNotDependOnReadiness(t *testing.T) {
	srv := httptest.NewServer(NewOperationsHTTPServer("", "auction-service", failingHealthChecker{err: errors.New("closed")}))
	defer srv.Close()
	for _, path := range []string{"/healthz", "/livez"} {
		response, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want liveness OK", path, response.StatusCode)
		}
	}
}

func TestOperationsAdmissionEndpointIsIndependentOfReadiness(t *testing.T) {
	readiness := Readiness{
		Store:         okHealthChecker{},
		AdmissionGate: failingHealthChecker{err: errors.New("projection lag exceeded")},
	}
	srv := httptest.NewServer(NewOperationsHTTPServer("", "auction-service", readiness))
	defer srv.Close()

	for path, wantStatus := range map[string]int{
		"/readyz":     http.StatusOK,
		"/admissionz": http.StatusServiceUnavailable,
	} {
		response, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, wantStatus)
		}
	}
}

func TestOperationsAdmissionEndpointReportsOpenGate(t *testing.T) {
	readiness := Readiness{Store: okHealthChecker{}, AdmissionGate: okHealthChecker{}}
	srv := httptest.NewServer(NewOperationsHTTPServer("", "auction-service", readiness))
	defer srv.Close()

	response, err := http.Get(srv.URL + "/admissionz")
	if err != nil {
		t.Fatalf("GET /admissionz: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /admissionz status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /admissionz: %v", err)
	}
	if payload["status"] != "open" {
		t.Fatalf("GET /admissionz status payload = %q, want open", payload["status"])
	}
}

type okHealthChecker struct{}

func (okHealthChecker) Ping(context.Context) error {
	return nil
}

type failingHealthChecker struct {
	err error
}

func (checker failingHealthChecker) Ping(context.Context) error {
	return checker.err
}
