package searchindex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestElasticsearchApplyUsesStrictExternalVersion(t *testing.T) {
	var indexed elasticsearchDocument
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case request.URL.Path == "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case request.Method == http.MethodPut && request.URL.Path == "/auction-lots-current/_doc/lot-1":
			if request.URL.Query().Get("version") != "7" || request.URL.Query().Get("version_type") != "external" || request.URL.Query().Has("external_gte") {
				t.Fatalf("query=%s", request.URL.RawQuery)
			}
			if err := json.NewDecoder(request.Body).Decode(&indexed); err != nil {
				t.Fatal(err)
			}
			writeElasticsearchTestJSON(writer, http.StatusCreated, map[string]any{"_version": 7})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	result, err := index.ApplyDocument(context.Background(), validVectorDocument(t))
	if err != nil {
		t.Fatalf("ApplyDocument: %v", err)
	}
	if !result.Applied || result.Duplicate || result.Stale || indexed.LotID != "lot-1" || indexed.LotVersion != 7 || indexed.IndexedAtUnixMilli <= 0 {
		t.Fatalf("result=%+v document=%+v", result, indexed)
	}
}

func TestElasticsearchConflictClassification(t *testing.T) {
	for _, test := range []struct {
		name        string
		version     int64
		eventID     string
		contentHash string
		stale       bool
		duplicate   bool
		conflict    bool
	}{
		{name: "stale", version: 8, stale: true},
		{name: "duplicate", version: 7, duplicate: true},
		{name: "same version different identity", version: 7, contentHash: strings.Repeat("b", 64), conflict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := validVectorDocument(t)
			eventID := test.eventID
			if eventID == "" {
				eventID = document.LastEventID
			}
			contentHash := test.contentHash
			if contentHash == "" {
				contentHash = document.ContentHash
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch {
				case request.URL.Path == "/_alias/auction-lots-current":
					writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
				case request.URL.Path == "/auction-lots-v1/_mapping":
					writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
				case request.Method == http.MethodPut:
					writeElasticsearchTestJSON(writer, http.StatusConflict, map[string]any{"error": "version_conflict_engine_exception"})
				case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/_doc/"):
					writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{
						"found": true, "_version": test.version,
						"_source": map[string]any{"lot_version": test.version, "last_event_id": eventID, "content_hash": contentHash},
					})
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			index := newTestElasticsearchIndex(t, server.URL)
			result, err := index.ApplyDocument(context.Background(), document)
			if test.conflict {
				if !errors.Is(err, ErrElasticsearchVersionConflict) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || result.Stale != test.stale || result.Duplicate != test.duplicate {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestCurrentElasticsearchDocumentIdentityResolvesWriteAlias(t *testing.T) {
	document := validVectorDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case "/auction-lots-current/_doc/lot-1":
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{
				"found": true, "_version": document.LotVersion,
				"_source": map[string]any{
					"lot_version": document.LotVersion, "last_event_id": document.LastEventID, "content_hash": document.ContentHash,
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	identity, err := index.CurrentDocumentIdentity(context.Background(), document.LotID)
	if err != nil || !identity.Found || identity.LotVersion != document.LotVersion || identity.ContentHash != document.ContentHash {
		t.Fatalf("identity=%+v error=%v", identity, err)
	}
}

func TestNewElasticsearchIndexRejectsUnsafeMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "_alias") {
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
			return
		}
		mapping := mappingResponse()
		properties := mapping["auction-lots-v1"].(map[string]any)["mappings"].(map[string]any)["properties"].(map[string]any)
		properties["title"].(map[string]any)["analyzer"] = "standard"
		writeElasticsearchTestJSON(writer, http.StatusOK, mapping)
	}))
	defer server.Close()
	_, err := NewElasticsearchIndex(context.Background(), ElasticsearchConfig{BaseURL: server.URL, WriteAlias: "auction-lots-current"})
	if !errors.Is(err, ErrElasticsearchSchemaUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestElasticsearchResponseLimitAndAuthentication(t *testing.T) {
	authenticated := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		authenticated = authenticated || (ok && username == "indexer" && password == "secret")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(strings.Repeat("x", 256)))
	}))
	defer server.Close()
	_, err := NewElasticsearchIndex(context.Background(), ElasticsearchConfig{
		BaseURL: server.URL, Username: "indexer", Password: "secret", WriteAlias: "auction-lots-current",
		RequestTimeout: time.Second, MaxResponseBytes: 64,
	})
	if err == nil || !authenticated {
		t.Fatalf("error=%v authenticated=%t", err, authenticated)
	}
}

func TestElasticsearchApplyRejectsInvalidDocumentWithoutIO(t *testing.T) {
	index := &ElasticsearchIndex{client: &http.Client{}}
	document := validVectorDocument(t)
	document.CurrentPrice.Amount = -1
	if _, err := index.ApplyDocument(context.Background(), document); !errors.Is(err, ErrInvalidElasticsearchDocument) {
		t.Fatalf("error=%v", err)
	}
}

func TestElasticsearchApplyReturnsBoundedStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		default:
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("cluster\nunavailable"))
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	_, err := index.ApplyDocument(context.Background(), validVectorDocument(t))
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("error=%v", err)
	}
}

