package auction

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestApplyDraftPatchCommitsCompleteValidatedClone(t *testing.T) {
	lot := completeDraftLot()
	lot.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_UNSPECIFIED
	patchRule := completeBidRule()
	patchRule.StartPrice.Amount = 20_000
	request := &v1.PatchLotDraftRequest{
		RoomId:           "room-2",
		Title:            "patched title",
		Description:      "patched description",
		ImageUrl:         "https://cdn.example.test/main.jpg",
		GalleryImageUrls: []string{" https://cdn.example.test/one.jpg ", "https://cdn.example.test/two.jpg"},
		Category:         "collectibles",
		Tags:             []string{" rare ", "signed"},
		EstimatePrice:    &v1.Money{Amount: 30_000, Currency: "CNY"},
		Stock:            2,
		AfterSaleNotes:   "no returns",
		DepositAmount:    &v1.Money{Amount: 1_000, Currency: "CNY"},
		Rule:             patchRule,
		TrustCards: []*v1.TrustRevealCard{{
			Title: "certificate", ImageUrl: "https://cdn.example.test/card.jpg", Revealed: true, RevealedAtUnixMs: 99,
		}},
	}

	if err := ApplyDraftPatch(lot, request); err != nil {
		t.Fatalf("ApplyDraftPatch: %v", err)
	}
	if lot.GetRoomId() != "room-2" || lot.GetTitle() != "patched title" || lot.GetDescription() != "patched description" ||
		lot.GetCategory() != "collectibles" || lot.GetStock() != 2 || lot.GetAfterSaleNotes() != "no returns" || lot.GetVersion() != 2 || lot.GetConfigVersion() != 2 {
		t.Fatalf("patched lot fields mismatch: %+v", lot)
	}
	if lot.GetQueueStatus() != v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE || lot.GetCurrentPrice().GetAmount() != 20_000 ||
		lot.GetFinalPrice().GetCurrency() != "CNY" || lot.GetDepositAmount().GetAmount() != 1_000 {
		t.Fatalf("patched runtime defaults mismatch: %+v", lot)
	}
	if len(lot.GetGalleryImageUrls()) != 2 || lot.GetGalleryImageUrls()[0] != "https://cdn.example.test/one.jpg" ||
		len(lot.GetTags()) != 2 || lot.GetTags()[0] != "rare" {
		t.Fatalf("normalized slices mismatch: gallery=%v tags=%v", lot.GetGalleryImageUrls(), lot.GetTags())
	}
	if len(lot.GetTrustCards()) != 1 || lot.GetTrustCards()[0].GetId() == "" || lot.GetTrustCards()[0].GetLotId() != lot.GetId() ||
		lot.GetTrustCards()[0].GetRevealed() || lot.GetTrustCards()[0].GetRevealedAtUnixMs() != 0 {
		t.Fatalf("normalized trust card mismatch: %+v", lot.GetTrustCards())
	}

	request.Rule.StartPrice.Amount = 1
	request.Tags[0] = "mutated"
	request.GalleryImageUrls[0] = "https://mutated.example.test/image.jpg"
	request.DepositAmount.Amount = 2
	if lot.GetRule().GetStartPrice().GetAmount() != 20_000 || lot.GetTags()[0] != "rare" ||
		lot.GetGalleryImageUrls()[0] != "https://cdn.example.test/one.jpg" || lot.GetDepositAmount().GetAmount() != 1_000 {
		t.Fatal("patched lot aliases request-owned data")
	}
}

func TestApplyDraftPatchFailureLeavesOriginalUntouched(t *testing.T) {
	lot := completeDraftLot()
	original := proto.Clone(lot).(*v1.Lot)
	request := &v1.PatchLotDraftRequest{
		Title: "must not leak",
		GalleryImageUrls: []string{
			"https://cdn.example.test/1.jpg", "https://cdn.example.test/2.jpg", "https://cdn.example.test/3.jpg",
			"https://cdn.example.test/4.jpg", "https://cdn.example.test/5.jpg", "https://cdn.example.test/6.jpg",
			"https://cdn.example.test/7.jpg",
		},
	}
	if err := ApplyDraftPatch(lot, request); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("ApplyDraftPatch error=%v want invalid argument", err)
	}
	if !proto.Equal(lot, original) {
		t.Fatalf("failed patch mutated lot: got=%v want=%v", lot, original)
	}
}

