package searchindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"live-auction-bid/backend/app/auction/service/internal/observability"
)

const defaultDashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

type EmbeddingConfig struct {
	Provider             string
	BaseURL              string
	Model                string
	ModelVersion         string
	APIKey               string
	Dimensions           int
	Timeout              time.Duration
	BatchSize            int
	CostPerMillionTokens float64
}

type EmbeddingClient struct {
	cfg    EmbeddingConfig
	client *http.Client
}

func NewEmbeddingClient(cfg EmbeddingConfig) *EmbeddingClient {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = "dashscope"
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultDashScopeBaseURL
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		cfg.Model = DefaultEmbeddingModel
	}
	cfg.ModelVersion = strings.TrimSpace(cfg.ModelVersion)
	if cfg.ModelVersion == "" {
		cfg.ModelVersion = cfg.Model
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Dimensions = NormalizeDimensions(cfg.Dimensions)
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	if cfg.BatchSize <= 0 || cfg.BatchSize > DefaultEmbeddingBatchSize {
		cfg.BatchSize = DefaultEmbeddingBatchSize
	}
	if cfg.CostPerMillionTokens < 0 || math.IsNaN(cfg.CostPerMillionTokens) || math.IsInf(cfg.CostPerMillionTokens, 0) {
		cfg.CostPerMillionTokens = 0
	}
	return &EmbeddingClient{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

func NewEmbeddingClientFromEnv(getenv func(string) string) *EmbeddingClient {
	timeout := 8 * time.Second
	if raw := strings.TrimSpace(getenv("AUCTION_EMBEDDING_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			timeout = parsed
		}
	}
	return NewEmbeddingClient(EmbeddingConfig{
		Provider:             getenv("AUCTION_EMBEDDING_PROVIDER"),
		BaseURL:              getenv("AUCTION_EMBEDDING_BASE_URL"),
		Model:                getenv("AUCTION_EMBEDDING_MODEL"),
		ModelVersion:         getenv("AUCTION_EMBEDDING_MODEL_VERSION"),
		APIKey:               getenv("AUCTION_EMBEDDING_API_KEY"),
		Dimensions:           parsePositiveInt(getenv("AUCTION_EMBEDDING_DIMENSIONS"), DefaultEmbeddingDimensions),
		Timeout:              timeout,
		BatchSize:            parsePositiveInt(getenv("AUCTION_EMBEDDING_BATCH_SIZE"), DefaultEmbeddingBatchSize),
		CostPerMillionTokens: parseNonNegativeFloat(getenv("AUCTION_EMBEDDING_COST_PER_MILLION_TOKENS"), 0),
	})
}

func (c *EmbeddingClient) Configured() bool {
	if c == nil {
		return false
	}
	return c.cfg.Provider == "dashscope" && c.cfg.APIKey != "" && c.cfg.Model != "" && c.cfg.BaseURL != ""
}

func (c *EmbeddingClient) Model() string {
	if c == nil {
		return DefaultEmbeddingModel
	}
	return c.cfg.Model
}

func (c *EmbeddingClient) Provider() string {
	if c == nil {
		return "dashscope"
	}
	return c.cfg.Provider
}

func (c *EmbeddingClient) ModelVersion() string {
	if c == nil || strings.TrimSpace(c.cfg.ModelVersion) == "" {
		return c.Model()
	}
	return c.cfg.ModelVersion
}

func (c *EmbeddingClient) Dimensions() int {
	if c == nil {
		return DefaultEmbeddingDimensions
	}
	return c.cfg.Dimensions
}

func (c *EmbeddingClient) BatchSize() int {
	if c == nil || c.cfg.BatchSize <= 0 {
		return DefaultEmbeddingBatchSize
	}
	return c.cfg.BatchSize
}

func (c *EmbeddingClient) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if !c.Configured() {
		return nil, errors.New("embedding client is not configured")
	}
	cleaned := make([]string, 0, len(texts))
	for _, text := range texts {
		if value := strings.TrimSpace(text); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	if len(cleaned) > c.BatchSize() {
		return nil, fmt.Errorf("embedding batch too large: %d > %d", len(cleaned), c.BatchSize())
	}
	body, err := json.Marshal(map[string]any{
		"model":           c.cfg.Model,
		"input":           cleaned,
		"dimensions":      c.cfg.Dimensions,
		"encoding_format": "float",
	})
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding provider http status %d", resp.StatusCode)
	}
	var envelope struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Data) != len(cleaned) {
		return nil, fmt.Errorf("embedding provider returned %d vectors for %d inputs", len(envelope.Data), len(cleaned))
	}
	out := make([][]float64, len(cleaned))
	for _, item := range envelope.Data {
		if item.Index < 0 || item.Index >= len(cleaned) {
			return nil, fmt.Errorf("embedding provider returned invalid index %d", item.Index)
		}
		if len(item.Embedding) != c.cfg.Dimensions {
			return nil, fmt.Errorf("embedding dimensions mismatch: got %d want %d", len(item.Embedding), c.cfg.Dimensions)
		}
		out[item.Index] = item.Embedding
	}
	for i := range out {
		if len(out[i]) == 0 {
			return nil, fmt.Errorf("embedding provider omitted index %d", i)
		}
	}
	tokens, source := envelope.Usage.PromptTokens, "provider"
	if tokens <= 0 {
		tokens = envelope.Usage.TotalTokens
	}
	if tokens <= 0 {
		tokens, source = estimateEmbeddingTokens(cleaned), "estimated"
	}
	observability.RecordEmbeddingUsage(c.Model(), source, tokens, c.cfg.CostPerMillionTokens)
	return out, nil
}

func (c *EmbeddingClient) endpoint() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/embeddings") {
		return base
	}
	return base + "/embeddings"
}

func parsePositiveInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseNonNegativeFloat(raw string, fallback float64) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
}

func estimateEmbeddingTokens(texts []string) int {
	total := 0
	for _, text := range texts {
		if count := utf8.RuneCountInString(strings.TrimSpace(text)); count > 0 {
			total += count
		}
	}
	return total
}
