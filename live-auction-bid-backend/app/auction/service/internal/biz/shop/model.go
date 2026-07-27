package shop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

const (
	DefaultCurrency = "CNY"

	OrderSourceAuction OrderSource = "auction"
	OrderSourceShop    OrderSource = "shop"

	OrderStatusPendingPayment OrderStatus = "pending_payment"
	OrderStatusPaid           OrderStatus = "paid"
	OrderStatusShipped        OrderStatus = "shipped"
	OrderStatusCompleted      OrderStatus = "completed"
	OrderStatusCancelled      OrderStatus = "cancelled"
	OrderStatusExpired        OrderStatus = "expired"
	OrderStatusRefunded       OrderStatus = "refunded"

	PaymentStatusInit       PaymentStatus = "init"
	PaymentStatusProcessing PaymentStatus = "processing"
	PaymentStatusSuccess    PaymentStatus = "success"
	PaymentStatusFailed     PaymentStatus = "failed"
	PaymentStatusClosed     PaymentStatus = "closed"

	DeliveryAddressStatusActive  DeliveryAddressStatus = "active"
	DeliveryAddressStatusDeleted DeliveryAddressStatus = "deleted"
)

type OrderSource string
type OrderStatus string
type PaymentStatus string
type DeliveryAddressStatus string

type DeliveryAddress struct {
	ID              string                `json:"id"`
	UserID          string                `json:"userId,omitempty"`
	ReceiverName    string                `json:"receiverName"`
	Phone           string                `json:"phone"`
	Province        string                `json:"province"`
	City            string                `json:"city"`
	District        string                `json:"district"`
	Street          string                `json:"street"`
	Detail          string                `json:"detail"`
	PostalCode      string                `json:"postalCode,omitempty"`
	Tag             string                `json:"tag,omitempty"`
	IsDefault       bool                  `json:"isDefault"`
	Status          DeliveryAddressStatus `json:"status,omitempty"`
	CreatedAtUnixMs int64                 `json:"createdAtUnixMs,omitempty"`
	UpdatedAtUnixMs int64                 `json:"updatedAtUnixMs,omitempty"`
	DeletedAtUnixMs int64                 `json:"deletedAtUnixMs,omitempty"`
}

type DeliveryAddressInput struct {
	ReceiverName string `json:"receiverName"`
	Receiver     string `json:"receiver,omitempty"`
	Phone        string `json:"phone"`
	Province     string `json:"province"`
	City         string `json:"city"`
	District     string `json:"district"`
	Street       string `json:"street"`
	Detail       string `json:"detail"`
	PostalCode   string `json:"postalCode,omitempty"`
	Tag          string `json:"tag,omitempty"`
	IsDefault    bool   `json:"isDefault"`
}

type DeliveryAddressSnapshot struct {
	AddressID    string `json:"addressId"`
	ReceiverName string `json:"receiverName"`
	Phone        string `json:"phone"`
	Province     string `json:"province"`
	City         string `json:"city"`
	District     string `json:"district"`
	Street       string `json:"street"`
	Detail       string `json:"detail"`
	PostalCode   string `json:"postalCode,omitempty"`
	FullAddress  string `json:"fullAddress"`
}

type Product struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Subtitle            string   `json:"subtitle,omitempty"`
	Description         string   `json:"description,omitempty"`
	Category            string   `json:"category"`
	ShopName            string   `json:"shopName"`
	MainImageURL        string   `json:"mainImageUrl"`
	DetailImageURLs     []string `json:"detailImageUrls"`
	Tags                []string `json:"tags"`
	Badges              []string `json:"badges"`
	PriceAmount         int64    `json:"priceAmount"`
	OriginalPriceAmount int64    `json:"originalPriceAmount,omitempty"`
	Currency            string   `json:"currency"`
	SoldLabel           string   `json:"soldLabel"`
	Live                bool     `json:"live"`
	Status              string   `json:"status"`
	CreatedAtUnixMs     int64    `json:"createdAtUnixMs,omitempty"`
	UpdatedAtUnixMs     int64    `json:"updatedAtUnixMs,omitempty"`
	SKUs                []SKU    `json:"skus,omitempty"`
}

