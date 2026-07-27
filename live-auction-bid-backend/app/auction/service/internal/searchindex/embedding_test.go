package searchindex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbeddingClientEmbedsDashScopeCompatibleResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compatible-mode/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %s", got)
		}
		var req struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "text-embedding-v4" || req.Dimensions != 3 || len(req.Input) != 2 {
			t.Fatalf("unexpected request: %+v", req)
		}
		_, _ = w.Write([]byte(`{
			"data": [
				{"index": 1, "embedding": [0.4, 0.5, 0.6]},
				{"index": 0, "embedding": [0.1, 0.2, 0.3]}
			]
		}`))
	}))
	defer server.Close()

	client := NewEmbeddingClient(EmbeddingConfig{
		Provider:   "dashscope",
		BaseURL:    server.URL + "/compatible-mode/v1",
		Model:      "text-embedding-v4",
		APIKey:     "test-key",
		Dimensions: 3,
		Timeout:    time.Second,
	})
	if client.Provider() != "dashscope" || client.ModelVersion() != "text-embedding-v4" {
		t.Fatalf("embedding identity provider=%q version=%q", client.Provider(), client.ModelVersion())
	}

	embeddings, err := client.Embed(context.Background(), []string{"翡翠手镯", "送礼收藏"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(embeddings) != 2 || embeddings[0][0] != 0.1 || embeddings[1][0] != 0.4 {
		t.Fatalf("unexpected embeddings: %#v", embeddings)
	}
}

func TestEmbeddingClientRequiresConfiguration(t *testing.T) {
	client := NewEmbeddingClient(EmbeddingConfig{Provider: "dashscope"})
	if client.Configured() {
		t.Fatal("client should not be configured without api key")
	}
}

func TestEmbeddingCostConfigurationAndTokenEstimate(t *testing.T) {
	values := map[string]string{
		"AUCTION_EMBEDDING_API_KEY":                 "key",
		"AUCTION_EMBEDDING_COST_PER_MILLION_TOKENS": "4.5",
	}
	client := NewEmbeddingClientFromEnv(func(key string) string { return values[key] })
	if client.cfg.CostPerMillionTokens != 4.5 {
		t.Fatalf("cost per million=%v", client.cfg.CostPerMillionTokens)
	}
	if got := estimateEmbeddingTokens([]string{"翡翠手镯", " abc "}); got != 7 {
		t.Fatalf("estimated tokens=%d want=7", got)
	}
	for _, invalid := range []string{"-1", "NaN", "+Inf", "invalid"} {
		if got := parseNonNegativeFloat(invalid, 2); got != 2 {
			t.Fatalf("invalid %q parsed as %v", invalid, got)
		}
	}
	if got := NewEmbeddingClient(EmbeddingConfig{CostPerMillionTokens: -1}).cfg.CostPerMillionTokens; got != 0 {
		t.Fatalf("negative direct cost=%v", got)
	}
}
