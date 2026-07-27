package auction

import (
	"context"
	"errors"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestValidateRoomInMainAccount(t *testing.T) {
	repositoryError := errors.New("room lookup failed")
	tests := []struct {
		name          string
		usecase       *AuctionUsecase
		roomID        string
		mainAccountID string
		wantError     error
	}{
		{name: "repository required", usecase: &AuctionUsecase{}, roomID: "room-1", mainAccountID: "main-1", wantError: errors.New("room repository is required")},
		{name: "room required", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{}}, mainAccountID: "main-1", wantError: apperr.ErrInvalidArgument},
		{name: "main account required", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{}}, roomID: "room-1", wantError: apperr.ErrPermissionDenied},
		{name: "lookup error", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{findError: repositoryError}}, roomID: "room-1", mainAccountID: "main-1", wantError: repositoryError},
		{name: "not found", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{}}, roomID: "room-1", mainAccountID: "main-1", wantError: apperr.ErrNotFound},
		{name: "wrong account", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{room: &Room{ID: "room-1", MainAccountID: "main-2"}, found: true}}, roomID: "room-1", mainAccountID: "main-1", wantError: apperr.ErrPermissionDenied},
		{name: "disabled", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{room: &Room{ID: "room-1", MainAccountID: "main-1", Status: RoomStatusDisabled}, found: true}}, roomID: "room-1", mainAccountID: "main-1", wantError: apperr.ErrInvalidArgument},
		{name: "active", usecase: &AuctionUsecase{rooms: &roomRepositoryStub{room: &Room{ID: "room-1", MainAccountID: "main-1", Status: RoomStatusActive}, found: true}}, roomID: " room-1 ", mainAccountID: " main-1 "},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.usecase.ValidateRoomInMainAccount(context.Background(), test.roomID, test.mainAccountID)
			if test.wantError == nil {
				if err != nil {
					t.Fatalf("ValidateRoomInMainAccount returned error: %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantError) && err.Error() != test.wantError.Error() {
				t.Fatalf("ValidateRoomInMainAccount error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestFindActiveRoom(t *testing.T) {
	active := &Room{ID: "room-1", Status: RoomStatusActive}
	uc := &AuctionUsecase{rooms: &roomRepositoryStub{room: active, found: true}}

	room, err := uc.findActiveRoom(context.Background(), " room-1 ")
	if err != nil {
		t.Fatalf("findActiveRoom returned error: %v", err)
	}
	if room != active {
		t.Fatalf("findActiveRoom returned unexpected room: %+v", room)
	}

	uc.rooms = &roomRepositoryStub{room: &Room{ID: "room-2", Status: RoomStatusDisabled}, found: true}
	if _, err := uc.findActiveRoom(context.Background(), "room-2"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("disabled room error = %v, want not found", err)
	}
	if _, err := uc.findActiveRoom(context.Background(), " "); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("empty room error = %v, want invalid argument", err)
	}
}

func TestRoomRepositoryDelegation(t *testing.T) {
	defaultRoom := &Room{ID: "room-default", MainAccountID: "main-1", Status: RoomStatusActive}
	repository := &roomRepositoryStub{
		ensuredRoom: defaultRoom,
		listedRooms: []Room{*defaultRoom},
	}
	uc := &AuctionUsecase{rooms: repository}

	room, err := uc.EnsureDefaultRoom(context.Background(), " main-1 ", " owner-1 ")
	if err != nil || room != defaultRoom {
		t.Fatalf("EnsureDefaultRoom mismatch: room=%+v err=%v", room, err)
	}
	rooms, err := uc.ListRooms(context.Background(), RoomQuery{MainAccountID: "main-1"})
	if err != nil || len(rooms) != 1 || rooms[0].ID != defaultRoom.ID {
		t.Fatalf("ListRooms mismatch: rooms=%+v err=%v", rooms, err)
	}

	if _, err := (&AuctionUsecase{}).EnsureDefaultRoom(context.Background(), "main-1", "owner-1"); err == nil {
		t.Fatal("missing room repository should fail")
	}
	if _, err := uc.EnsureDefaultRoom(context.Background(), "", "owner-1"); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("empty main account error = %v, want permission denied", err)
	}
	if _, err := (&AuctionUsecase{}).ListRooms(context.Background(), RoomQuery{}); err == nil {
		t.Fatal("missing room repository should fail list")
	}
}

func TestGetLotAndFindLotsByIDs(t *testing.T) {
	lot := &v1.Lot{Id: "lot-1", RoomId: "room-1"}
	lots := &lotRepositoryStub{lot: lot}
	uc := &AuctionUsecase{
		lots:  lots,
		rooms: &roomRepositoryStub{room: &Room{ID: "room-1", Status: RoomStatusActive}, found: true},
	}

	got, err := uc.GetLot(context.Background(), "lot-1")
	if err != nil || got != lot {
		t.Fatalf("GetLot mismatch: lot=%+v err=%v", got, err)
	}
	if _, err := uc.GetLot(context.Background(), ""); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("empty lot error = %v, want invalid argument", err)
	}
	found, err := uc.FindLotsByIDs(context.Background(), []string{" lot-1 ", "lot-1"})
	if err != nil || len(found) != 1 || found[0] != lot {
		t.Fatalf("FindLotsByIDs fallback mismatch: lots=%+v err=%v", found, err)
	}
	if _, err := (*AuctionUsecase)(nil).FindLotsByIDs(context.Background(), []string{"lot-1"}); err == nil {
		t.Fatal("nil usecase should fail batch lookup")
	}

	batch := &batchLotRepositoryStub{lotRepositoryStub: lots}
	batchUsecase := NewAuctionUsecase(batch, nil, nil, nil)
	found, err = batchUsecase.FindLotsByIDs(context.Background(), []string{"lot-1", "lot-2"})
	if err != nil || len(found) != 1 || len(batch.ids) != 2 {
		t.Fatalf("FindLotsByIDs batch mismatch: lots=%+v ids=%v err=%v", found, batch.ids, err)
	}
}

