package auction

import (
	"context"
	"errors"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestMockPaymentProviderValidationAndSuccess(t *testing.T) {
	if _, err := NewPaymentProviderFromName(""); !errors.Is(err, apperr.ErrPaymentProviderNotConfigured) {
		t.Fatalf("empty provider error=%v", err)
	}
	if _, err := NewPaymentProviderFromName("stripe"); !errors.Is(err, apperr.ErrPaymentProviderNotConfigured) {
		t.Fatalf("unsupported provider error=%v", err)
	}
	provider, err := NewPaymentProviderFromName(" MOCK ")
	if err != nil || provider.Name() != "mock" {
		t.Fatalf("mock provider=%v error=%v", provider, err)
	}
	invalid := []PaymentProviderRequest{
		{UserID: "buyer", Amount: 100, Currency: "CNY", IdempotencyKey: "idem"},
		{BusinessID: "order", Amount: 100, Currency: "CNY", IdempotencyKey: "idem"},
		{BusinessID: "order", UserID: "buyer", Amount: -1, Currency: "CNY", IdempotencyKey: "idem"},
		{BusinessID: "order", UserID: "buyer", Amount: 100, IdempotencyKey: "idem"},
		{BusinessID: "order", UserID: "buyer", Amount: 100, Currency: "CNY"},
	}
	for _, request := range invalid {
		if _, err := provider.Pay(context.Background(), request); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("request=%+v error=%v", request, err)
		}
	}
	result, err := provider.Pay(context.Background(), PaymentProviderRequest{
		BusinessID: "order-1", BusinessType: "ORDER", UserID: "buyer", Amount: 100, Currency: "CNY", IdempotencyKey: "idem",
	})
	if err != nil || result.ProviderPaymentID != "mock_order-1" || result.PaidAtUnixMs <= 0 {
		t.Fatalf("provider result=%+v error=%v", result, err)
	}
}

func TestAuctionVisibilityStateAndPagination(t *testing.T) {
	visible := PublicVisibleLotStatuses()
	if len(visible) != 3 {
		t.Fatalf("public statuses=%v", visible)
	}
	for _, status := range visible {
		if !IsPublicVisibleLotStatus(status) {
			t.Fatalf("status %s missing from public classifier", status)
		}
	}
	if IsPublicVisibleLotStatus(v1.LotStatus_LOT_STATUS_DRAFT) {
		t.Fatal("draft lot is public")
	}

	tests := []struct {
		lot  *v1.Lot
		want AuctionState
	}{
		{nil, AuctionStateFailed},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_DRAFT}, AuctionStateDraft},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_READY}, AuctionStateDraft},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_QUEUED}, AuctionStateQueued},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_LIVE}, AuctionStateLive},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_LIVE, DuelState: &v1.DuelState{ExtendCount: 1}}, AuctionStateExtended},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_EXTENDED}, AuctionStateExtended},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_SETTLED}, AuctionStateSettled},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_CANCELLED}, AuctionStateCancelled},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_FAILED}, AuctionStateFailed},
		{&v1.Lot{Status: v1.LotStatus_LOT_STATUS_UNSPECIFIED}, AuctionStateFailed},
	}
	for _, test := range tests {
		if got := AuctionStateOf(test.lot); got != test.want {
			t.Fatalf("AuctionStateOf(%v)=%s want=%s", test.lot, got, test.want)
		}
	}
	if page, size := NormalizePagination(0, 0); page != 1 || size != 20 {
		t.Fatalf("default pagination=%d/%d", page, size)
	}
	if page, size := NormalizePagination(2, 1000); page != 2 || size != 100 || PageOffset(page, size) != 100 {
		t.Fatalf("bounded pagination=%d/%d offset=%d", page, size, PageOffset(page, size))
	}
}

func TestLotResultViewerOrderAuthorization(t *testing.T) {
	order := &Order{MainAccountID: "main-1", BuyerUserID: "buyer-1"}
	if (LotResultViewer{}).CanViewOrder(nil) {
		t.Fatal("nil order was visible")
	}
	admin := LotResultViewer{MainAccountID: "main-1", PermissionCodes: []string{userbiz.PermissionOrderManage}}
	if !admin.CanViewOrder(order) {
		t.Fatal("same-main admin cannot view order")
	}
	admin.MainAccountID = "main-2"
	if admin.CanViewOrder(order) {
		t.Fatal("cross-main admin can view order")
	}
	buyer := LotResultViewer{UserID: "buyer-1", PermissionCodes: []string{userbiz.PermissionOrderViewOwn}}
	if !buyer.CanViewOrder(order) {
		t.Fatal("buyer cannot view own order")
	}
	buyer.UserID = "buyer-2"
	if buyer.CanViewOrder(order) {
		t.Fatal("buyer can view another order")
	}
}

