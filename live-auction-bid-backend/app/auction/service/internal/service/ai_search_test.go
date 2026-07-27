package service

import (
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

func TestReciprocalRankFusionRewardsCrossRetrieverAgreement(t *testing.T) {
	keyword := []searchindex.LotDocument{{LotID: "lot-a"}, {LotID: "lot-b"}, {LotID: "lot-c"}, {LotID: "lot-a"}}
	vector := []searchindex.LotDocument{{LotID: "lot-b"}, {LotID: "lot-d"}, {LotID: "lot-a"}}
	ranked := reciprocalRankFusion([][]searchindex.LotDocument{keyword, vector}, 60, 10)
	if len(ranked) != 4 {
		t.Fatalf("ranked=%+v", ranked)
	}
	if ranked[0].LotID != "lot-b" || ranked[0].Sources != 2 || ranked[1].LotID != "lot-a" || ranked[1].Sources != 2 {
		t.Fatalf("cross-source agreement was not rewarded: %+v", ranked)
	}
	if ranked[2].LotID != "lot-d" || ranked[3].LotID != "lot-c" {
		t.Fatalf("single-source rank order changed: %+v", ranked)
	}
}

func TestReciprocalRankFusionBoundsAndTieBreaksDeterministically(t *testing.T) {
	ranked := reciprocalRankFusion([][]searchindex.LotDocument{{{LotID: "lot-b"}, {LotID: "lot-a"}}, {{LotID: "lot-a"}, {LotID: "lot-b"}}}, 0, 1)
	if len(ranked) != 1 || ranked[0].LotID != "lot-a" {
		t.Fatalf("ranked=%+v", ranked)
	}
	if got := reciprocalRankFusion(nil, 60, 10); len(got) != 0 {
		t.Fatalf("empty fusion=%+v", got)
	}
}
