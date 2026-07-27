package auction

import (
	"context"
	"errors"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

type depositHoldRepoStub struct {
	hold  *DepositHold
	found bool
}

func (s depositHoldRepoStub) FindDepositHoldByLotBuyer(context.Context, string, string) (*DepositHold, bool, error) {
	return s.hold, s.found, nil
}

func (s depositHoldRepoStub) FindDepositHoldByIdempotencyKey(context.Context, string, string, string) (*DepositHold, bool, error) {
	return nil, false, nil
}

func (s depositHoldRepoStub) CommitDepositHold(context.Context, DepositHold) (*DepositHold, error) {
	return nil, errors.New("not implemented")
}

func TestEnsureDepositHeldRequiresHoldForZeroAmountLot(t *testing.T) {
	lot := &v1.Lot{
		Id:            "lot-zero-deposit",
		DepositAmount: &v1.Money{Amount: 0, Currency: "CNY"},
	}
	uc := &AuctionUsecase{deposits: depositHoldRepoStub{}}

	err := uc.ensureDepositHeld(context.Background(), lot, "buyer-1")
	if !errors.Is(err, apperr.ErrDepositRequired) {
		t.Fatalf("expected deposit required for zero-amount lot without hold, got %v", err)
	}
}

func TestEnsureDepositHeldAcceptsZeroAmountHeldDeposit(t *testing.T) {
	lot := &v1.Lot{
		Id:            "lot-zero-deposit",
		DepositAmount: &v1.Money{Amount: 0, Currency: "CNY"},
	}
	uc := &AuctionUsecase{deposits: depositHoldRepoStub{
		hold: &DepositHold{
			LotID:       lot.GetId(),
			BuyerUserID: "buyer-1",
			Status:      DepositStatusHeld,
			Amount:      0,
			Currency:    "CNY",
		},
		found: true,
	}}

	if err := uc.ensureDepositHeld(context.Background(), lot, "buyer-1"); err != nil {
		t.Fatalf("expected held zero-amount deposit to pass, got %v", err)
	}
}
