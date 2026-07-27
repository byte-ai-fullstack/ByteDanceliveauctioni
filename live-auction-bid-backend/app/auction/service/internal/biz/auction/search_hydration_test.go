package auction

import "testing"

func TestNormalizeBatchLotIDsPreservesOrderAndDeduplicates(t *testing.T) {
	ids, err := normalizeBatchLotIDs([]string{" lot-2 ", "lot-1", "lot-2"})
	if err != nil || len(ids) != 2 || ids[0] != "lot-2" || ids[1] != "lot-1" {
		t.Fatalf("ids=%v error=%v", ids, err)
	}
	if _, err := normalizeBatchLotIDs([]string{"bad\nlot"}); err == nil {
		t.Fatal("invalid lot id was accepted")
	}
	tooMany := make([]string, 101)
	for index := range tooMany {
		tooMany[index] = "lot"
	}
	if _, err := normalizeBatchLotIDs(tooMany); err == nil {
		t.Fatal("oversized lot id batch was accepted")
	}
}
