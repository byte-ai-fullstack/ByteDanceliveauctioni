package auction

import (
	"context"
	"errors"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/biz/shop"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

// LotRepository 管理拍品聚合持久化。
// biz 只依赖接口，不关心内存、MySQL 或其他存储实现。
type LotRepository interface {
	Create(ctx context.Context, lot *v1.Lot, ownerUserID string, events []*v1.AuctionEvent) error
	Save(ctx context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error
	QueueLotAsNext(ctx context.Context, lotID, mainAccountID, ownerUserID string, nowMs int64) (*v1.Lot, int32, []*v1.AuctionEvent, error)
	AttachAssets(ctx context.Context, ownerUserID string, lot *v1.Lot) error
	FindByID(ctx context.Context, lotID string) (*v1.Lot, error)
	FindCoreByID(ctx context.Context, lotID string) (*v1.Lot, error)
	List(ctx context.Context, roomID string, status v1.LotStatus) ([]*v1.Lot, error)
	ListLots(ctx context.Context, query LotQuery) (LotList, error)
	FindRoomState(ctx context.Context, roomID, mainAccountID string) (*RoomState, error)
}

// LotBatchRepository is a read-only capability used after search candidate retrieval.
// Implementations must preserve input order and hydrate all IDs without per-lot queries.
type LotBatchRepository interface {
	FindByIDs(ctx context.Context, lotIDs []string) ([]*v1.Lot, error)
}

// LotPresentationRepository persists live presentation state independently
// from auction_lots.version. Redis Lua and Projector remain the only writers
// of the adjudication version and runtime columns.
type LotPresentationRepository interface {
	SaveLotPresentation(ctx context.Context, lot *v1.Lot, expectedPresentationVersion int64, events []*v1.AuctionEvent) error
}

type RoomRepository interface {
	EnsureDefaultRoom(ctx context.Context, mainAccountID, createdByUserID string, nowMs int64) (*Room, error)
	ListRooms(ctx context.Context, query RoomQuery) ([]Room, error)
	FindRoomByID(ctx context.Context, roomID string) (*Room, bool, error)
}

// BidRepository reads the Kafka-projected bid history and optionally warms the
// Redis replay cache. It never commits an accepted bid; Redis Lua owns that
// decision and Projector owns its MySQL transaction.
type BidRepository interface {
	ListByLot(ctx context.Context, lotID string) ([]*v1.Bid, error)
	ListBidRecordsByBuyer(ctx context.Context, buyerUserID string, query BidRecordQuery) (BidRecordList, error)
	FindByIdempotencyKey(ctx context.Context, lotID, userID, key string) (*v1.Bid, bool, error)
	CacheIdempotencyKey(ctx context.Context, lotID, userID, key string, bid *v1.Bid)
}

type RuntimeBidResult struct {
	Lot                *v1.Lot
	Bid                *v1.Bid
	Ranking            []*v1.RankingItem
	RecentBids         []*v1.Bid
	PreviousLeaderID   string
	EndsBeforeBid      int64
	ExtendCountBefore  int32
	RuntimeEventID     string
	PreviousLotVersion int64
	LotVersion         int64
	OrderID            string
	Replayed           bool
}

type RuntimeBidRejectError struct {
	Code               string
	CurrentAmount      int64
	CurrentCurrency    string
	MinIncrementAmount int64
	NextBidAmount      int64
	LeadingUserID      string
	LeadingNickname    string
	LotVersion         int64
	EndsAtUnixMs       int64
	Cause              error
}

func (e *RuntimeBidRejectError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return e.Code
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(apperr.CodeBidRejected)
}

func (e *RuntimeBidRejectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func RuntimeBidRejectFromError(err error) (*RuntimeBidRejectError, bool) {
	var reject *RuntimeBidRejectError
	if errors.As(err, &reject) {
		return reject, true
	}
	return nil, false
}

func (e *RuntimeBidRejectError) Lot(lotID string, fallbackAmount *v1.Money) *v1.Lot {
	if e == nil {
		return &v1.Lot{Id: lotID}
	}
	currency := e.CurrentCurrency
	if currency == "" && fallbackAmount != nil {
		currency = fallbackAmount.GetCurrency()
	}
	lot := &v1.Lot{
		Id:              lotID,
		CurrentPrice:    &v1.Money{Amount: e.CurrentAmount, Currency: currency},
		LeadingUserId:   e.LeadingUserID,
		LeadingNickname: e.LeadingNickname,
		EndsAtUnixMs:    e.EndsAtUnixMs,
		Version:         e.LotVersion,
		Rule: &v1.BidRule{
			MinIncrement: &v1.Money{Amount: e.MinIncrementAmount, Currency: currency},
		},
	}
	switch apperr.BusinessCode(e.Code) {
	case apperr.CodeLotCancelled:
		lot.Status = v1.LotStatus_LOT_STATUS_CANCELLED
	case apperr.CodeBidEnded:
		lot.Status = v1.LotStatus_LOT_STATUS_SETTLED
	}
	return lot
}

type AuctionRuntime interface {
	ActiveRuntimeLotID(ctx context.Context, roomID string) (lotID string, found bool, err error)
	PlaceBidRuntime(ctx context.Context, lot *v1.Lot, req *v1.PlaceBidRequest, bidderID, nickname, avatarURL, bidID string, nowMs int64) (RuntimeBidResult, error)
	SnapshotRuntime(ctx context.Context, current *v1.Lot) (*v1.RoomSnapshot, error)
	RankingRuntime(ctx context.Context, lotID string, limit int64) ([]*v1.RankingItem, error)
}

// AuctionRuntimeDisplay separates the lot shown in snapshots from the lot that
// is still allowed to accept commands. Terminal commands release the active
// pointer but retain this display pointer for bounded recovery.
type AuctionRuntimeDisplay interface {
	DisplayedRuntimeLotID(ctx context.Context, roomID string) (lotID string, found bool, err error)
}

// RuntimeCommandRepository is the clean-cut lifecycle path whose accepted results are projected through Kafka.
type RuntimeCommandRepository interface {
	ExecuteStartLot(ctx context.Context, lot *v1.Lot, traceID string) (RuntimeStartResult, error)
	ExecuteCancelLot(ctx context.Context, lotID, reason, operatorID, traceID string) (*v1.RuntimeFactV1, error)
	ExecuteCloseIfExpired(ctx context.Context, lotID, orderID, traceID string) (*v1.RuntimeFactV1, error)
	ExecuteSyncLotConfig(ctx context.Context, lot *v1.Lot, expectedConfigVersion int64, traceID string) (*v1.RuntimeFactV1, error)
}

// RuntimeStartResult binds the exact MySQL configuration row serialized by
// the start command to the Redis fact produced while that row lock was held.
type RuntimeStartResult struct {
	SourceLot *v1.Lot
	Fact      *v1.RuntimeFactV1
}

type OrderRepository interface {
	FindOrderByID(ctx context.Context, orderID string) (*Order, error)
	FindOrderByLot(ctx context.Context, lotID string) (*Order, bool, error)
	ListOrdersByBuyer(ctx context.Context, buyerUserID string) ([]Order, error)
	ListOrders(ctx context.Context, query OrderQuery) (OrderList, error)
}

type PaymentRepository interface {
	FindPaymentByIdempotencyKey(ctx context.Context, orderID, key string) (*Payment, bool, error)
	CommitPaymentSuccess(ctx context.Context, payment Payment, order Order, expectedOrderVersion int64, events []*v1.AuctionEvent) error
}

type DepositRepository interface {
	FindDepositHoldByLotBuyer(ctx context.Context, lotID, buyerUserID string) (*DepositHold, bool, error)
	FindDepositHoldByIdempotencyKey(ctx context.Context, lotID, buyerUserID, key string) (*DepositHold, bool, error)
	CommitDepositHold(ctx context.Context, hold DepositHold) (*DepositHold, error)
}

type DeliveryAddressRepository interface {
	FindDeliveryAddress(ctx context.Context, userID, addressID string) (*shop.DeliveryAddress, error)
}

// EventRepository 持久化不伴随聚合状态更新的领域事件。
// 伴随 lot/bid 状态变化的事件必须随对应 repository 方法进入同一个 MySQL 事务。
type EventRepository interface {
	PersistEvents(ctx context.Context, events []*v1.AuctionEvent) error
	ListRoomEvents(ctx context.Context, query RoomEventQuery) (RoomEventList, error)
}

// EventPublisher 发布领域事件。
type EventPublisher interface {
	Publish(ctx context.Context, event *v1.AuctionEvent) error
}
