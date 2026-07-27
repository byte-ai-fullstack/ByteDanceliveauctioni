package searchindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const defaultElasticsearchMaxResponseBytes = 1 << 20

var (
	ErrElasticsearchSchemaUnavailable = errors.New("elasticsearch search schema is unavailable")
	ErrInvalidElasticsearchDocument   = errors.New("invalid Elasticsearch lot document")
	ErrElasticsearchVersionConflict   = errors.New("elasticsearch lot version identity conflict")
)

type ElasticsearchConfig struct {
	BaseURL          string
	Username         string
	Password         string
	WriteAlias       string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
}

type ElasticsearchIndex struct {
	baseURL          *url.URL
	username         string
	password         string
	writeAlias       string
	client           *http.Client
	maxResponseBytes int64
}

type ElasticsearchApplyResult struct {
	Applied   bool
	Duplicate bool
	Stale     bool
}

type elasticsearchDocument struct {
	LotID              string   `json:"lot_id"`
	RoomID             string   `json:"room_id"`
	MainAccountID      string   `json:"main_account_id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Category           string   `json:"category"`
	Tags               []string `json:"tags"`
	ImageURL           string   `json:"image_url"`
	Status             string   `json:"status"`
	StartPriceFen      int64    `json:"start_price_fen"`
	CurrentPriceFen    int64    `json:"current_price_fen"`
	Currency           string   `json:"currency"`
	StartsAtUnixMs     int64    `json:"starts_at"`
	EndsAtUnixMs       int64    `json:"ends_at"`
	Href               string   `json:"href"`
	PublicVisible      bool     `json:"public_visible"`
	LotVersion         int64    `json:"lot_version"`
	LastEventID        string   `json:"last_event_id"`
	ContentHash        string   `json:"content_hash"`
	IndexedAtUnixMilli int64    `json:"indexed_at"`
}

func NewElasticsearchIndex(ctx context.Context, config ElasticsearchConfig) (*ElasticsearchIndex, error) {
	baseURL, err := parseElasticsearchBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	config.WriteAlias = strings.TrimSpace(config.WriteAlias)
	if !validElasticsearchName(config.WriteAlias) {
		return nil, errors.New("elasticsearch write alias is invalid")
	}
	config.Username = strings.TrimSpace(config.Username)
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("elasticsearch username and password must be configured together")
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 5 * time.Second
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultElasticsearchMaxResponseBytes
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: config.RequestTimeout}
	}
	index := &ElasticsearchIndex{
		baseURL: baseURL, username: config.Username, password: config.Password,
		writeAlias: config.WriteAlias, client: client, maxResponseBytes: config.MaxResponseBytes,
	}
	if err := index.verifySchema(ctx); err != nil {
		return nil, err
	}
	return index, nil
}

func parseElasticsearchBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("elasticsearch base URL must be an http(s) origin without credentials, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func (index *ElasticsearchIndex) verifySchema(ctx context.Context) error {
	if index == nil || index.client == nil || index.baseURL == nil {
		return ErrElasticsearchSchemaUnavailable
	}
	status, payload, err := index.request(ctx, http.MethodGet, "/_alias/"+url.PathEscape(index.writeAlias), nil)
	if err != nil {
		return fmt.Errorf("%w: verify write alias: %v", ErrElasticsearchSchemaUnavailable, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("%w: write alias %q returned HTTP %d; run deploy/elasticsearch/init-index.sh", ErrElasticsearchSchemaUnavailable, index.writeAlias, status)
	}
	writeIndex, err := decodeElasticsearchWriteIndex(payload, index.writeAlias)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrElasticsearchSchemaUnavailable, err)
	}
	status, payload, err = index.request(ctx, http.MethodGet, "/"+url.PathEscape(writeIndex)+"/_mapping", nil)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("%w: read alias mapping: HTTP %d: %v", ErrElasticsearchSchemaUnavailable, status, err)
	}
	if err := validateElasticsearchMapping(payload); err != nil {
		return fmt.Errorf("%w: %v", ErrElasticsearchSchemaUnavailable, err)
	}
	return nil
}

// EnsureVersionedIndex creates a deployment-owned rebuild target or validates
// an existing target when resume is explicitly requested.
func (index *ElasticsearchIndex) EnsureVersionedIndex(ctx context.Context, indexName string, definition []byte, resume bool) (bool, error) {
	if index == nil || index.client == nil || index.baseURL == nil {
		return false, errors.New("elasticsearch index is not initialized")
	}
	indexName = strings.TrimSpace(indexName)
	if !validElasticsearchName(indexName) || indexName == index.writeAlias {
		return false, errors.New("elasticsearch rebuild target must be a valid versioned index name")
	}
	if len(definition) == 0 || int64(len(definition)) > index.maxResponseBytes || !json.Valid(definition) {
		return false, errors.New("elasticsearch index definition is empty, invalid, or too large")
	}
	status, payload, err := index.request(ctx, http.MethodGet, "/"+url.PathEscape(indexName), nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		if !resume {
			return false, fmt.Errorf("elasticsearch rebuild target %s already exists; use resume explicitly", indexName)
		}
		if err := index.validateIndexMapping(ctx, indexName); err != nil {
			return false, err
		}
		return false, nil
	case http.StatusNotFound:
	case http.StatusBadRequest:
		return false, elasticsearchStatusError("inspect rebuild target", status, payload)
	default:
		return false, elasticsearchStatusError("inspect rebuild target", status, payload)
	}
	status, payload, err = index.request(ctx, http.MethodPut, "/"+url.PathEscape(indexName), definition)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, elasticsearchStatusError("create rebuild target", status, payload)
	}
	if err := index.validateIndexMapping(ctx, indexName); err != nil {
		return false, err
	}
	return true, nil
}

func (index *ElasticsearchIndex) validateIndexMapping(ctx context.Context, indexName string) error {
	status, payload, err := index.request(ctx, http.MethodGet, "/"+url.PathEscape(indexName)+"/_mapping", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return elasticsearchStatusError("validate target mapping", status, payload)
	}
	if err := validateElasticsearchMapping(payload); err != nil {
		return fmt.Errorf("%w: target %s: %v", ErrElasticsearchSchemaUnavailable, indexName, err)
	}
	return nil
}

func (index *ElasticsearchIndex) RefreshIndex(ctx context.Context, indexName string) error {
	if index == nil || !validElasticsearchName(strings.TrimSpace(indexName)) {
		return errors.New("elasticsearch index and target name are required")
	}
	status, payload, err := index.request(ctx, http.MethodPost, "/"+url.PathEscape(strings.TrimSpace(indexName))+"/_refresh", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return elasticsearchStatusError("refresh rebuild target", status, payload)
	}
	return nil
}

func (index *ElasticsearchIndex) CountDocuments(ctx context.Context, indexName string) (int64, error) {
	if index == nil || !validElasticsearchName(strings.TrimSpace(indexName)) {
		return 0, errors.New("elasticsearch index and target name are required")
	}
	status, payload, err := index.request(ctx, http.MethodGet, "/"+url.PathEscape(strings.TrimSpace(indexName))+"/_count", nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, elasticsearchStatusError("count rebuild target", status, payload)
	}
	var response struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.Count < 0 {
		return 0, errors.New("elasticsearch count response is invalid")
	}
	return response.Count, nil
}

// SwitchWriteAlias atomically removes the read/write alias from every old
// member and makes target the sole write index. It returns the previous writer.
func (index *ElasticsearchIndex) SwitchWriteAlias(ctx context.Context, target string) (string, error) {
	if index == nil || index.client == nil {
		return "", errors.New("elasticsearch index is not initialized")
	}
	target = strings.TrimSpace(target)
	if !validElasticsearchName(target) || target == index.writeAlias {
		return "", errors.New("elasticsearch alias target is invalid")
	}
	if err := index.validateIndexMapping(ctx, target); err != nil {
		return "", err
	}
	status, payload, err := index.request(ctx, http.MethodGet, "/_alias/"+url.PathEscape(index.writeAlias), nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", elasticsearchStatusError("read current alias", status, payload)
	}
	previous, members, err := decodeElasticsearchAliasMembers(payload, index.writeAlias)
	if err != nil {
		return "", err
	}
	if previous == target && len(members) == 1 {
		return previous, nil
	}
	actions := make([]any, 0, len(members)+1)
	for _, member := range members {
		actions = append(actions, map[string]any{"remove": map[string]any{"index": member, "alias": index.writeAlias}})
	}
	actions = append(actions, map[string]any{"add": map[string]any{
		"index": target, "alias": index.writeAlias, "is_write_index": true,
	}})
	body, err := json.Marshal(map[string]any{"actions": actions})
	if err != nil {
		return "", err
	}
	status, payload, err = index.request(ctx, http.MethodPost, "/_aliases", body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", elasticsearchStatusError("switch write alias", status, payload)
	}
	status, payload, err = index.request(ctx, http.MethodGet, "/_alias/"+url.PathEscape(index.writeAlias), nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", elasticsearchStatusError("verify switched alias", status, payload)
	}
	current, currentMembers, err := decodeElasticsearchAliasMembers(payload, index.writeAlias)
	if err != nil {
		return "", err
	}
	if current != target || len(currentMembers) != 1 {
		return "", errors.New("elasticsearch alias switch did not converge to one target")
	}
	return previous, nil
}

func decodeElasticsearchAliasMembers(payload []byte, writeAlias string) (string, []string, error) {
	var aliases map[string]struct {
		Aliases map[string]struct {
			IsWriteIndex bool `json:"is_write_index"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(payload, &aliases); err != nil || len(aliases) == 0 {
		return "", nil, errors.New("write alias response is invalid")
	}
	members := make([]string, 0, len(aliases))
	writeIndex := ""
	for indexName, value := range aliases {
		alias, exists := value.Aliases[writeAlias]
		if !exists || !validElasticsearchName(indexName) {
			continue
		}
		members = append(members, indexName)
		if alias.IsWriteIndex {
			if writeIndex != "" {
				return "", nil, fmt.Errorf("alias %q has multiple write indices", writeAlias)
			}
			writeIndex = indexName
		}
	}
	if writeIndex == "" || len(members) == 0 {
		return "", nil, fmt.Errorf("alias %q must have exactly one write index", writeAlias)
	}
	sort.Strings(members)
	return writeIndex, members, nil
}

