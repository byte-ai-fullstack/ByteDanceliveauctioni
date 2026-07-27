package shop

import (
	"context"
	"testing"
)

type fakeRepo struct {
	lastProductQuery       ProductQuery
	lastOrderQuery         OrderQuery
	lastFrequentStoreQuery FrequentStoreQuery
	lastPayRequest         MockPayRequest
	createCalled           bool
}

func (r *fakeRepo) ListProducts(_ context.Context, query ProductQuery) (ProductList, error) {
	r.lastProductQuery = query
	return ProductList{Products: []Product{{ID: "p1"}}, Total: 1, Page: query.Page, PageSize: query.PageSize}, nil
}

func (r *fakeRepo) FindProductByID(_ context.Context, productID string) (*Product, error) {
	return &Product{ID: productID}, nil
}

func (r *fakeRepo) ListDeliveryAddresses(_ context.Context, _ string) ([]DeliveryAddress, error) {
	return []DeliveryAddress{{ID: "addr1", ReceiverName: "买家", Phone: "13900000000", Street: "街道", Detail: "门牌", IsDefault: true}}, nil
}

func (r *fakeRepo) FindDeliveryAddress(_ context.Context, _ string, addressID string) (*DeliveryAddress, error) {
	return &DeliveryAddress{ID: addressID, ReceiverName: "买家", Phone: "13900000000", Street: "街道", Detail: "门牌", IsDefault: true}, nil
}

func (r *fakeRepo) CreateDeliveryAddress(_ context.Context, _ string, input DeliveryAddressInput) (*DeliveryAddress, error) {
	return &DeliveryAddress{ID: "addr1", ReceiverName: input.ReceiverName, Phone: input.Phone, Street: input.Street, Detail: input.Detail, IsDefault: input.IsDefault}, nil
}

func (r *fakeRepo) UpdateDeliveryAddress(_ context.Context, _ string, addressID string, input DeliveryAddressInput) (*DeliveryAddress, error) {
	return &DeliveryAddress{ID: addressID, ReceiverName: input.ReceiverName, Phone: input.Phone, Street: input.Street, Detail: input.Detail, IsDefault: input.IsDefault}, nil
}

func (r *fakeRepo) DeleteDeliveryAddress(_ context.Context, _, _ string) error {
	return nil
}

func (r *fakeRepo) SetDefaultDeliveryAddress(_ context.Context, _, addressID string) ([]DeliveryAddress, error) {
	return []DeliveryAddress{{ID: addressID, ReceiverName: "买家", Phone: "13900000000", Street: "街道", Detail: "门牌", IsDefault: true}}, nil
}

func (r *fakeRepo) CreateOrder(_ context.Context, _ UserRef, req CreateOrderRequest) (*Order, error) {
	r.createCalled = true
	return &Order{ID: "o1", Items: []OrderItem{{SKUID: req.SKUID, Quantity: req.Quantity}}}, nil
}

func (r *fakeRepo) ListShopOrders(_ context.Context, query OrderQuery) (OrderList, error) {
	r.lastOrderQuery = query
	return OrderList{Orders: []Order{{ID: "o1"}}, Total: 1, Page: query.Page, PageSize: query.PageSize}, nil
}

func (r *fakeRepo) FindUserOrder(_ context.Context, userID, orderID string) (*UserOrder, error) {
	return &UserOrder{ID: orderID, UserID: userID}, nil
}

func (r *fakeRepo) ListUserOrders(_ context.Context, query OrderQuery) (UserOrderList, error) {
	r.lastOrderQuery = query
	return UserOrderList{Orders: []UserOrder{{ID: "o1", UserID: query.UserID}}, Total: 1, Page: query.Page, PageSize: query.PageSize}, nil
}

func (r *fakeRepo) ListFrequentStores(_ context.Context, query FrequentStoreQuery) (FrequentStoreList, error) {
	r.lastFrequentStoreQuery = query
	return FrequentStoreList{Stores: []FrequentStore{{StoreKey: "shop:demo", StoreName: "demo"}}, Total: 1, Limit: query.Limit}, nil
}

func (r *fakeRepo) MockPayOrder(_ context.Context, _ string, _ string, req MockPayRequest) (*MockPayResult, error) {
	r.lastPayRequest = req
	return &MockPayResult{Order: Order{ID: "o1"}, Payment: Payment{ID: "p1"}, Paid: true}, nil
}