func TestSetPaymentProvider(t *testing.T) {
	uc := &AuctionUsecase{}
	provider := NewMockPaymentProvider()
	if got := uc.SetPaymentProvider(provider); got != uc || got.paymentProvider.Name() != "mock" {
		t.Fatalf("SetPaymentProvider did not preserve fluent usecase: %+v", got)
	}
}

func TestCreateLotDraftUsesDefaultRoom(t *testing.T) {
	repository := &lotRepositoryStub{}
	uc := &AuctionUsecase{
		lots:  repository,
		rooms: &roomRepositoryStub{ensuredRoom: &Room{ID: "room-default", MainAccountID: "main-1", Status: RoomStatusActive}},
	}

	created, err := uc.CreateLotDraft(context.Background(), &v1.CreateLotRequest{Stock: 0}, " main-1 ", "owner-1")
	if err != nil {
		t.Fatalf("CreateLotDraft returned error: %v", err)
	}
	if created.GetRoomId() != "room-default" || created.GetMainAccountId() != "main-1" || created.GetStatus() != v1.LotStatus_LOT_STATUS_DRAFT {
		t.Fatalf("created draft mismatch: %+v", created)
	}
	if repository.createdLot == nil || repository.createdByUserID != "owner-1" {
		t.Fatalf("repository call mismatch: lot=%+v owner=%q", repository.createdLot, repository.createdByUserID)
	}
	if created == repository.createdLot {
		t.Fatal("CreateLotDraft must return a clone, not the repository-owned message")
	}
}

func TestPatchLotDraftPersistsValidatedVersion(t *testing.T) {
	lot := completeDraftLot()
	repository := &lotRepositoryStub{lot: lot}
	uc := &AuctionUsecase{
		lots:  repository,
		rooms: &roomRepositoryStub{room: &Room{ID: lot.GetRoomId(), MainAccountID: lot.GetMainAccountId(), Status: RoomStatusActive}, found: true},
	}

	patched, err := uc.PatchLotDraft(context.Background(), &v1.PatchLotDraftRequest{LotId: lot.GetId(), Title: "patched"}, " main-1 ", "owner-1")
	if err != nil {
		t.Fatalf("PatchLotDraft returned error: %v", err)
	}
	if patched.GetTitle() != "patched" || patched.GetVersion() != 2 {
		t.Fatalf("patched draft mismatch: %+v", patched)
	}
	if repository.saveCalls != 1 || repository.expectedVersion != 1 || repository.savedLot.GetVersion() != 2 {
		t.Fatalf("save call mismatch: calls=%d expected=%d lot=%+v", repository.saveCalls, repository.expectedVersion, repository.savedLot)
	}
	if patched == repository.savedLot {
		t.Fatal("PatchLotDraft must return a clone, not the repository-owned message")
	}
}

func TestEnsureLotMainAccount(t *testing.T) {
	if err := ensureLotMainAccount(nil, "main-1"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("nil lot error = %v, want not found", err)
	}
	lot := &v1.Lot{MainAccountId: "main-1"}
	if err := ensureLotMainAccount(lot, "main-2"); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("foreign lot error = %v, want permission denied", err)
	}
	if err := ensureLotMainAccount(lot, " main-1 "); err != nil {
		t.Fatalf("matching account returned error: %v", err)
	}
}