func TestElasticsearchConflictRejectsCorruptStoredIdentity(t *testing.T) {
	document := validVectorDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case request.URL.Path == "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case request.Method == http.MethodPut:
			writeElasticsearchTestJSON(writer, http.StatusConflict, map[string]any{"error": "version conflict"})
		default:
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{
				"found": true, "_version": 8,
				"_source": map[string]any{"lot_version": 7, "last_event_id": document.LastEventID, "content_hash": document.ContentHash},
			})
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	_, err := index.ApplyDocument(context.Background(), document)
	if !errors.Is(err, ErrElasticsearchVersionConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestElasticsearchSearchKeywordsUsesPublicFiltersAndRankOrder(t *testing.T) {
	document := validVectorDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case request.URL.Path == "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case request.Method == http.MethodPost && request.URL.Path == "/auction-lots-current/_search":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body)
			text := string(encoded)
			for _, required := range []string{"public_visible", "room-1", "LOT_STATUS_LIVE", "title^4", "lot_id"} {
				if !strings.Contains(text, required) {
					t.Fatalf("query missing %q: %s", required, text)
				}
			}
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"hits": map[string]any{"hits": []any{
				map[string]any{"_id": document.LotID, "_source": newElasticsearchDocument(document)},
			}}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	documents, err := index.SearchKeywords(context.Background(), KeywordSearchQuery{
		Query: "翡翠手镯", RoomID: "room-1", Statuses: []string{"LOT_STATUS_LIVE"}, Limit: 20,
	})
	if err != nil || len(documents) != 1 || documents[0].LotID != "lot-1" {
		t.Fatalf("documents=%+v error=%v", documents, err)
	}
}

func TestElasticsearchSearchKeywordsRejectsInvalidAndDuplicateHits(t *testing.T) {
	index := &ElasticsearchIndex{client: &http.Client{}, baseURL: &url.URL{Scheme: "http", Host: "example.test"}}
	if _, err := index.SearchKeywords(context.Background(), KeywordSearchQuery{RoomID: " bad"}); err == nil {
		t.Fatal("invalid room filter was accepted")
	}
	document := validVectorDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		default:
			hit := map[string]any{"_id": document.LotID, "_source": newElasticsearchDocument(document)}
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"hits": map[string]any{"hits": []any{hit, hit}}})
		}
	}))
	defer server.Close()
	index = newTestElasticsearchIndex(t, server.URL)
	if _, err := index.SearchKeywords(context.Background(), KeywordSearchQuery{Query: "jade"}); err == nil {
		t.Fatal("duplicate Elasticsearch hits were accepted")
	}
}

func TestElasticsearchRebuildTargetCreateResumeAndExplicitWrite(t *testing.T) {
	targetExists := false
	document := validVectorDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case request.URL.Path == "/auction-lots-v1/_mapping" || request.URL.Path == "/auction-lots-v2/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case request.URL.Path == "/auction-lots-v2" && request.Method == http.MethodGet:
			if !targetExists {
				writeElasticsearchTestJSON(writer, http.StatusNotFound, map[string]any{"status": 404})
				return
			}
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"auction-lots-v2": map[string]any{}})
		case request.URL.Path == "/auction-lots-v2" && request.Method == http.MethodPut:
			targetExists = true
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"acknowledged": true})
		case request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/auction-lots-v2/_doc/lot-1"):
			writeElasticsearchTestJSON(writer, http.StatusCreated, map[string]any{"_version": document.LotVersion})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	definition := []byte(`{"settings":{},"mappings":{}}`)
	created, err := index.EnsureVersionedIndex(context.Background(), "auction-lots-v2", definition, false)
	if err != nil || !created {
		t.Fatalf("created=%t error=%v", created, err)
	}
	created, err = index.EnsureVersionedIndex(context.Background(), "auction-lots-v2", definition, true)
	if err != nil || created {
		t.Fatalf("resume created=%t error=%v", created, err)
	}
	if _, err := index.EnsureVersionedIndex(context.Background(), "auction-lots-v2", definition, false); err == nil {
		t.Fatal("existing target was accepted without explicit resume")
	}
	result, err := index.ApplyDocumentTo(context.Background(), "auction-lots-v2", document)
	if err != nil || !result.Applied {
		t.Fatalf("apply result=%+v error=%v", result, err)
	}
}