func TestApplyDraftPatchRejectsInvalidTargetsAndFields(t *testing.T) {
	if err := ApplyDraftPatch(nil, &v1.PatchLotDraftRequest{}); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("nil lot error=%v", err)
	}
	if err := ApplyDraftPatch(completeDraftLot(), nil); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("nil request error=%v", err)
	}
	for _, lot := range []*v1.Lot{
		{Status: v1.LotStatus_LOT_STATUS_LIVE},
		{Status: v1.LotStatus_LOT_STATUS_EXTENDED},
		{Status: v1.LotStatus_LOT_STATUS_SETTLED},
		{Status: v1.LotStatus_LOT_STATUS_CANCELLED},
		{Status: v1.LotStatus_LOT_STATUS_FAILED},
		{Status: v1.LotStatus_LOT_STATUS_DRAFT, QueueStatus: v1.LotQueueStatus_LOT_QUEUE_STATUS_NEXT},
		{Status: v1.LotStatus_LOT_STATUS_QUEUED, QueueStatus: v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED},
	} {
		if err := ApplyDraftPatch(lot, &v1.PatchLotDraftRequest{Title: "blocked"}); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("lot=%v error=%v", lot, err)
		}
	}

	tests := []*v1.PatchLotDraftRequest{
		{ImageUrl: "file:///tmp/image.jpg"},
		{Stock: -1},
		{DepositAmount: &v1.Money{Amount: -1, Currency: "CNY"}},
		{DepositAmount: &v1.Money{Amount: 1}},
		{TrustCards: []*v1.TrustRevealCard{{ImageUrl: "javascript:alert(1)"}}},
	}
	for _, request := range tests {
		lot := completeDraftLot()
		original := proto.Clone(lot).(*v1.Lot)
		if err := ApplyDraftPatch(lot, request); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("request=%v error=%v", request, err)
		}
		if !proto.Equal(lot, original) {
			t.Fatalf("invalid request mutated lot: request=%v", request)
		}
	}
}

func TestQueueLotOnlyMutatesPreStartQueueState(t *testing.T) {
	lot := completeDraftLot()
	configVersion := lot.GetConfigVersion()
	if err := QueueLot(lot, 3); err != nil {
		t.Fatalf("QueueLot: %v", err)
	}
	if lot.GetStatus() != v1.LotStatus_LOT_STATUS_QUEUED || lot.GetQueueStatus() != v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED ||
		lot.GetQueuePosition() != 3 || lot.GetVersion() != 2 || lot.GetConfigVersion() != configVersion {
		t.Fatalf("queued lot mismatch: %+v", lot)
	}
	if err := QueueLot(lot, 99); err != nil || lot.GetQueuePosition() != 3 || lot.GetVersion() != 2 {
		t.Fatalf("idempotent queue changed lot: lot=%+v error=%v", lot, err)
	}
	for _, status := range []v1.LotStatus{
		v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED, v1.LotStatus_LOT_STATUS_SETTLED,
		v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED,
	} {
		candidate := completeDraftLot()
		candidate.Status = status
		if err := QueueLot(candidate, 1); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("status=%s queue error=%v want invalid argument", status, err)
		}
	}
}