func decodeElasticsearchWriteIndex(payload []byte, writeAlias string) (string, error) {
	var aliases map[string]struct {
		Aliases map[string]struct {
			IsWriteIndex bool `json:"is_write_index"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(payload, &aliases); err != nil || len(aliases) == 0 {
		return "", errors.New("write alias response is invalid")
	}
	writable := 0
	writeIndex := ""
	for indexName, value := range aliases {
		if value.Aliases[writeAlias].IsWriteIndex {
			writable++
			writeIndex = indexName
		}
	}
	if writable != 1 || !validElasticsearchName(writeIndex) {
		return "", fmt.Errorf("alias %q must have exactly one write index", writeAlias)
	}
	return writeIndex, nil
}

func validateElasticsearchMapping(payload []byte) error {
	var mappings map[string]struct {
		Mappings struct {
			Dynamic    any `json:"dynamic"`
			Properties map[string]struct {
				Type           string `json:"type"`
				Analyzer       string `json:"analyzer"`
				SearchAnalyzer string `json:"search_analyzer"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(payload, &mappings); err != nil || len(mappings) == 0 {
		return errors.New("mapping response is invalid")
	}
	for _, index := range mappings {
		if fmt.Sprint(index.Mappings.Dynamic) != "strict" {
			return errors.New("mapping must set dynamic=strict")
		}
		required := map[string]string{
			"lot_id": "keyword", "room_id": "keyword", "main_account_id": "keyword", "title": "text",
			"description": "text", "category": "keyword", "tags": "keyword", "image_url": "keyword", "status": "keyword",
			"start_price_fen": "long", "current_price_fen": "long", "currency": "keyword",
			"starts_at": "date", "ends_at": "date", "href": "keyword", "public_visible": "boolean", "lot_version": "long",
			"last_event_id": "keyword", "content_hash": "keyword", "indexed_at": "date",
		}
		for field, fieldType := range required {
			if index.Mappings.Properties[field].Type != fieldType {
				return fmt.Errorf("mapping field %s must be %s", field, fieldType)
			}
		}
		title := index.Mappings.Properties["title"]
		description := index.Mappings.Properties["description"]
		if title.Analyzer != "ik_max_word" || title.SearchAnalyzer != "ik_smart" ||
			description.Analyzer != "ik_max_word" || description.SearchAnalyzer != "ik_smart" {
			return errors.New("title and description must use ik_max_word with ik_smart search analysis")
		}
	}
	return nil
}

func (index *ElasticsearchIndex) ApplyDocument(ctx context.Context, document LotDocument) (ElasticsearchApplyResult, error) {
	return index.applyDocument(ctx, index.writeAlias, document, index.documentState)
}

// ApplyDocumentTo writes a rebuild document to an explicit versioned index.
// It keeps the same strict external-version identity rules as the live alias.
func (index *ElasticsearchIndex) ApplyDocumentTo(ctx context.Context, indexName string, document LotDocument) (ElasticsearchApplyResult, error) {
	indexName = strings.TrimSpace(indexName)
	if !validElasticsearchName(indexName) {
		return ElasticsearchApplyResult{}, errors.New("elasticsearch target index name is invalid")
	}
	return index.applyDocument(ctx, indexName, document, func(ctx context.Context, lotID string) (elasticsearchDocumentState, error) {
		return index.documentStateAt(ctx, indexName, lotID)
	})
}

func (index *ElasticsearchIndex) applyDocument(
	ctx context.Context,
	target string,
	document LotDocument,
	readState func(context.Context, string) (elasticsearchDocumentState, error),
) (ElasticsearchApplyResult, error) {
	if index == nil || index.client == nil {
		return ElasticsearchApplyResult{}, errors.New("elasticsearch index is not initialized")
	}
	if err := validateElasticsearchDocument(document); err != nil {
		return ElasticsearchApplyResult{}, err
	}
	if !validElasticsearchName(target) || readState == nil {
		return ElasticsearchApplyResult{}, errors.New("elasticsearch write target and state reader are required")
	}
	body, err := json.Marshal(newElasticsearchDocument(document))
	if err != nil {
		return ElasticsearchApplyResult{}, fmt.Errorf("marshal Elasticsearch document: %w", err)
	}
	path := "/" + url.PathEscape(target) + "/_doc/" + url.PathEscape(document.LotID) +
		"?version=" + strconv.FormatInt(document.LotVersion, 10) + "&version_type=external"
	for attempt := 0; attempt < 2; attempt++ {
		status, payload, err := index.request(ctx, http.MethodPut, path, body)
		if err != nil {
			return ElasticsearchApplyResult{}, err
		}
		if status == http.StatusOK || status == http.StatusCreated {
			var response struct {
				Version int64 `json:"_version"`
			}
			if err := json.Unmarshal(payload, &response); err != nil || response.Version != document.LotVersion {
				return ElasticsearchApplyResult{}, errors.New("elasticsearch write response has an invalid external version")
			}
			return ElasticsearchApplyResult{Applied: true}, nil
		}
		if status != http.StatusConflict {
			return ElasticsearchApplyResult{}, elasticsearchStatusError("index document", status, payload)
		}
		state, err := readState(ctx, document.LotID)
		if err != nil {
			return ElasticsearchApplyResult{}, err
		}
		if !state.Found {
			if attempt == 0 {
				continue
			}
			return ElasticsearchApplyResult{}, errors.New("elasticsearch version conflict was returned without a current document")
		}
		if state.LotVersion > document.LotVersion {
			return ElasticsearchApplyResult{Stale: true}, nil
		}
		if state.LotVersion == document.LotVersion {
			if state.LastEventID == document.LastEventID && state.ContentHash == document.ContentHash {
				return ElasticsearchApplyResult{Duplicate: true}, nil
			}
			return ElasticsearchApplyResult{}, fmt.Errorf("%w: lot_id=%s version=%d", ErrElasticsearchVersionConflict, document.LotID, document.LotVersion)
		}
	}
	return ElasticsearchApplyResult{}, errors.New("elasticsearch external-version retry did not converge")
}

func (index *ElasticsearchIndex) SearchKeywords(ctx context.Context, query KeywordSearchQuery) ([]LotDocument, error) {
	if index == nil || index.client == nil || index.baseURL == nil {
		return nil, errors.New("elasticsearch index is not initialized")
	}
	query.Query = strings.TrimSpace(query.Query)
	query.RoomID = strings.TrimSpace(query.RoomID)
	query.LotID = strings.TrimSpace(query.LotID)
	if len(query.Query) > 1024 || (query.RoomID != "" && !validVectorText(query.RoomID, 64)) ||
		(query.LotID != "" && !validVectorText(query.LotID, 64)) || len(query.Statuses) > 16 {
		return nil, errors.New("elasticsearch keyword query is invalid")
	}
	filters := []any{map[string]any{"term": map[string]any{"public_visible": true}}}
	if query.RoomID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"room_id": query.RoomID}})
	}
	if query.LotID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"lot_id": query.LotID}})
	}
	if len(query.Statuses) > 0 {
		statuses := make([]string, 0, len(query.Statuses))
		seen := make(map[string]struct{}, len(query.Statuses))
		for _, status := range query.Statuses {
			status = strings.TrimSpace(status)
			if !validVectorText(status, 64) {
				return nil, errors.New("elasticsearch keyword status filter is invalid")
			}
			if _, duplicate := seen[status]; duplicate {
				continue
			}
			seen[status] = struct{}{}
			statuses = append(statuses, status)
		}
		filters = append(filters, map[string]any{"terms": map[string]any{"status": statuses}})
	}
	must := []any{map[string]any{"match_all": map[string]any{}}}
	if query.Query != "" {
		must = []any{map[string]any{"multi_match": map[string]any{
			"query": query.Query, "fields": []string{"title^4", "description^2", "category^2", "tags^2"},
			"type": "best_fields", "operator": "or", "minimum_should_match": "30%",
		}}}
	}
	requestBody := map[string]any{
		"size": NormalizeLimit(query.Limit, DefaultSearchLimit), "track_total_hits": false,
		"_source": []string{"lot_id"},
		"query":   map[string]any{"bool": map[string]any{"must": must, "filter": filters}},
		"sort":    []any{map[string]any{"_score": "desc"}, map[string]any{"lot_id": "asc"}},
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal Elasticsearch keyword query: %w", err)
	}
	status, responsePayload, err := index.request(ctx, http.MethodPost, "/"+url.PathEscape(index.writeAlias)+"/_search", payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, elasticsearchStatusError("search keywords", status, responsePayload)
	}
	var response struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					LotID string `json:"lot_id"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(responsePayload, &response); err != nil {
		return nil, fmt.Errorf("decode Elasticsearch keyword response: %w", err)
	}
	documents := make([]LotDocument, 0, len(response.Hits.Hits))
	seen := make(map[string]struct{}, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		lotID := strings.TrimSpace(hit.Source.LotID)
		if !validVectorText(lotID, 64) || hit.ID != lotID {
			return nil, errors.New("elasticsearch keyword hit identity is inconsistent")
		}
		if _, duplicate := seen[lotID]; duplicate {
			return nil, errors.New("elasticsearch keyword response contains a duplicate lot")
		}
		seen[lotID] = struct{}{}
		documents = append(documents, LotDocument{LotID: lotID})
	}
	return documents, nil
}

type ElasticsearchDocumentIdentity struct {
	Found       bool
	LotVersion  int64
	LastEventID string
	ContentHash string
}

type elasticsearchDocumentState = ElasticsearchDocumentIdentity

func (index *ElasticsearchIndex) CurrentDocumentIdentity(ctx context.Context, lotID string) (ElasticsearchDocumentIdentity, error) {
	if index == nil {
		return ElasticsearchDocumentIdentity{}, errors.New("elasticsearch index is not initialized")
	}
	lotID = strings.TrimSpace(lotID)
	if !validVectorText(lotID, 64) {
		return ElasticsearchDocumentIdentity{}, errors.New("elasticsearch lot_id is invalid")
	}
	// The alias is verified at construction to have exactly one write index, so
	// reading through it avoids a second alias-resolution request for every
	// reconciliation sample.
	return index.documentStateAt(ctx, index.writeAlias, lotID)
}

func (index *ElasticsearchIndex) DocumentIdentity(ctx context.Context, indexName, lotID string) (ElasticsearchDocumentIdentity, error) {
	indexName = strings.TrimSpace(indexName)
	lotID = strings.TrimSpace(lotID)
	if !validElasticsearchName(indexName) || !validVectorText(lotID, 64) {
		return ElasticsearchDocumentIdentity{}, errors.New("elasticsearch identity target or lot_id is invalid")
	}
	return index.documentStateAt(ctx, indexName, lotID)
}

func (index *ElasticsearchIndex) documentState(ctx context.Context, lotID string) (elasticsearchDocumentState, error) {
	status, payload, err := index.request(ctx, http.MethodGet, "/_alias/"+url.PathEscape(index.writeAlias), nil)
	if err != nil {
		return elasticsearchDocumentState{}, fmt.Errorf("resolve Elasticsearch write alias: %w", err)
	}
	if status != http.StatusOK {
		return elasticsearchDocumentState{}, elasticsearchStatusError("resolve write alias", status, payload)
	}
	writeIndex, err := decodeElasticsearchWriteIndex(payload, index.writeAlias)
	if err != nil {
		return elasticsearchDocumentState{}, err
	}
	return index.documentStateAt(ctx, writeIndex, lotID)
}

func (index *ElasticsearchIndex) documentStateAt(ctx context.Context, indexName, lotID string) (elasticsearchDocumentState, error) {
	path := "/" + url.PathEscape(indexName) + "/_doc/" + url.PathEscape(lotID) +
		"?_source_includes=lot_version,last_event_id,content_hash"
	status, payload, err := index.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return elasticsearchDocumentState{}, err
	}
	if status == http.StatusNotFound {
		return elasticsearchDocumentState{}, nil
	}
	if status != http.StatusOK {
		return elasticsearchDocumentState{}, elasticsearchStatusError("read document identity", status, payload)
	}
	var response struct {
		Found   bool  `json:"found"`
		Version int64 `json:"_version"`
		Source  struct {
			LotVersion  int64  `json:"lot_version"`
			LastEventID string `json:"last_event_id"`
			ContentHash string `json:"content_hash"`
		} `json:"_source"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return elasticsearchDocumentState{}, fmt.Errorf("decode Elasticsearch document identity: %w", err)
	}
	if !response.Found {
		return elasticsearchDocumentState{}, nil
	}
	if response.Version <= 0 || response.Version != response.Source.LotVersion ||
		eventcontract.ValidateEventID(response.Source.LastEventID) != nil || !validSHA256Text(response.Source.ContentHash) {
		return elasticsearchDocumentState{}, fmt.Errorf("%w: stored document identity is inconsistent", ErrElasticsearchVersionConflict)
	}
	return elasticsearchDocumentState{
		Found: true, LotVersion: response.Source.LotVersion,
		LastEventID: response.Source.LastEventID, ContentHash: response.Source.ContentHash,
	}, nil
}