func TestElasticsearchSwitchWriteAliasRemovesEveryOldMember(t *testing.T) {
	current := "auction-lots-v1"
	switched := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_alias/auction-lots-current" && current == "auction-lots-v1":
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{
				"auction-lots-v0": map[string]any{"aliases": map[string]any{"auction-lots-current": map[string]any{}}},
				"auction-lots-v1": map[string]any{"aliases": map[string]any{"auction-lots-current": map[string]any{"is_write_index": true}}},
			})
		case request.URL.Path == "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{
				"auction-lots-v2": map[string]any{"aliases": map[string]any{"auction-lots-current": map[string]any{"is_write_index": true}}},
			})
		case request.URL.Path == "/auction-lots-v1/_mapping" || request.URL.Path == "/auction-lots-v2/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case request.Method == http.MethodPost && request.URL.Path == "/_aliases":
			var body struct {
				Actions []map[string]map[string]any `json:"actions"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Actions) != 3 {
				t.Fatalf("alias actions=%+v error=%v", body.Actions, err)
			}
			current = "auction-lots-v2"
			switched = true
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"acknowledged": true})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	previous, err := index.SwitchWriteAlias(context.Background(), "auction-lots-v2")
	if err != nil || previous != "auction-lots-v1" || !switched {
		t.Fatalf("previous=%q switched=%t error=%v", previous, switched, err)
	}
}

func TestElasticsearchRefreshCountAndDocumentIdentity(t *testing.T) {
	document := validVectorDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_alias/auction-lots-current":
			writeElasticsearchTestJSON(writer, http.StatusOK, aliasResponse())
		case "/auction-lots-v1/_mapping":
			writeElasticsearchTestJSON(writer, http.StatusOK, mappingResponse())
		case "/auction-lots-v2/_refresh":
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"_shards": map[string]any{"failed": 0}})
		case "/auction-lots-v2/_count":
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{"count": 9})
		case "/auction-lots-v2/_doc/lot-1":
			writeElasticsearchTestJSON(writer, http.StatusOK, map[string]any{
				"found": true, "_version": document.LotVersion,
				"_source": map[string]any{"lot_version": document.LotVersion, "last_event_id": document.LastEventID, "content_hash": document.ContentHash},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	index := newTestElasticsearchIndex(t, server.URL)
	if err := index.RefreshIndex(context.Background(), "auction-lots-v2"); err != nil {
		t.Fatal(err)
	}
	if count, err := index.CountDocuments(context.Background(), "auction-lots-v2"); err != nil || count != 9 {
		t.Fatalf("count=%d error=%v", count, err)
	}
	identity, err := index.DocumentIdentity(context.Background(), "auction-lots-v2", "lot-1")
	if err != nil || !identity.Found || identity.LotVersion != document.LotVersion {
		t.Fatalf("identity=%+v error=%v", identity, err)
	}
}

func newTestElasticsearchIndex(t *testing.T, baseURL string) *ElasticsearchIndex {
	t.Helper()
	index, err := NewElasticsearchIndex(context.Background(), ElasticsearchConfig{
		BaseURL: baseURL, WriteAlias: "auction-lots-current", RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func aliasResponse() map[string]any {
	return map[string]any{
		"auction-lots-v1": map[string]any{"aliases": map[string]any{
			"auction-lots-current": map[string]any{"is_write_index": true},
		}},
	}
}

func mappingResponse() map[string]any {
	properties := map[string]any{}
	for field, fieldType := range map[string]string{
		"lot_id": "keyword", "room_id": "keyword", "main_account_id": "keyword", "category": "keyword", "tags": "keyword",
		"image_url": "keyword", "status": "keyword", "start_price_fen": "long", "current_price_fen": "long", "currency": "keyword",
		"starts_at": "date", "ends_at": "date", "public_visible": "boolean", "lot_version": "long",
		"last_event_id": "keyword", "content_hash": "keyword", "indexed_at": "date", "href": "keyword",
	} {
		properties[field] = map[string]any{"type": fieldType}
	}
	properties["title"] = map[string]any{"type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart"}
	properties["description"] = map[string]any{"type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart"}
	return map[string]any{
		"auction-lots-v1": map[string]any{"mappings": map[string]any{"dynamic": "strict", "properties": properties}},
	}
}

func writeElasticsearchTestJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