func TestValidateLotReadyRejectsEachUnsafeRuleBoundary(t *testing.T) {
	valid := completeBidRule()
	tests := []struct {
		title    string
		imageURL string
		rule     *v1.BidRule
	}{
		{imageURL: "https://cdn.example.test/main.jpg", rule: valid},
		{title: "lot", rule: valid},
		{title: "lot", imageURL: "file:///tmp/image.jpg", rule: valid},
		{title: "lot", imageURL: "https://cdn.example.test/main.jpg"},
		{title: "lot", imageURL: "https://cdn.example.test/main.jpg", rule: &v1.BidRule{StartPrice: &v1.Money{Amount: 1, Currency: "CNY"}}},
	}
	mutations := []func(*v1.BidRule){
		func(rule *v1.BidRule) { rule.StartPrice.Currency = "" },
		func(rule *v1.BidRule) { rule.MinIncrement.Currency = "USD" },
		func(rule *v1.BidRule) { rule.StartPrice.Amount = -1 },
		func(rule *v1.BidRule) { rule.MinIncrement.Amount = 0 },
		func(rule *v1.BidRule) { rule.DurationSeconds = 59 },
		func(rule *v1.BidRule) { rule.AntiSnipeWindowSeconds = 0 },
		func(rule *v1.BidRule) { rule.AntiSnipeExtendSeconds = 9 },
		func(rule *v1.BidRule) { rule.AntiSnipeExtendSeconds = 31 },
		func(rule *v1.BidRule) { rule.MaxExtendCount = 0 },
		func(rule *v1.BidRule) { rule.CapPrice = &v1.Money{Amount: 20_000} },
		func(rule *v1.BidRule) { rule.CapPrice = &v1.Money{Amount: 20_000, Currency: "USD"} },
		func(rule *v1.BidRule) { rule.CapPrice = &v1.Money{Amount: rule.StartPrice.Amount, Currency: "CNY"} },
	}
	for _, mutate := range mutations {
		rule := proto.Clone(valid).(*v1.BidRule)
		mutate(rule)
		tests = append(tests, struct {
			title    string
			imageURL string
			rule     *v1.BidRule
		}{title: "lot", imageURL: "https://cdn.example.test/main.jpg", rule: rule})
	}
	for index, test := range tests {
		if err := ValidateLotReady(test.title, test.imageURL, test.rule); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
	if err := ValidateLotReady("lot", "https://cdn.example.test/main.jpg", valid); err != nil {
		t.Fatalf("valid lot rejected: %v", err)
	}
}

func TestLotDraftDefaultsAndPreStartStatusClassification(t *testing.T) {
	if _, err := NewLotDraftFromRequest("lot-1", nil, false); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("nil draft request error=%v", err)
	}
	draft, err := NewLotDraftFromRequest("lot-1", &v1.CreateLotRequest{Stock: 0}, false)
	if err != nil {
		t.Fatalf("create incomplete draft: %v", err)
	}
	if draft.GetStock() != 1 || draft.GetRule() == nil || draft.GetCurrentPrice() == nil || draft.GetVersion() != 1 {
		t.Fatalf("draft defaults mismatch: %+v", draft)
	}
	if _, err := NewLotDraftFromRequest("lot-1", &v1.CreateLotRequest{Stock: -1}, false); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("negative stock error=%v", err)
	}

	for _, status := range []v1.LotStatus{
		v1.LotStatus_LOT_STATUS_DRAFT, v1.LotStatus_LOT_STATUS_READY, v1.LotStatus_LOT_STATUS_QUEUED,
	} {
		if !IsPreStartCancellableStatus(status) {
			t.Fatalf("status %s should be pre-start cancellable", status)
		}
	}
	for _, status := range []v1.LotStatus{v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_SETTLED} {
		if IsPreStartCancellableStatus(status) {
			t.Fatalf("status %s should not be pre-start cancellable", status)
		}
	}
}

func completeDraftLot() *v1.Lot {
	return &v1.Lot{
		Id: "lot-1", RoomId: "room-1", MainAccountId: "main-1", Title: "lot", Description: "description",
		ImageUrl: "https://cdn.example.test/original.jpg", Status: v1.LotStatus_LOT_STATUS_DRAFT,
		QueueStatus: v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE, Rule: completeBidRule(),
		CurrentPrice: &v1.Money{Amount: 10_000, Currency: "CNY"}, FinalPrice: &v1.Money{Currency: "CNY"},
		Version: 1, ConfigVersion: 1, Stock: 1, Stats: &v1.LotStats{}, DuelState: &v1.DuelState{},
	}
}

func completeBidRule() *v1.BidRule {
	return &v1.BidRule{
		StartPrice:      &v1.Money{Amount: 10_000, Currency: "CNY"},
		MinIncrement:    &v1.Money{Amount: 100, Currency: "CNY"},
		CapPrice:        &v1.Money{Amount: 50_000, Currency: "CNY"},
		DurationSeconds: 60, AntiSnipeWindowSeconds: 10, AntiSnipeExtendSeconds: 30, MaxExtendCount: 3,
	}
}

func TestValidateHTTPImageURLBounds(t *testing.T) {
	if err := validateHTTPImageURL("image", ""); err != nil {
		t.Fatalf("empty optional URL: %v", err)
	}
	if err := validateHTTPImageURL("image", "https://cdn.example.test/image.jpg"); err != nil {
		t.Fatalf("valid URL: %v", err)
	}
	for _, value := range []string{"not-a-url", "ftp://cdn.example.test/image.jpg", "https://" + strings.Repeat("a", 1024)} {
		if err := validateHTTPImageURL("image", value); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("URL length=%d error=%v", len(value), err)
		}
	}
}
