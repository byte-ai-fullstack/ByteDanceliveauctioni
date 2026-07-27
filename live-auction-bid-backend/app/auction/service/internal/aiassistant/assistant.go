package aiassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

type Config struct {
	Provider      string
	BaseURL       string
	Model         string
	APIKey        string
	Timeout       time.Duration
	MockOnFailure bool
}

type Assistant struct {
	cfg    Config
	client *http.Client
}

type BuyerConsultRequest struct {
	Query          string `json:"query"`
	RoomID         string `json:"roomId,omitempty"`
	LotID          string `json:"lotId,omitempty"`
	Budget         int64  `json:"budget,omitempty"`
	RiskPreference string `json:"riskPreference,omitempty"`
}

type BuyerConsultReply struct {
	Answer       string        `json:"answer"`
	Intent       string        `json:"intent"`
	Results      []BuyerResult `json:"results"`
	Sources      []Source      `json:"sources"`
	FallbackUsed bool          `json:"fallbackUsed"`
}

type BuyerSuggestionRequest struct {
	Limit int `json:"limit,omitempty"`
}

type BuyerSuggestion struct {
	Text   string `json:"text"`
	Reason string `json:"reason,omitempty"`
}

type BuyerSuggestionReply struct {
	Suggestions  []BuyerSuggestion `json:"suggestions"`
	FallbackUsed bool              `json:"fallbackUsed"`
}

type BuyerResult struct {
	Type         string    `json:"type"`
	Title        string    `json:"title"`
	RoomID       string    `json:"roomId"`
	LotID        string    `json:"lotId"`
	Status       string    `json:"status"`
	CurrentPrice *v1.Money `json:"currentPrice,omitempty"`
	Href         string    `json:"href"`
	Reason       string    `json:"reason"`
	ImageURL     string    `json:"imageUrl,omitempty"`
}

type Source struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	RoomID string `json:"roomId,omitempty"`
	LotID  string `json:"lotId,omitempty"`
}

type BuyerConsultContext struct {
	Candidates []LotCandidate   `json:"candidates"`
	Snapshot   *v1.RoomSnapshot `json:"snapshot,omitempty"`
}

type BuyerSuggestionContext struct {
	Candidates []LotCandidate `json:"candidates"`
}

type LotCandidate struct {
	Type         string    `json:"type"`
	Title        string    `json:"title"`
	RoomID       string    `json:"roomId"`
	LotID        string    `json:"lotId"`
	Status       string    `json:"status"`
	CurrentPrice *v1.Money `json:"currentPrice,omitempty"`
	Href         string    `json:"href"`
	Reason       string    `json:"reason"`
	ImageURL     string    `json:"imageUrl,omitempty"`
	Score        int       `json:"score,omitempty"`
}

