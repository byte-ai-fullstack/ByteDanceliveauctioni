package data

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAuctionLotModelMatchesUnifiedCurrencySchema(t *testing.T) {
	typeOf := reflect.TypeOf(AuctionLotModel{})
	currencyColumns := 0
	for index := 0; index < typeOf.NumField(); index++ {
		tag := typeOf.Field(index).Tag.Get("gorm")
		for _, forbidden := range []string{
			"start_price_currency", "min_increment_currency", "cap_price_currency", "current_price_currency", "final_price_currency",
		} {
			if strings.Contains(tag, forbidden) {
				t.Fatalf("AuctionLotModel still maps forbidden column %s", forbidden)
			}
		}
		if strings.Contains(tag, "column:currency") {
			currencyColumns++
		}
	}
	if currencyColumns != 1 {
		t.Fatalf("AuctionLotModel currency columns=%d want 1", currencyColumns)
	}
}

func TestNormalizeRuntimeOutboxShardCountPreventsRelayCoverageGaps(t *testing.T) {
	for _, value := range []int{0, -1, RuntimeOutboxShardCount} {
		got, err := normalizeRuntimeOutboxShardCount(value)
		if err != nil || got != RuntimeOutboxShardCount {
			t.Fatalf("value=%d got=%d error=%v", value, got, err)
		}
	}
	for _, value := range []int{1, 8, 32} {
		if _, err := normalizeRuntimeOutboxShardCount(value); err == nil {
			t.Fatalf("unsupported shard count %d was accepted", value)
		}
	}
}

func TestNewStoreRequiresSchemaVerifierBeforeOpeningDependencies(t *testing.T) {
	_, err := NewStore(context.Background(), Config{
		MySQLDSN:  "auction:secret@tcp(127.0.0.1:1)/live_auction",
		RedisAddr: "127.0.0.1:1",
	})
	if err == nil || !strings.Contains(err.Error(), "schema verifier is required") {
		t.Fatalf("NewStore error = %v", err)
	}
}
