package data

import (
	"errors"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
)

func TestLotPresentationModelRoundTripAndOverlay(t *testing.T) {
	lot := &v1.Lot{
		Id: "lot-1", MainAccountId: "main-1", Status: v1.LotStatus_LOT_STATUS_EXTENDED,
		Version: 23, PresentationVersion: 4, PlaybookStage: v1.PlaybookStage_PLAYBOOK_STAGE_DUEL_MODE,
		TrustCards: []*v1.TrustRevealCard{{Id: "card-1", LotId: "lot-1", Revealed: true, RevealedAtUnixMs: 100}},
		DuelState:  &v1.DuelState{Active: true, LotId: "lot-1", UserAId: "buyer-1", UserBId: "buyer-2", StartedAtUnixMs: 90},
	}
	model, err := lotPresentationToModel(lot, 200)
	if err != nil {
		t.Fatalf("lotPresentationToModel: %v", err)
	}
	if model.LotID != lot.GetId() || model.Version != 4 || model.UpdatedAtUnixMs != 200 || model.Payload == "" {
		t.Fatalf("model mismatch: %+v", model)
	}
	presentation, err := modelToLotPresentation(model)
	if err != nil {
		t.Fatalf("modelToLotPresentation: %v", err)
	}
	base := &v1.Lot{
		Id: lot.Id, MainAccountId: lot.MainAccountId, Status: lot.Status, Version: lot.Version,
		PlaybookStage: v1.PlaybookStage_PLAYBOOK_STAGE_BIDDING_ACTIVE,
		DuelState:     &v1.DuelState{LotId: lot.Id, EndsAtUnixMs: 500, ExtendCount: 2, MaxExtendCount: 3},
	}
	if err := auction.OverlayLotPresentation(base, presentation); err != nil {
		t.Fatalf("OverlayLotPresentation: %v", err)
	}
	if base.GetVersion() != 23 || base.GetPresentationVersion() != 4 || !base.GetTrustCards()[0].GetRevealed() ||
		base.GetDuelState().GetUserAId() != "buyer-1" || base.GetDuelState().GetEndsAtUnixMs() != 500 || base.GetDuelState().GetExtendCount() != 2 {
		t.Fatalf("round-trip overlay mismatch: %+v", base)
	}
}

func TestLotPresentationConversionRejectsInvalidState(t *testing.T) {
	if _, err := lotPresentationToModel(nil, 1); err == nil {
		t.Fatal("nil lot was accepted")
	}
	if _, err := lotPresentationToModel(&v1.Lot{PresentationVersion: 1}, 0); err == nil {
		t.Fatal("zero update time was accepted")
	}
	if _, err := modelToLotPresentation(nil); err == nil {
		t.Fatal("nil model was accepted")
	}
	if _, err := modelToLotPresentation(&AuctionLotPresentationModel{LotID: "lot-1", MainAccountID: "main-1", Version: 1, Payload: "{"}); err == nil {
		t.Fatal("invalid payload was accepted")
	}
}

func TestIsPresentationDuplicateKey(t *testing.T) {
	if !isPresentationDuplicateKey(&mysqlDriver.MySQLError{Number: 1062, Message: "duplicate"}) {
		t.Fatal("MySQL duplicate key was not detected")
	}
	if isPresentationDuplicateKey(errors.New("duplicate text without driver code")) {
		t.Fatal("plain text error was treated as a duplicate key")
	}
}