type SKU struct {
	ID          string `json:"id"`
	ProductID   string `json:"productId"`
	Name        string `json:"name"`
	PriceAmount int64  `json:"priceAmount"`
	Currency    string `json:"currency"`
	Stock       int64  `json:"stock"`
}

type OrderItem struct {
	ID          string `json:"id"`
	OrderID     string `json:"orderId"`
	ProductID   string `json:"productId"`
	SKUID       string `json:"skuId"`
	Title       string `json:"title"`
	ImageURL    string `json:"imageUrl"`
	SKUName     string `json:"skuName"`
	Quantity    int64  `json:"quantity"`
	UnitAmount  int64  `json:"unitAmount"`
	TotalAmount int64  `json:"totalAmount"`
	Currency    string `json:"currency"`
}

type UserOrderItem struct {
	ID           string      `json:"id"`
	OrderID      string      `json:"orderId"`
	Source       OrderSource `json:"source"`
	SourceItemID string      `json:"sourceItemId,omitempty"`
	ProductID    string      `json:"productId,omitempty"`
	SKUID        string      `json:"skuId,omitempty"`
	LotID        string      `json:"lotId,omitempty"`
	RoomID       string      `json:"roomId,omitempty"`
	Title        string      `json:"title"`
	ImageURL     string      `json:"imageUrl"`
	SKUName      string      `json:"skuName,omitempty"`
	Quantity     int64       `json:"quantity"`
	UnitAmount   int64       `json:"unitAmount"`
	TotalAmount  int64       `json:"totalAmount"`
	Currency     string      `json:"currency"`
}

type UserOrder struct {
	ID                      string                   `json:"id"`
	Source                  OrderSource              `json:"source"`
	SourceOrderID           string                   `json:"sourceOrderId,omitempty"`
	OrderNo                 string                   `json:"orderNo"`
	MainAccountID           string                   `json:"mainAccountId,omitempty"`
	UserID                  string                   `json:"userId"`
	Nickname                string                   `json:"nickname,omitempty"`
	Status                  OrderStatus              `json:"status"`
	PaymentStatus           PaymentStatus            `json:"paymentStatus"`
	PaymentID               string                   `json:"paymentId,omitempty"`
	Title                   string                   `json:"title"`
	ShopName                string                   `json:"shopName"`
	EnrichmentStatus        orderenrichment.Status   `json:"enrichmentStatus,omitempty"`
	EnrichmentUpdatedAtMs   int64                    `json:"enrichmentUpdatedAtUnixMs,omitempty"`
	TotalAmount             int64                    `json:"totalAmount"`
	Currency                string                   `json:"currency"`
	ShippingAddressID       string                   `json:"shippingAddressId,omitempty"`
	ShippingAddressSnapshot *DeliveryAddressSnapshot `json:"shippingAddressSnapshot,omitempty"`
	AddressSnapshot         string                   `json:"addressSnapshot,omitempty"`
	CreatedAtUnixMs         int64                    `json:"createdAtUnixMs"`
	UpdatedAtUnixMs         int64                    `json:"updatedAtUnixMs"`
	PaidAtUnixMs            int64                    `json:"paidAtUnixMs,omitempty"`
	ExpiresAtUnixMs         int64                    `json:"expiresAtUnixMs,omitempty"`
	Version                 int64                    `json:"version,omitempty"`
	PaymentIdempotencyKey   string                   `json:"-"`
	Items                   []UserOrderItem          `json:"items"`
}

type Order struct {
	ID                      string                   `json:"id"`
	OrderNo                 string                   `json:"orderNo"`
	UserID                  string                   `json:"userId"`
	Nickname                string                   `json:"nickname,omitempty"`
	Status                  OrderStatus              `json:"status"`
	PaymentStatus           PaymentStatus            `json:"paymentStatus"`
	PaymentID               string                   `json:"paymentId,omitempty"`
	ShopName                string                   `json:"shopName"`
	TotalAmount             int64                    `json:"totalAmount"`
	Currency                string                   `json:"currency"`
	ShippingAddressID       string                   `json:"shippingAddressId,omitempty"`
	ShippingAddressSnapshot *DeliveryAddressSnapshot `json:"shippingAddressSnapshot,omitempty"`
	AddressSnapshot         string                   `json:"addressSnapshot,omitempty"`
	CreatedAtUnixMs         int64                    `json:"createdAtUnixMs"`
	UpdatedAtUnixMs         int64                    `json:"updatedAtUnixMs"`
	PaidAtUnixMs            int64                    `json:"paidAtUnixMs,omitempty"`
	PaymentIdempotencyKey   string                   `json:"-"`
	Items                   []OrderItem              `json:"items"`
}

