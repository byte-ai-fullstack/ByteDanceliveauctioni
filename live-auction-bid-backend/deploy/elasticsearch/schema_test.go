package elasticsearch

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIndexV1UsesStrictIKMapping(t *testing.T) {
	payload, err := os.ReadFile("index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Mappings struct {
			Dynamic    string `json:"dynamic"`
			Properties map[string]struct {
				Type           string `json:"type"`
				Analyzer       string `json:"analyzer"`
				SearchAnalyzer string `json:"search_analyzer"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(payload, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Mappings.Dynamic != "strict" || schema.Mappings.Properties["title"].Analyzer != "ik_max_word" ||
		schema.Mappings.Properties["title"].SearchAnalyzer != "ik_smart" || schema.Mappings.Properties["lot_version"].Type != "long" {
		t.Fatalf("unsafe Elasticsearch schema: %+v", schema.Mappings)
	}
	for _, forbidden := range []string{"external_gte", "dynamic\": true"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("mapping contains forbidden token %q", forbidden)
		}
	}
}

func TestImagePinsMatchingIKVersion(t *testing.T) {
	payload, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	content := string(payload)
	if !strings.Contains(content, "elasticsearch:8.19.17@sha256:") ||
		!strings.Contains(content, "elasticsearch-analysis-ik-8.19.17.zip") ||
		strings.Contains(content, "analysis-ik/latest") {
		t.Fatalf("Elasticsearch and IK versions are not pinned together: %s", content)
	}
}