func TestAuctionUsecaseQueryDelegation(t *testing.T) {
	orderRepository := &orderRepositoryStub{result: OrderList{Total: 1}}
	eventRepository := &eventRepositoryStub{result: RoomEventList{NextPageToken: "next"}}
	bidRepository := &bidRepositoryStub{result: BidRecordList{Total: 2}}
	uc := &AuctionUsecase{orders: orderRepository, eventsStore: eventRepository, bids: bidRepository}

	orders, err := uc.ListOrdersByBuyerQuery(context.Background(), "buyer-1", OrderQuery{PageSize: 500, Buyer: "ignored"})
	if err != nil {
		t.Fatalf("ListOrdersByBuyerQuery returned error: %v", err)
	}
	if orders.Total != 1 || orderRepository.query.BuyerUserID != "buyer-1" || orderRepository.query.Buyer != "" || orderRepository.query.Page != 1 || orderRepository.query.PageSize != 100 {
		t.Fatalf("normalized order query mismatch: %+v, result: %+v", orderRepository.query, orders)
	}

	events, err := uc.ListRoomEvents(context.Background(), RoomEventQuery{RoomID: "room-1"})
	if err != nil || events.NextPageToken != "next" || eventRepository.query.RoomID != "room-1" {
		t.Fatalf("ListRoomEvents mismatch: result=%+v query=%+v err=%v", events, eventRepository.query, err)
	}

	bids, err := uc.ListBidRecordsByBuyer(context.Background(), "buyer-1", BidRecordQuery{Page: -1, PageSize: 500})
	if err != nil {
		t.Fatalf("ListBidRecordsByBuyer returned error: %v", err)
	}
	if bids.Total != 2 || bidRepository.query.Page != 1 || bidRepository.query.PageSize != 100 || bidRepository.buyerUserID != "buyer-1" {
		t.Fatalf("normalized bid query mismatch: %+v, result: %+v", bidRepository.query, bids)
	}
}

func TestAuctionUsecaseQueryValidation(t *testing.T) {
	uc := &AuctionUsecase{}
	if _, err := uc.ListOrdersByBuyerQuery(context.Background(), "", OrderQuery{}); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("empty buyer order error = %v, want invalid argument", err)
	}
	if _, err := uc.ListRoomEvents(context.Background(), RoomEventQuery{}); err == nil {
		t.Fatal("empty room event query should fail")
	}
	if _, err := uc.ListRoomEvents(context.Background(), RoomEventQuery{RoomID: "room-1"}); err == nil {
		t.Fatal("missing event repository should fail")
	}
	if _, err := uc.ListBidRecordsByBuyer(context.Background(), "", BidRecordQuery{}); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("empty buyer bid error = %v, want invalid argument", err)
	}
}

func TestGetMyDepositHoldAndBidderAvatarURL(t *testing.T) {
	hold := &DepositHold{LotID: "lot-1", BuyerUserID: "buyer-1", Status: DepositStatusHeld}
	uc := &AuctionUsecase{deposits: depositHoldRepoStub{hold: hold, found: true}}
	got, found, err := uc.GetMyDepositHold(context.Background(), " lot-1 ", " buyer-1 ")
	if err != nil || !found || got != hold {
		t.Fatalf("GetMyDepositHold mismatch: hold=%+v found=%v err=%v", got, found, err)
	}
	if _, _, err := uc.GetMyDepositHold(context.Background(), "", "buyer-1"); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("empty lot error = %v, want invalid argument", err)
	}
	if _, _, err := uc.GetMyDepositHold(context.Background(), "lot-1", ""); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Fatalf("empty buyer error = %v, want unauthenticated", err)
	}
	if _, _, err := (&AuctionUsecase{}).GetMyDepositHold(context.Background(), "lot-1", "buyer-1"); err == nil {
		t.Fatal("missing deposit repository should fail")
	}

	if got := bidderAvatarURL("buyer-1", " ", " https://example.com/avatar.png "); got != "https://example.com/avatar.png" {
		t.Fatalf("explicit avatar URL = %q", got)
	}
	if got := bidderAvatarURL("buyer-1"); got == "" {
		t.Fatal("fallback avatar URL should not be empty")
	}
}

type roomRepositoryStub struct {
	room        *Room
	found       bool
	findError   error
	ensuredRoom *Room
	listedRooms []Room
}

type lotRepositoryStub struct {
	lot                         *v1.Lot
	createdLot                  *v1.Lot
	createdByUserID             string
	savedLot                    *v1.Lot
	expectedVersion             int64
	events                      []*v1.AuctionEvent
	saveCalls                   int
	presentationSaveCalls       int
	expectedPresentationVersion int64
}