func New(cfg Config) *Assistant {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = "mock"
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	return &Assistant{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

func NewFromEnv(getenv func(string) string) *Assistant {
	timeout := 8 * time.Second
	if raw := strings.TrimSpace(getenv("AUCTION_AI_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			timeout = parsed
		}
	}
	assistant := New(Config{
		Provider:      getenv("AUCTION_AI_PROVIDER"),
		BaseURL:       getenv("AUCTION_AI_BASE_URL"),
		Model:         getenv("AUCTION_AI_MODEL"),
		APIKey:        getenv("AUCTION_AI_API_KEY"),
		Timeout:       timeout,
		MockOnFailure: parseBool(getenv("AUCTION_AI_MOCK_ON_FAILURE"), true),
	})
	slog.Info("ai assistant configured",
		"provider", assistant.cfg.Provider,
		"model", assistant.cfg.Model,
		"base_url_configured", assistant.cfg.BaseURL != "",
		"api_key_configured", assistant.cfg.APIKey != "",
		"timeout", assistant.cfg.Timeout.String(),
		"mock_on_failure", assistant.cfg.MockOnFailure,
	)
	return assistant
}

// Mode reports the bounded runtime behavior used by the assistant. A provider
// that is absent or not fully configured follows the deterministic mock path.
func (a *Assistant) Mode() string {
	if a != nil && a.configured() {
		return "external"
	}
	return "mock"
}

func (a *Assistant) ConsultBuyer(
	ctx context.Context,
	req BuyerConsultRequest,
	data BuyerConsultContext,
) (BuyerConsultReply, error) {
	fallback := fallbackBuyer(req, data)
	if a == nil || !a.configured() {
		return fallback, nil
	}
	var rawReply map[string]any
	system := "你是直播竞拍平台的买家找拍品助手。必须只基于提供的候选拍品和来源回答，" +
		"不编造场次、价格、链接，也不要输出出价建议。只输出 JSON。"
	err := a.chatJSON(ctx, system, map[string]any{
		"task":             "buyer_consult",
		"request":          req,
		"dynamic_context":  data,
		"static_knowledge": buyerKnowledge(),
		"schema":           "answer,intent,results,sources,fallbackUsed",
	}, &rawReply)
	if err != nil {
		a.logFallback("buyer_consult", err)
		return fallback, nil
	}
	reply := normalizeBuyerReply(buyerReplyFromMap(rawReply), fallback)
	return reply, nil
}

func (a *Assistant) SuggestBuyerPrompts(
	ctx context.Context,
	req BuyerSuggestionRequest,
	data BuyerSuggestionContext,
) (BuyerSuggestionReply, error) {
	fallback := fallbackBuyerSuggestions(req, data)
	if a == nil || !a.configured() {
		return fallback, nil
	}
	var rawReply map[string]any
	system := "你是直播竞拍平台的找拍推荐词助手。必须只基于提供的公开候选拍品生成短搜索词，" +
		"不能编造不存在的品牌、场次、价格、主播或拍品。每个推荐词 4 到 12 个汉字左右，适合作为搜索 chip。只输出 JSON。"
	err := a.chatJSONWithTemperature(ctx, system, map[string]any{
		"task":            "buyer_prompt_suggestions",
		"request":         req,
		"public_lot_pool": data,
		"schema":          "suggestions:[{text,reason}],fallbackUsed",
	}, &rawReply, 0.7)
	if err != nil {
		a.logFallback("buyer_prompt_suggestions", err)
		return fallback, nil
	}
	reply := normalizeBuyerSuggestionReply(buyerSuggestionReplyFromMap(rawReply), fallback, data.Candidates, req.Limit)
	return reply, nil
}

func (a *Assistant) configured() bool {
	if a == nil {
		return false
	}
	return a.cfg.Provider == "deepseek" && a.cfg.BaseURL != "" && a.cfg.Model != "" && a.cfg.APIKey != ""
}

func (a *Assistant) chatJSON(ctx context.Context, system string, payload any, out any) error {
	return a.chatJSONWithTemperature(ctx, system, payload, out, 0.2)
}

func (a *Assistant) chatJSONWithTemperature(ctx context.Context, system string, payload any, out any, temperature float64) error {
	userPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	reqBody := map[string]any{
		"model":           a.cfg.Model,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": string(userPayload) + "\n请返回严格 JSON，不要 Markdown。"},
		},
		"temperature": temperature,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.Timeout)
	defer cancel()

	endpoint := a.chatEndpoint()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ai provider http status %d", resp.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if len(envelope.Choices) == 0 || strings.TrimSpace(envelope.Choices[0].Message.Content) == "" {
		return errors.New("ai provider returned empty content")
	}
	content := extractJSONObject(envelope.Choices[0].Message.Content)
	if content == "" {
		return errors.New("ai provider returned non-json content")
	}
	return json.Unmarshal([]byte(content), out)
}

func (a *Assistant) chatEndpoint() string {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func (a *Assistant) logFallback(task string, err error) {
	if a == nil || err == nil {
		return
	}
	slog.Warn("ai assistant fallback",
		"task", task,
		"provider", a.cfg.Provider,
		"model", a.cfg.Model,
		"error_kind", classifyError(err),
	)
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "timeout") {
		return "timeout"
	}
	if strings.Contains(err.Error(), "json") {
		return "json"
	}
	if strings.Contains(err.Error(), "http status") {
		return "provider_http"
	}
	return "provider"
}

func extractJSONObject(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return ""
	}
	return content[start : end+1]
}