type Payment struct {
	ID              string        `json:"id"`
	OrderID         string        `json:"orderId"`
	UserID          string        `json:"userId"`
	Status          PaymentStatus `json:"status"`
	Amount          int64         `json:"amount"`
	Currency        string        `json:"currency"`
	IdempotencyKey  string        `json:"idempotencyKey,omitempty"`
	CreatedAtUnixMs int64         `json:"createdAtUnixMs"`
	SucceededAtMs   int64         `json:"succeededAtUnixMs,omitempty"`
}

type UserOrderPayment struct {
	ID              string        `json:"id"`
	OrderID         string        `json:"orderId"`
	Source          OrderSource   `json:"source"`
	Provider        string        `json:"provider"`
	UserID          string        `json:"userId"`
	Status          PaymentStatus `json:"status"`
	Amount          int64         `json:"amount"`
	Currency        string        `json:"currency"`
	IdempotencyKey  string        `json:"idempotencyKey,omitempty"`
	CreatedAtUnixMs int64         `json:"createdAtUnixMs"`
	UpdatedAtUnixMs int64         `json:"updatedAtUnixMs,omitempty"`
	SucceededAtMs   int64         `json:"succeededAtUnixMs,omitempty"`
}

type ProductQuery struct {
	Query    string
	Category string
	Page     int
	PageSize int
}

type ProductList struct {
	Products []Product `json:"products"`
	Total    int64     `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type OrderQuery struct {
	UserID   string
	Source   OrderSource
	Status   OrderStatus
	Query    string
	LotID    string
	Page     int
	PageSize int
}

type OrderList struct {
	Orders   []Order `json:"orders"`
	Total    int64   `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
}

