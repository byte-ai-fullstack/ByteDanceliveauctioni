package searchindex

import (
	"errors"
	"math"
	"strings"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestPrepareVectorDocumentSetsStableEmbeddingIdentity(t *testing.T) {
	index := &PGVectorIndex{provider: "dashscope", model: "text-embedding-v4", modelVersion: "2026-06", dimensions: 3}
	document := validVectorDocument(t)
	if err := index.prepareDocument(&document, []float64{0.1, 0.2, 0.3}); err != nil {
		t.Fatalf("prepareDocument: %v", err)
	}
	if document.EmbeddingProvider != "dashscope" || document.EmbeddingModelVersion != "2026-06" ||
		document.EmbeddingDimensions != 3 || len(document.EmbeddingHash) != 64 {
		t.Fatalf("document=%+v", document)
	}
}

func TestPrepareVectorDocumentRejectsInvalidMoneyAndVector(t *testing.T) {
	index := &PGVectorIndex{provider: "dashscope", model: "model", modelVersion: "v1", dimensions: 3}
	document := validVectorDocument(t)
	document.CurrentPrice.Currency = "cny"
	if err := index.prepareDocument(&document, nil); err == nil {
		t.Fatal("lowercase currency was accepted")
	}
	document = validVectorDocument(t)
	if err := index.prepareDocument(&document, []float64{0.1, math.NaN(), 0.3}); err == nil {
		t.Fatal("non-finite vector was accepted")
	}
	document = validVectorDocument(t)
	document.ContentHash = strings.Repeat("z", 64)
	if err := index.prepareDocument(&document, nil); err == nil {
		t.Fatal("invalid content hash was accepted")
	}
}

func TestPGVectorIdentityDefaults(t *testing.T) {
	var index *PGVectorIndex
	if index.Provider() != "dashscope" || index.Model() != DefaultEmbeddingModel ||
		index.ModelVersion() != DefaultEmbeddingModel || index.Dimensions() != DefaultEmbeddingDimensions || index.TableName() != DefaultPGVectorTable {
		t.Fatal("nil index identity defaults changed")
	}
}

func TestPGVectorTableNameValidation(t *testing.T) {
	accepted := []string{"", DefaultPGVectorTable, "auction_lot_search_docs_v2", "auction_lot_search_docs_backup_20260727_120000"}
	for _, value := range accepted {
		if _, err := normalizePGVectorTableName(value); err != nil {
			t.Fatalf("normalizePGVectorTableName(%q): %v", value, err)
		}
	}
	for _, value := range []string{
		"auction_lot_search_docs_v0", "public.auction_lot_search_docs", `auction_lot_search_docs_v2\";DROP TABLE x`, "other",
		"auction_lot_search_docs_v" + strings.Repeat("9", 64),
	} {
		if _, err := normalizePGVectorTableName(value); err == nil {
			t.Fatalf("unsafe table name %q was accepted", value)
		}
	}
}

func TestParseVectorLiteralValidatesShapeAndValues(t *testing.T) {
	vector, err := parseVectorLiteral("[0.25,-1,3.5]", 3)
	if err != nil || len(vector) != 3 || vector[1] != -1 {
		t.Fatalf("vector=%v error=%v", vector, err)
	}
	for _, value := range []string{"0.25,-1,3.5", "[1,2]", "[1,NaN,3]"} {
		if _, err := parseVectorLiteral(value, 3); err == nil {
			t.Fatalf("invalid vector literal %q was accepted", value)
		}
	}
}

func TestScanVectorCandidateIDsReturnsOnlyOrderedIdentities(t *testing.T) {
	rows := &candidateIDRows{values: []string{"lot-2", "lot-1"}}
	documents, err := scanVectorCandidateIDs(rows)
	if err != nil || len(documents) != 2 || documents[0].LotID != "lot-2" || documents[1].LotID != "lot-1" {
		t.Fatalf("documents=%+v error=%v", documents, err)
	}
	if documents[0].Title != "" || documents[0].CurrentPrice != nil {
		t.Fatalf("candidate query leaked stale projection fields: %+v", documents[0])
	}
}

func TestScanVectorCandidateIDsRejectsDuplicatesAndRowErrors(t *testing.T) {
	if _, err := scanVectorCandidateIDs(&candidateIDRows{values: []string{"lot-1", "lot-1"}}); err == nil {
		t.Fatal("duplicate pgvector candidate was accepted")
	}
	expected := errors.New("rows failed")
	if _, err := scanVectorCandidateIDs(&candidateIDRows{rowErr: expected}); !errors.Is(err, expected) {
		t.Fatalf("error=%v", err)
	}
}

type candidateIDRows struct {
	values []string
	index  int
	rowErr error
}

func (rows *candidateIDRows) Next() bool {
	if rows.index >= len(rows.values) {
		return false
	}
	rows.index++
	return true
}

func (rows *candidateIDRows) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("unexpected scan width")
	}
	value, ok := dest[0].(*string)
	if !ok {
		return errors.New("unexpected scan target")
	}
	*value = rows.values[rows.index-1]
	return nil
}

func (rows *candidateIDRows) Err() error { return rows.rowErr }

func validVectorDocument(t *testing.T) LotDocument {
	t.Helper()
	eventID, err := eventcontract.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	return LotDocument{
		LotID: "lot-1", RoomID: "room-1", MainAccountID: "merchant-1", Title: "Jade vase",
		Description: "Vintage", Category: "jewelry", Tags: []string{"jade"}, SearchText: "Jade vase\nVintage",
		Status: v1.LotStatus_LOT_STATUS_LIVE.String(), StartPrice: &v1.Money{Amount: 10_000, Currency: "CNY"},
		CurrentPrice: &v1.Money{Amount: 12_000, Currency: "CNY"}, StartsAtUnixMs: 100, EndsAtUnixMs: 200,
		Href: "/m/room/room-1", PublicVisible: true, LotVersion: 7, LastEventID: eventID,
		ContentHash: strings.Repeat("a", 64),
	}
}