func fallbackBuyer(_ BuyerConsultRequest, data BuyerConsultContext) BuyerConsultReply {
	candidates := append([]LotCandidate(nil), data.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	results := make([]BuyerResult, 0, len(candidates))
	sources := make([]Source, 0, len(candidates)+1)
	for _, candidate := range candidates {
		results = append(results, BuyerResult{
			Type:         firstNonEmpty(candidate.Type, "lot"),
			Title:        candidate.Title,
			RoomID:       candidate.RoomID,
			LotID:        candidate.LotID,
			Status:       candidate.Status,
			CurrentPrice: cloneMoney(candidate.CurrentPrice),
			Href:         candidate.Href,
			Reason:       candidate.Reason,
			ImageURL:     candidate.ImageURL,
		})
		sources = append(sources, Source{Type: "lot", Title: candidate.Title, RoomID: candidate.RoomID, LotID: candidate.LotID})
	}
	answer := "我会优先帮你找正在竞拍或即将开拍的公开拍品。"
	if len(results) == 0 {
		answer = "暂时没有找到匹配的公开竞拍场次，可以换个关键词或稍后再看。"
	}
	return BuyerConsultReply{
		Answer:       answer,
		Intent:       "find_auction",
		Results:      results,
		Sources:      sources,
		FallbackUsed: true,
	}
}

func fallbackBuyerSuggestions(req BuyerSuggestionRequest, data BuyerSuggestionContext) BuyerSuggestionReply {
	limit := normalizeSuggestionLimit(req.Limit)
	items := make([]BuyerSuggestion, 0, limit+len(data.Candidates)*2)
	add := func(text, reason string) {
		text = cleanSuggestionText(text)
		if text == "" {
			return
		}
		for _, item := range items {
			if item.Text == text {
				return
			}
		}
		items = append(items, BuyerSuggestion{Text: text, Reason: reason})
	}
	for _, candidate := range data.Candidates {
		title := cleanSuggestionText(candidate.Title)
		if title != "" && len([]rune(title)) <= 12 {
			add(title, "来自公开拍品标题")
		}
		for _, keyword := range supportedTitleKeywords(candidate.Title + " " + candidate.Reason) {
			add(keyword, "来自公开拍品内容")
		}
		switch candidate.Status {
		case "LOT_STATUS_LIVE":
			add("正在竞拍的拍品", "来自公开拍品状态")
		case "LOT_STATUS_EXTENDED":
			add("加时中的拍品", "来自公开拍品状态")
		case "LOT_STATUS_QUEUED":
			add("即将开拍", "来自公开拍品状态")
		}
		if strings.Contains(candidate.Reason, "适合送礼") {
			add("适合送礼的收藏品", "来自候选命中原因")
		}
		if strings.Contains(candidate.Reason, "低价") || strings.Contains(candidate.Reason, "预算") {
			add("预算友好好物", "来自候选命中原因")
		}
	}
	for _, text := range defaultBuyerSuggestionTexts() {
		add(text, "系统兜底推荐")
	}
	shuffleBuyerSuggestions(items)
	if len(items) > limit {
		items = items[:limit]
	}
	return BuyerSuggestionReply{Suggestions: items, FallbackUsed: true}
}

func normalizeBuyerReply(reply BuyerConsultReply, fallback BuyerConsultReply) BuyerConsultReply {
	if strings.TrimSpace(reply.Answer) == "" {
		reply.Answer = fallback.Answer
	}
	if strings.TrimSpace(reply.Intent) == "" {
		reply.Intent = fallback.Intent
	}
	if len(reply.Results) == 0 {
		reply.Results = fallback.Results
		reply.Sources = fallback.Sources
		reply.FallbackUsed = true
		return reply
	} else {
		reply.Results = sanitizeBuyerResults(reply.Results, fallback.Results)
		if len(reply.Results) == 0 {
			fallback.Answer = firstNonEmpty(reply.Answer, fallback.Answer)
			return fallback
		}
	}
	if len(reply.Sources) == 0 {
		reply.Sources = fallback.Sources
	}
	reply.FallbackUsed = false
	return reply
}

func normalizeBuyerSuggestionReply(reply BuyerSuggestionReply, fallback BuyerSuggestionReply, candidates []LotCandidate, limit int) BuyerSuggestionReply {
	limit = normalizeSuggestionLimit(limit)
	clean := sanitizeBuyerSuggestions(reply.Suggestions, candidates, limit)
	if len(clean) == 0 {
		return fallback
	}
	if len(clean) < limit {
		seen := make(map[string]bool, len(clean))
		for _, item := range clean {
			seen[item.Text] = true
		}
		for _, item := range fallback.Suggestions {
			if seen[item.Text] {
				continue
			}
			clean = append(clean, item)
			seen[item.Text] = true
			if len(clean) >= limit {
				break
			}
		}
	}
	return BuyerSuggestionReply{Suggestions: clean, FallbackUsed: false}
}

func buyerReplyFromMap(raw map[string]any) BuyerConsultReply {
	return BuyerConsultReply{
		Answer:  textFromMap(raw, "answer"),
		Intent:  textFromMap(raw, "intent"),
		Results: buyerResultsFromAny(valueFromMap(raw, "results", "lots", "matches")),
		Sources: sourcesFromAny(valueFromMap(raw, "sources", "source")),
	}
}

func buyerSuggestionReplyFromMap(raw map[string]any) BuyerSuggestionReply {
	return BuyerSuggestionReply{
		Suggestions: suggestionsFromAny(valueFromMap(raw, "suggestions", "prompts", "chips", "queries")),
	}
}

func suggestionsFromAny(value any) []BuyerSuggestion {
	items := sliceFromAny(value)
	out := make([]BuyerSuggestion, 0, len(items))
	for _, item := range items {
		if text := textFromAny(item); text != "" {
			out = append(out, BuyerSuggestion{Text: text})
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := textFromMap(m, "text", "query", "prompt", "label", "title")
		if text == "" {
			continue
		}
		out = append(out, BuyerSuggestion{Text: text, Reason: textFromMap(m, "reason", "source")})
	}
	return out
}

func buyerResultsFromAny(value any) []BuyerResult {
	items := sliceFromAny(value)
	out := make([]BuyerResult, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title := textFromMap(m, "title", "name")
		roomID := textFromMap(m, "roomId", "room_id")
		lotID := textFromMap(m, "lotId", "lot_id")
		if title == "" && lotID == "" {
			continue
		}
		out = append(out, BuyerResult{
			Type:         firstNonEmpty(textFromMap(m, "type"), "lot"),
			Title:        title,
			RoomID:       roomID,
			LotID:        lotID,
			Status:       textFromMap(m, "status"),
			CurrentPrice: moneyFromAny(valueFromMap(m, "currentPrice", "current_price", "price")),
			Href:         textFromMap(m, "href", "url", "link"),
			Reason:       textFromMap(m, "reason", "description"),
			ImageURL:     textFromMap(m, "imageUrl", "image_url", "image", "coverUrl", "cover_url"),
		})
	}
	return out
}

func sanitizeBuyerResults(results []BuyerResult, fallback []BuyerResult) []BuyerResult {
	if len(results) == 0 || len(fallback) == 0 {
		return nil
	}
	byLotID := make(map[string]BuyerResult, len(fallback))
	for _, item := range fallback {
		if strings.TrimSpace(item.LotID) != "" {
			byLotID[item.LotID] = item
		}
	}
	out := make([]BuyerResult, 0, len(results))
	seen := map[string]bool{}
	for _, result := range results {
		lotID := strings.TrimSpace(result.LotID)
		if lotID == "" || seen[lotID] {
			continue
		}
		fallbackItem, ok := byLotID[lotID]
		if !ok {
			continue
		}
		seen[lotID] = true
		clean := fallbackItem
		if strings.TrimSpace(result.Reason) != "" {
			clean.Reason = result.Reason
		}
		out = append(out, clean)
	}
	return out
}

func sanitizeBuyerSuggestions(suggestions []BuyerSuggestion, candidates []LotCandidate, limit int) []BuyerSuggestion {
	limit = normalizeSuggestionLimit(limit)
	out := make([]BuyerSuggestion, 0, limit)
	seen := map[string]bool{}
	for _, suggestion := range suggestions {
		text := cleanSuggestionText(suggestion.Text)
		if text == "" || seen[text] || !suggestionSupportedByCandidates(text, candidates) {
			continue
		}
		seen[text] = true
		out = append(out, BuyerSuggestion{Text: text, Reason: strings.TrimSpace(suggestion.Reason)})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func suggestionSupportedByCandidates(text string, candidates []LotCandidate) bool {
	for _, allowed := range defaultBuyerSuggestionTexts() {
		if text == allowed {
			return true
		}
	}
	if text == "加时中的拍品" || text == "预算友好好物" {
		return true
	}
	for _, candidate := range candidates {
		haystack := candidate.Title + " " + candidate.Reason
		if strings.Contains(haystack, text) || strings.Contains(text, candidate.Title) {
			return true
		}
		for _, keyword := range supportedTitleKeywords(haystack) {
			if strings.Contains(text, keyword) {
				return true
			}
		}
	}
	return false
}

func supportedTitleKeywords(text string) []string {
	candidates := []string{
		"翡翠手镯", "翡翠", "手镯", "吊坠", "项链", "玛瑙", "和田玉", "玉石", "珠宝",
		"首饰", "收藏",
	}
	out := make([]string, 0, len(candidates))
	for _, keyword := range candidates {
		if strings.Contains(text, keyword) {
			out = append(out, keyword)
		}
	}
	return out
}

func cleanSuggestionText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, " \t\r\n\"'“”‘’.,，。:：;；!?！？#")
	text = strings.Join(strings.Fields(text), "")
	runes := []rune(text)
	if len(runes) < 2 || len(runes) > 14 {
		return ""
	}
	if isASCIIIntegerText(text) {
		return ""
	}
	for _, bad := range []string{"http://", "https://", "¥", "￥", "元成交", "出价"} {
		if strings.Contains(text, bad) {
			return ""
		}
	}
	return text
}

func isASCIIIntegerText(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeSuggestionLimit(limit int) int {
	if limit <= 0 {
		return 6
	}
	if limit > 8 {
		return 8
	}
	return limit
}

func defaultBuyerSuggestionTexts() []string {
	return []string{"翡翠手镯", "适合送礼的收藏品", "正在竞拍的拍品", "预算500以内", "低价开拍", "即将开拍"}
}

func shuffleBuyerSuggestions(items []BuyerSuggestion) {
	if len(items) <= 1 {
		return
	}
	rng := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})
}

func sourcesFromAny(value any) []Source {
	items := sliceFromAny(value)
	if len(items) == 0 {
		if title := textFromAny(value); title != "" {
			return []Source{{Type: "context", Title: title}}
		}
		return nil
	}
	out := make([]Source, 0, len(items))
	for _, item := range items {
		if title := textFromAny(item); title != "" {
			out = append(out, Source{Type: "context", Title: title})
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title := textFromMap(m, "title", "name")
		if title == "" {
			continue
		}
		out = append(out, Source{
			Type:   firstNonEmpty(textFromMap(m, "type"), "context"),
			Title:  title,
			RoomID: textFromMap(m, "roomId", "room_id"),
			LotID:  textFromMap(m, "lotId", "lot_id"),
		})
	}
	return out
}

func valueFromMap(m map[string]any, keys ...string) any {
	if m == nil {
		return nil
	}
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func textFromMap(m map[string]any, keys ...string) string {
	return textFromAny(valueFromMap(m, keys...))
}

func textFromAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64, bool:
		return strings.TrimSpace(fmt.Sprint(v))
	default:
		return ""
	}
}

func sliceFromAny(value any) []any {
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		return v
	case []string:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	default:
		return 0
	}
}

func moneyFromAny(value any) *v1.Money {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		amount := int64FromAny(valueFromMap(v, "amount", "value"))
		if amount <= 0 {
			return nil
		}
		return &v1.Money{Amount: amount, Currency: firstNonEmpty(textFromMap(v, "currency"), "CNY")}
	case float64, int, int64:
		amount := int64FromAny(v)
		if amount <= 0 {
			return nil
		}
		return &v1.Money{Amount: amount, Currency: "CNY"}
	default:
		return nil
	}
}

func buyerKnowledge() []string {
	return []string{
		"公开可见拍品只包含即将开始、直播中、延时中的拍品。",
		"找拍品回答必须引用候选拍品，不提供下一口价或建议上限。",
		"预算只用于筛选和排序，不代表系统给出的出价建议。",
	}
}

func cloneMoney(m *v1.Money) *v1.Money {
	if m == nil {
		return nil
	}
	return &v1.Money{Amount: m.GetAmount(), Currency: m.GetCurrency()}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
