package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestSetSearchMonitoringRequirements(t *testing.T) {
	t.Cleanup(func() { SetSearchMonitoringRequirements(false, false) })

	SetSearchMonitoringRequirements(true, false)
	wants := map[string]string{
		"index-es":          "1",
		"elasticsearch":     "1",
		"index-pgvector":    "0",
		"pgvector":          "0",
		"search-reconciler": "0",
	}
	response := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for component, want := range wants {
		line := `auction_search_component_required{component="` + component + `"} ` + want
		if !strings.Contains(response.Body.String(), line) {
			t.Fatalf("metric line %q is missing", line)
		}
	}

	SetSearchMonitoringRequirements(true, true)
	response = httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `auction_search_component_required{component="search-reconciler"} 1`) {
		t.Fatal("search reconciler requirement metric is missing")
	}
}

func TestSetAIAssistantModePublishesOneBoundedActiveMode(t *testing.T) {
	t.Cleanup(func() { SetAIAssistantMode("mock") })
	SetAIAssistantMode("external")

	response := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range []string{
		`auction_ai_assistant_info{mode="external"} 1`,
		`auction_ai_assistant_info{mode="mock"} 0`,
	} {
		if !strings.Contains(response.Body.String(), line) {
			t.Fatalf("metric line %q is missing", line)
		}
	}

	SetAIAssistantMode("unsupported-provider")
	response = httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `auction_ai_assistant_info{mode="mock"} 1`) {
		t.Fatal("unsupported mode must be reported as mock")
	}
}

func TestRecordEmbeddingUsage(t *testing.T) {
	model := "metrics-test-embedding-model"
	RecordEmbeddingUsage(model, "provider", 250, 4)

	response := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range []string{
		`auction_embedding_tokens_estimate_total{model="metrics-test-embedding-model",source="provider"} 250`,
		`auction_embedding_cost_estimate_total{model="metrics-test-embedding-model"} 0.001`,
	} {
		if !strings.Contains(response.Body.String(), line) {
			t.Fatalf("metric line %q is missing", line)
		}
	}
}

func TestMetricsExposeGoBuildInfoCompatibilityFamily(t *testing.T) {
	response := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "go_build_info{") {
		t.Fatal("go_build_info compatibility metric is missing")
	}
}

func TestSetOrderVisibilityLag(t *testing.T) {
	t.Cleanup(func() { SetOrderVisibilityLag(0) })
	SetOrderVisibilityLag(12_345)

	response := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "auction_order_visibility_lag_ms 12345") {
		t.Fatal("order visibility lag metric is missing")
	}
	SetOrderVisibilityLag(-1)
	response = httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "auction_order_visibility_lag_ms 0") {
		t.Fatal("order visibility lag metric did not clamp a negative value")
	}
}