func newElasticsearchDocument(document LotDocument) elasticsearchDocument {
	return elasticsearchDocument{
		LotID: document.LotID, RoomID: document.RoomID, MainAccountID: document.MainAccountID,
		Title: document.Title, Description: document.Description, Category: document.Category,
		Tags: append([]string(nil), document.Tags...), ImageURL: document.ImageURL, Status: document.Status,
		StartPriceFen: document.StartPrice.GetAmount(), CurrentPriceFen: document.CurrentPrice.GetAmount(),
		Currency: document.CurrentPrice.GetCurrency(), StartsAtUnixMs: document.StartsAtUnixMs, EndsAtUnixMs: document.EndsAtUnixMs,
		Href: document.Href, PublicVisible: document.PublicVisible, LotVersion: document.LotVersion,
		LastEventID: document.LastEventID, ContentHash: document.ContentHash, IndexedAtUnixMilli: time.Now().UnixMilli(),
	}
}

func validateElasticsearchDocument(document LotDocument) error {
	if !validVectorText(document.LotID, 64) || !validVectorText(document.RoomID, 64) ||
		!validVectorText(document.MainAccountID, 64) || !validVectorText(document.Title, 255) || len(document.Description) > 65_535 ||
		len(document.Category) > 64 || len(document.ImageURL) > 1024 || !validVectorText(document.Status, 64) || len(document.Href) > 1024 ||
		document.LotVersion <= 0 || eventcontract.ValidateEventID(document.LastEventID) != nil || !validSHA256Text(document.ContentHash) || len(document.Tags) > 100 {
		return fmt.Errorf("%w: document identity, version, content, or bounds are invalid", ErrInvalidElasticsearchDocument)
	}
	for _, tag := range document.Tags {
		if !validVectorText(tag, 64) {
			return fmt.Errorf("%w: tag is invalid", ErrInvalidElasticsearchDocument)
		}
	}
	if document.StartPrice == nil || document.CurrentPrice == nil || document.StartPrice.GetAmount() < 0 ||
		document.CurrentPrice.GetAmount() < document.StartPrice.GetAmount() || document.StartPrice.GetCurrency() != document.CurrentPrice.GetCurrency() ||
		!validVectorCurrency(document.StartPrice.GetCurrency()) || document.StartsAtUnixMs < 0 || document.EndsAtUnixMs < 0 {
		return fmt.Errorf("%w: money or time fields are invalid", ErrInvalidElasticsearchDocument)
	}
	return nil
}

func (index *ElasticsearchIndex) request(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	endpoint := strings.TrimRight(index.baseURL.String(), "/") + "/" + strings.TrimLeft(path, "/")
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("create Elasticsearch request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if index.username != "" {
		request.SetBasicAuth(index.username, index.password)
	}
	response, err := index.client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("execute Elasticsearch request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, index.maxResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read Elasticsearch response: %w", err)
	}
	if int64(len(payload)) > index.maxResponseBytes {
		return 0, nil, errors.New("elasticsearch response exceeded the configured size limit")
	}
	return response.StatusCode, payload, nil
}

func elasticsearchStatusError(operation string, status int, payload []byte) error {
	message := strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\x00' {
			return ' '
		}
		return character
	}, strings.TrimSpace(string(payload)))
	if len(message) > 512 {
		message = message[:512]
	}
	return fmt.Errorf("elasticsearch %s returned HTTP %d: %s", operation, status, message)
}

func validElasticsearchName(value string) bool {
	if value == "" || len(value) > 255 || value == "." || value == ".." || strings.ToLower(value) != value {
		return false
	}
	return !strings.ContainsAny(value, "\\/*?\"<>| ,#:") && value[0] != '-' && value[0] != '_' && value[0] != '+'
}