type UserOrderList struct {
	Orders   []UserOrder `json:"orders"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
}

type FrequentStore struct {
	StoreKey          string      `json:"storeKey"`
	StoreName         string      `json:"storeName"`
	Source            OrderSource `json:"source"`
	OrderCount        int64       `json:"orderCount"`
	LastOrderAtUnixMs int64       `json:"lastOrderAtUnixMs"`
	ImageURL          string      `json:"imageUrl"`
	TargetURL         string      `json:"targetUrl"`
}

type FrequentStoreQuery struct {
	UserID string
	Limit  int
}

type FrequentStoreList struct {
	Stores []FrequentStore `json:"stores"`
	Total  int64           `json:"total"`
	Limit  int             `json:"limit"`
}

type CreateOrderRequest struct {
	SKUID           string `json:"skuId"`
	Quantity        int64  `json:"quantity"`
	AddressID       string `json:"addressId"`
	AddressSnapshot string `json:"addressSnapshot,omitempty"`
	IdempotencyKey  string `json:"idempotencyKey,omitempty"`
}

type MockPayRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type MockPayResult struct {
	Order   Order   `json:"order"`
	Payment Payment `json:"payment"`
	Paid    bool    `json:"paid"`
}

type UserOrderMockPayResult struct {
	Order   UserOrder        `json:"order"`
	Payment UserOrderPayment `json:"payment"`
	Paid    bool             `json:"paid"`
}

type Repository interface {
	ListProducts(ctx context.Context, query ProductQuery) (ProductList, error)
	FindProductByID(ctx context.Context, productID string) (*Product, error)
	ListDeliveryAddresses(ctx context.Context, userID string) ([]DeliveryAddress, error)
	FindDeliveryAddress(ctx context.Context, userID, addressID string) (*DeliveryAddress, error)
	CreateDeliveryAddress(ctx context.Context, userID string, input DeliveryAddressInput) (*DeliveryAddress, error)
	UpdateDeliveryAddress(ctx context.Context, userID, addressID string, input DeliveryAddressInput) (*DeliveryAddress, error)
	DeleteDeliveryAddress(ctx context.Context, userID, addressID string) error
	SetDefaultDeliveryAddress(ctx context.Context, userID, addressID string) ([]DeliveryAddress, error)
	CreateOrder(ctx context.Context, user UserRef, req CreateOrderRequest) (*Order, error)
	ListShopOrders(ctx context.Context, query OrderQuery) (OrderList, error)
	MockPayOrder(ctx context.Context, userID, orderID string, req MockPayRequest) (*MockPayResult, error)
	FindUserOrder(ctx context.Context, userID, orderID string) (*UserOrder, error)
	ListUserOrders(ctx context.Context, query OrderQuery) (UserOrderList, error)
	ListFrequentStores(ctx context.Context, query FrequentStoreQuery) (FrequentStoreList, error)
}

type UserRef struct {
	ID       string
	Nickname string
}

func NormalizePagination(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}
	return page, pageSize
}

func PageOffset(page, pageSize int) int {
	page, pageSize = NormalizePagination(page, pageSize)
	return (page - 1) * pageSize
}

func NormalizeFrequentStoreLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 20 {
		return 20
	}
	return limit
}

func ValidateCreateOrderRequest(req CreateOrderRequest) error {
	if strings.TrimSpace(req.SKUID) == "" {
		return fmt.Errorf("%w: sku id is required", apperr.ErrInvalidArgument)
	}
	if strings.TrimSpace(req.AddressID) == "" {
		return fmt.Errorf("%w: address id is required", apperr.ErrAddressRequired)
	}
	if req.Quantity <= 0 {
		return fmt.Errorf("%w: quantity must be greater than 0", apperr.ErrInvalidArgument)
	}
	if req.Quantity > 99 {
		return fmt.Errorf("%w: quantity is too large", apperr.ErrInvalidArgument)
	}
	return nil
}

func NormalizeDeliveryAddressInput(input DeliveryAddressInput) DeliveryAddressInput {
	input.ReceiverName = strings.TrimSpace(input.ReceiverName)
	if input.ReceiverName == "" {
		input.ReceiverName = strings.TrimSpace(input.Receiver)
	}
	input.Receiver = ""
	input.Phone = strings.TrimSpace(input.Phone)
	input.Province = strings.TrimSpace(input.Province)
	input.City = strings.TrimSpace(input.City)
	input.District = strings.TrimSpace(input.District)
	input.Street = strings.TrimSpace(input.Street)
	input.Detail = strings.TrimSpace(input.Detail)
	input.PostalCode = strings.TrimSpace(input.PostalCode)
	input.Tag = strings.TrimSpace(input.Tag)
	return input
}

func ValidateDeliveryAddressInput(input DeliveryAddressInput) error {
	input = NormalizeDeliveryAddressInput(input)
	if input.ReceiverName == "" {
		return fmt.Errorf("%w: receiver name is required", apperr.ErrInvalidArgument)
	}
	if input.Phone == "" {
		return fmt.Errorf("%w: phone is required", apperr.ErrInvalidArgument)
	}
	if input.Province == "" && input.City == "" && input.District == "" && input.Street == "" {
		return fmt.Errorf("%w: address region is required", apperr.ErrInvalidArgument)
	}
	if input.Detail == "" {
		return fmt.Errorf("%w: address detail is required", apperr.ErrInvalidArgument)
	}
	return nil
}

func (a DeliveryAddress) Snapshot() DeliveryAddressSnapshot {
	return DeliveryAddressSnapshot{
		AddressID:    a.ID,
		ReceiverName: a.ReceiverName,
		Phone:        a.Phone,
		Province:     a.Province,
		City:         a.City,
		District:     a.District,
		Street:       a.Street,
		Detail:       a.Detail,
		PostalCode:   a.PostalCode,
		FullAddress:  strings.Join([]string{a.Province, a.City, a.District, a.Street, a.Detail}, ""),
	}
}

func NowMs() int64 {
	return time.Now().UnixMilli()
}