func (repository *lotRepositoryStub) Create(_ context.Context, lot *v1.Lot, ownerUserID string, events []*v1.AuctionEvent) error {
	repository.createdLot = lot
	repository.createdByUserID = ownerUserID
	repository.events = events
	return nil
}

func (repository *lotRepositoryStub) Save(_ context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error {
	repository.saveCalls++
	repository.savedLot = lot
	repository.expectedVersion = expectedVersion
	repository.events = events
	return nil
}

func (repository *lotRepositoryStub) SaveLotPresentation(_ context.Context, lot *v1.Lot, expectedVersion int64, events []*v1.AuctionEvent) error {
	repository.presentationSaveCalls++
	repository.savedLot = lot
	repository.expectedPresentationVersion = expectedVersion
	repository.events = events
	return nil
}

func (*lotRepositoryStub) QueueLotAsNext(context.Context, string, string, string, int64) (*v1.Lot, int32, []*v1.AuctionEvent, error) {
	return nil, 0, nil, errors.New("not implemented")
}

func (*lotRepositoryStub) AttachAssets(context.Context, string, *v1.Lot) error { return nil }

func (repository *lotRepositoryStub) FindByID(_ context.Context, lotID string) (*v1.Lot, error) {
	if repository.lot == nil || repository.lot.GetId() != lotID {
		return nil, errors.New("lot not found")
	}
	return repository.lot, nil
}

func (repository *lotRepositoryStub) FindCoreByID(ctx context.Context, lotID string) (*v1.Lot, error) {
	return repository.FindByID(ctx, lotID)
}

func (*lotRepositoryStub) List(context.Context, string, v1.LotStatus) ([]*v1.Lot, error) {
	return nil, errors.New("not implemented")
}

func (*lotRepositoryStub) ListLots(context.Context, LotQuery) (LotList, error) {
	return LotList{}, errors.New("not implemented")
}

func (*lotRepositoryStub) FindRoomState(context.Context, string, string) (*RoomState, error) {
	return nil, errors.New("not implemented")
}

func (repository *roomRepositoryStub) EnsureDefaultRoom(context.Context, string, string, int64) (*Room, error) {
	return repository.ensuredRoom, nil
}

func (repository *roomRepositoryStub) ListRooms(context.Context, RoomQuery) ([]Room, error) {
	return repository.listedRooms, nil
}

func (repository *roomRepositoryStub) FindRoomByID(context.Context, string) (*Room, bool, error) {
	return repository.room, repository.found, repository.findError
}

type orderRepositoryStub struct {
	query  OrderQuery
	result OrderList
}

func (*orderRepositoryStub) FindOrderByID(context.Context, string) (*Order, error) {
	return nil, errors.New("not implemented")
}

func (*orderRepositoryStub) FindOrderByLot(context.Context, string) (*Order, bool, error) {
	return nil, false, errors.New("not implemented")
}

func (*orderRepositoryStub) ListOrdersByBuyer(context.Context, string) ([]Order, error) {
	return nil, errors.New("not implemented")
}

func (repository *orderRepositoryStub) ListOrders(_ context.Context, query OrderQuery) (OrderList, error) {
	repository.query = query
	return repository.result, nil
}

type eventRepositoryStub struct {
	query  RoomEventQuery
	result RoomEventList
}

func (*eventRepositoryStub) PersistEvents(context.Context, []*v1.AuctionEvent) error {
	return errors.New("not implemented")
}

func (repository *eventRepositoryStub) ListRoomEvents(_ context.Context, query RoomEventQuery) (RoomEventList, error) {
	repository.query = query
	return repository.result, nil
}

type bidRepositoryStub struct {
	buyerUserID string
	query       BidRecordQuery
	result      BidRecordList
	lotBids     []*v1.Bid
	listError   error
}

func (repository *bidRepositoryStub) ListByLot(context.Context, string) ([]*v1.Bid, error) {
	return repository.lotBids, repository.listError
}

func (repository *bidRepositoryStub) ListBidRecordsByBuyer(_ context.Context, buyerUserID string, query BidRecordQuery) (BidRecordList, error) {
	repository.buyerUserID = buyerUserID
	repository.query = query
	return repository.result, nil
}

func (*bidRepositoryStub) FindByIdempotencyKey(context.Context, string, string, string) (*v1.Bid, bool, error) {
	return nil, false, errors.New("not implemented")
}

func (*bidRepositoryStub) CacheIdempotencyKey(context.Context, string, string, string, *v1.Bid) {}

type batchLotRepositoryStub struct {
	*lotRepositoryStub
	ids []string
}

func (repository *batchLotRepositoryStub) FindByIDs(_ context.Context, ids []string) ([]*v1.Lot, error) {
	repository.ids = append([]string(nil), ids...)
	return []*v1.Lot{repository.lot}, nil
}