func TestOrderAndPaymentLifecycleValidatesEveryTransition(t *testing.T) {
	settled := &v1.Lot{
		Id: "lot-1", RoomId: "room-1", MainAccountId: "main-1", Title: "lot", ImageUrl: "https://cdn.example.test/lot.jpg",
		Status: v1.LotStatus_LOT_STATUS_SETTLED, WinnerUserId: "buyer-1", WinnerNickname: "buyer",
		FinalPrice: &v1.Money{Amount: 12_000, Currency: "CNY"},
	}
	for _, input := range []struct {
		id  string
		lot *v1.Lot
	}{
		{"", settled},
		{"order-1", nil},
		{"order-1", &v1.Lot{FinalPrice: &v1.Money{Amount: 1, Currency: "CNY"}}},
		{"order-1", &v1.Lot{WinnerUserId: "buyer-1"}},
	} {
		if _, err := NewOrderFromSettledLot(input.id, input.lot, 1_000); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("order input=%+v error=%v", input, err)
		}
	}
	order, err := NewOrderFromSettledLot("order-1", settled, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != OrderStatusPendingPayment || order.PaymentStatus != PaymentStatusInit || order.ExpiresAtUnixMs != 1_000+OrderPaymentWindowMs || order.Version != 1 {
		t.Fatalf("new order=%+v", order)
	}
	expired := *order
	expired.ExpiresAtUnixMs = 1
	if summary := expired.Summary(); summary.Status != OrderStatusExpired || summary.PaymentStatus != PaymentStatusClosed {
		t.Fatalf("expired summary=%+v", summary)
	}

	invalidPayments := []struct {
		id       string
		order    Order
		key      string
		amount   int64
		currency string
	}{
		{"", *order, "idem", order.Amount, order.Currency},
		{"payment-1", *order, "", order.Amount, order.Currency},
		{"payment-1", Order{}, "idem", order.Amount, order.Currency},
		{"payment-1", Order{ID: "order", Status: OrderStatusPaid}, "idem", order.Amount, order.Currency},
		{"payment-1", *order, "idem", order.Amount + 1, order.Currency},
	}
	for _, input := range invalidPayments {
		if _, err := NewPayment(input.id, input.order, input.key, input.amount, input.currency, 2_000); !errors.Is(err, apperr.ErrInvalidArgument) {
			t.Fatalf("payment input=%+v error=%v", input, err)
		}
	}
	payment, err := NewPayment("payment-1", *order, "idem", order.Amount, order.Currency, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	var nilPayment *Payment
	if err := nilPayment.MarkProcessing(3_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("nil processing error=%v", err)
	}
	wrongState := *payment
	wrongState.Status = PaymentStatusSuccess
	if err := wrongState.MarkProcessing(3_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("wrong processing state error=%v", err)
	}
	if err := payment.MarkProcessing(3_000); err != nil {
		t.Fatal(err)
	}
	if err := nilPayment.MarkSuccess(4_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("nil success error=%v", err)
	}
	wrongState = *payment
	wrongState.Status = PaymentStatusInit
	if err := wrongState.MarkSuccess(4_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("wrong success state error=%v", err)
	}
	if err := payment.MarkSuccess(4_000); err != nil {
		t.Fatal(err)
	}
	if summary := payment.Summary(); summary.Status != PaymentStatusSuccess || summary.SucceededAtMs != 4_000 {
		t.Fatalf("payment summary=%+v", summary)
	}

	if err := MarkOrderPaid(nil, *payment, 5_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("nil order error=%v", err)
	}
	paidOrder := *order
	paidOrder.Status = OrderStatusPaid
	if err := MarkOrderPaid(&paidOrder, *payment, 5_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("already paid error=%v", err)
	}
	badPayment := *payment
	badPayment.Status = PaymentStatusFailed
	if err := MarkOrderPaid(order, badPayment, 5_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("failed payment error=%v", err)
	}
	badPayment = *payment
	badPayment.Amount++
	if err := MarkOrderPaid(order, badPayment, 5_000); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("amount mismatch error=%v", err)
	}
	if err := MarkOrderPaid(order, *payment, 5_000); err != nil {
		t.Fatal(err)
	}
	if order.Status != OrderStatusPaid || order.PaymentStatus != PaymentStatusSuccess || order.PaymentID != payment.ID || order.PaidAtUnixMs != 5_000 || order.Version != 2 {
		t.Fatalf("paid order=%+v", order)
	}
}