func TestUsecaseNormalizesProductPagination(t *testing.T) {
	repo := &fakeRepo{}
	uc := NewUsecase(repo)

	list, err := uc.ListProducts(context.Background(), ProductQuery{Query: "  玉镯  ", PageSize: 999})
	if err != nil {
		t.Fatalf("ListProducts error: %v", err)
	}
	if list.Page != 1 || list.PageSize != 50 {
		t.Fatalf("unexpected pagination: %+v", list)
	}
	if repo.lastProductQuery.Query != "玉镯" {
		t.Fatalf("query was not trimmed: %q", repo.lastProductQuery.Query)
	}
}

func TestUsecaseCreateOrderValidatesInput(t *testing.T) {
	repo := &fakeRepo{}
	uc := NewUsecase(repo)

	if _, err := uc.CreateOrder(context.Background(), UserRef{ID: "u1"}, CreateOrderRequest{SKUID: "", Quantity: 1}); err == nil {
		t.Fatal("expected missing sku error")
	}
	if repo.createCalled {
		t.Fatal("repo should not be called for invalid sku")
	}
	if _, err := uc.CreateOrder(context.Background(), UserRef{ID: "u1"}, CreateOrderRequest{SKUID: "sku1", Quantity: 1}); err == nil {
		t.Fatal("expected missing address error")
	}
	if _, err := uc.CreateOrder(context.Background(), UserRef{ID: ""}, CreateOrderRequest{SKUID: "sku1", Quantity: 1, AddressID: "addr1"}); err == nil {
		t.Fatal("expected unauthenticated error")
	}
	order, err := uc.CreateOrder(context.Background(), UserRef{ID: "u1"}, CreateOrderRequest{SKUID: " sku1 ", Quantity: 2, AddressID: " addr1 "})
	if err != nil {
		t.Fatalf("CreateOrder error: %v", err)
	}
	if order.Items[0].SKUID != "sku1" || order.Items[0].Quantity != 2 {
		t.Fatalf("unexpected order item: %+v", order.Items[0])
	}
}

func TestUsecaseListOrdersScopesToUser(t *testing.T) {
	repo := &fakeRepo{}
	uc := NewUsecase(repo)

	if _, err := uc.ListMyOrders(context.Background(), "", OrderQuery{}); err == nil {
		t.Fatal("expected unauthenticated error")
	}
	list, err := uc.ListMyOrders(context.Background(), "u1", OrderQuery{Status: OrderStatusPaid, Page: 2})
	if err != nil {
		t.Fatalf("ListMyOrders error: %v", err)
	}
	if list.Page != 2 || repo.lastOrderQuery.UserID != "u1" || repo.lastOrderQuery.Status != OrderStatusPaid {
		t.Fatalf("unexpected query: %+v list=%+v", repo.lastOrderQuery, list)
	}
}

func TestUsecaseListFrequentStoresScopesToUser(t *testing.T) {
	repo := &fakeRepo{}
	uc := NewUsecase(repo)

	if _, err := uc.ListFrequentStores(context.Background(), "", 10); err == nil {
		t.Fatal("expected unauthenticated error")
	}
	list, err := uc.ListFrequentStores(context.Background(), " u1 ", 999)
	if err != nil {
		t.Fatalf("ListFrequentStores error: %v", err)
	}
	if repo.lastFrequentStoreQuery.UserID != "u1" {
		t.Fatalf("user id was not trimmed: %q", repo.lastFrequentStoreQuery.UserID)
	}
	if repo.lastFrequentStoreQuery.Limit != 20 || list.Limit != 20 {
		t.Fatalf("limit was not normalized: query=%d list=%d", repo.lastFrequentStoreQuery.Limit, list.Limit)
	}
}

func TestUsecaseMockPayDefaultsIdempotencyKey(t *testing.T) {
	repo := &fakeRepo{}
	uc := NewUsecase(repo)

	result, err := uc.MockPayOrder(context.Background(), "u1", "o1", MockPayRequest{})
	if err != nil {
		t.Fatalf("MockPayOrder error: %v", err)
	}
	if !result.Paid {
		t.Fatal("expected paid result")
	}
	if repo.lastPayRequest.IdempotencyKey != "shop-pay-o1" {
		t.Fatalf("unexpected idempotency key: %q", repo.lastPayRequest.IdempotencyKey)
	}
}
