package auction

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestPresentationActionsNeverAdvanceRuntimeVersion(t *testing.T) {
	lot := presentationTestLot()
	runtimeVersion := lot.GetVersion()

	card, err := RevealTrustCard(lot, "card-1", 1_700_000_000_000)
	if err != nil {
		t.Fatalf("RevealTrustCard: %v", err)
	}
	if !card.GetRevealed() || lot.GetVersion() != runtimeVersion || lot.GetPresentationVersion() != 1 {
		t.Fatalf("reveal changed wrong version/state: lot=%+v card=%+v", lot, card)
	}
	if _, err := RevealTrustCard(lot, "card-1", 1_700_000_001_000); err != nil || lot.GetPresentationVersion() != 1 {
		t.Fatalf("idempotent reveal advanced presentation: version=%d error=%v", lot.GetPresentationVersion(), err)
	}

	ranking := []*v1.RankingItem{
		{UserId: "buyer-1", Nickname: "甲", Amount: &v1.Money{Amount: 12_000, Currency: "CNY"}},
		{UserId: "buyer-2", Nickname: "乙", Amount: &v1.Money{Amount: 11_000, Currency: "CNY"}},
	}
	if err := StartDuel(lot, ranking, 1_700_000_002_000, "buyer-1", "buyer-2"); err != nil {
		t.Fatalf("StartDuel: %v", err)
	}
	if lot.GetVersion() != runtimeVersion || lot.GetPresentationVersion() != 2 || !lot.GetDuelState().GetActive() {
		t.Fatalf("duel changed wrong version/state: %+v", lot)
	}
}

func TestOverlayLotPresentationPreservesRuntimeAuthority(t *testing.T) {
	lot := presentationTestLot()
	lot.Status = v1.LotStatus_LOT_STATUS_EXTENDED
	lot.PlaybookStage = v1.PlaybookStage_PLAYBOOK_STAGE_BIDDING_ACTIVE
	lot.DuelState = &v1.DuelState{LotId: lot.Id, EndsAtUnixMs: 2000, ExtendCount: 2, MaxExtendCount: 4}
	presentation := &v1.Lot{
		Id: lot.Id, MainAccountId: lot.MainAccountId, PresentationVersion: 7,
		TrustCards: []*v1.TrustRevealCard{{Id: "card-1", LotId: lot.Id, Revealed: true}},
		DuelState: &v1.DuelState{
			Active: true, LotId: lot.Id, UserAId: "buyer-1", UserBId: "buyer-2", StartedAtUnixMs: 1000,
			EndsAtUnixMs: 9999, ExtendCount: 99, MaxExtendCount: 99,
		},
		PlaybookStage: v1.PlaybookStage_PLAYBOOK_STAGE_DUEL_MODE,
	}

	if err := OverlayLotPresentation(lot, presentation); err != nil {
		t.Fatalf("OverlayLotPresentation: %v", err)
	}
	if lot.GetPresentationVersion() != 7 || !lot.GetTrustCards()[0].GetRevealed() || !lot.GetDuelState().GetActive() ||
		lot.GetDuelState().GetUserAId() != "buyer-1" || lot.GetDuelState().GetEndsAtUnixMs() != 2000 ||
		lot.GetDuelState().GetExtendCount() != 2 || lot.GetDuelState().GetMaxExtendCount() != 4 ||
		lot.GetPlaybookStage() != v1.PlaybookStage_PLAYBOOK_STAGE_DUEL_MODE {
		t.Fatalf("presentation/runtime merge mismatch: %+v", lot)
	}

	lot.Status = v1.LotStatus_LOT_STATUS_SETTLED
	lot.PlaybookStage = v1.PlaybookStage_PLAYBOOK_STAGE_SETTLE_READY
	if err := OverlayLotPresentation(lot, presentation); err != nil {
		t.Fatalf("terminal OverlayLotPresentation: %v", err)
	}
	if lot.GetDuelState().GetActive() || lot.GetPlaybookStage() != v1.PlaybookStage_PLAYBOOK_STAGE_SETTLE_READY {
		t.Fatalf("presentation overrode terminal runtime state: %+v", lot)
	}
}

func TestPresentationUsecasesPersistWithoutLotSaveAndRetryConflicts(t *testing.T) {
	base := presentationTestLot()
	repository := &lotRepositoryStub{lot: base}
	usecase := NewAuctionUsecase(repository, &bidRepositoryStub{}, nil, nil)
	usecase.runtime = &presentationRuntimeStub{}

	lot, card, err := usecase.RevealTrustCard(context.Background(), base.GetId(), base.GetMainAccountId(), "card-1", "operator-1")
	if err != nil {
		t.Fatalf("RevealTrustCard: %v", err)
	}
	if !card.GetRevealed() || lot.GetVersion() != 41 || lot.GetPresentationVersion() != 1 || repository.saveCalls != 0 ||
		repository.presentationSaveCalls != 1 || repository.expectedPresentationVersion != 0 || len(repository.events) != 1 {
		t.Fatalf("presentation persistence mismatch: lot=%+v repository=%+v", lot, repository)
	}

	duelLot := presentationTestLot()
	duelRepository := &lotRepositoryStub{lot: duelLot}
	bids := &bidRepositoryStub{lotBids: []*v1.Bid{
		{Id: "bid-1", UserId: "buyer-1", Nickname: "甲", Amount: &v1.Money{Amount: 12_000, Currency: "CNY"}, CreatedAtUnixMs: 2},
		{Id: "bid-2", UserId: "buyer-2", Nickname: "乙", Amount: &v1.Money{Amount: 11_000, Currency: "CNY"}, CreatedAtUnixMs: 1},
	}}
	duelUsecase := NewAuctionUsecase(duelRepository, bids, nil, nil)
	duelUsecase.runtime = &presentationRuntimeStub{ranking: BuildRealtimeRanking(bids.lotBids)}
	started, duel, err := duelUsecase.StartDuel(context.Background(), duelLot.GetId(), duelLot.GetMainAccountId(), "operator-1", "buyer-1", "buyer-2")
	if err != nil {
		t.Fatalf("StartDuel: %v", err)
	}
	if !duel.GetActive() || started.GetVersion() != 41 || started.GetPresentationVersion() != 1 ||
		duelRepository.saveCalls != 0 || duelRepository.presentationSaveCalls != 1 {
		t.Fatalf("duel persistence mismatch: lot=%+v repository=%+v", started, duelRepository)
	}

	retrying := &conflictingPresentationRepository{lotRepositoryStub: &lotRepositoryStub{lot: presentationTestLot()}}
	retryUsecase := NewAuctionUsecase(retrying, &bidRepositoryStub{}, nil, nil)
	retryUsecase.runtime = &presentationRuntimeStub{}
	if _, _, err := retryUsecase.RevealTrustCard(context.Background(), base.GetId(), base.GetMainAccountId(), "card-1", "operator-1"); err != nil {
		t.Fatalf("conflicting reveal did not retry: %v", err)
	}
	if retrying.findCalls != 2 || retrying.presentationCalls != 2 {
		t.Fatalf("retry calls find/save=%d/%d want 2/2", retrying.findCalls, retrying.presentationCalls)
	}
}

func TestCommittedPresentationWriteSurvivesRealtimePublishFailure(t *testing.T) {
	repository := &lotRepositoryStub{lot: presentationTestLot()}
	publisher := &presentationPublisherStub{err: errors.New("nats unavailable")}
	usecase := NewAuctionUsecase(repository, &bidRepositoryStub{}, nil, publisher)
	usecase.runtime = &presentationRuntimeStub{}

	lot, card, err := usecase.RevealTrustCard(context.Background(), "lot-1", "main-1", "card-1", "operator-1")
	if err != nil || !card.GetRevealed() || lot.GetPresentationVersion() != 1 {
		t.Fatalf("committed presentation must survive realtime failure: lot=%+v card=%+v error=%v", lot, card, err)
	}
	if repository.presentationSaveCalls != 1 || publisher.calls != 1 {
		t.Fatalf("save/publish calls=%d/%d", repository.presentationSaveCalls, publisher.calls)
	}
}

type presentationPublisherStub struct {
	calls int
	err   error
}

func (publisher *presentationPublisherStub) Publish(context.Context, *v1.AuctionEvent) error {
	publisher.calls++
	return publisher.err
}

type conflictingPresentationRepository struct {
	*lotRepositoryStub
	findCalls         int
	presentationCalls int
}

type presentationRuntimeStub struct {
	ranking []*v1.RankingItem
}

func (*presentationRuntimeStub) ActiveRuntimeLotID(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (*presentationRuntimeStub) PlaceBidRuntime(context.Context, *v1.Lot, *v1.PlaceBidRequest, string, string, string, string, int64) (RuntimeBidResult, error) {
	return RuntimeBidResult{}, errors.New("not implemented")
}

func (runtime *presentationRuntimeStub) SnapshotRuntime(_ context.Context, current *v1.Lot) (*v1.RoomSnapshot, error) {
	return &v1.RoomSnapshot{
		RoomId: current.GetRoomId(), CurrentLot: proto.Clone(current).(*v1.Lot), Ranking: runtime.ranking,
	}, nil
}

func (runtime *presentationRuntimeStub) RankingRuntime(context.Context, string, int64) ([]*v1.RankingItem, error) {
	return runtime.ranking, nil
}

func (repository *conflictingPresentationRepository) FindByID(_ context.Context, lotID string) (*v1.Lot, error) {
	repository.findCalls++
	if repository.lot == nil || repository.lot.GetId() != lotID {
		return nil, apperr.ErrNotFound
	}
	return proto.Clone(repository.lot).(*v1.Lot), nil
}

func (repository *conflictingPresentationRepository) SaveLotPresentation(_ context.Context, _ *v1.Lot, _ int64, _ []*v1.AuctionEvent) error {
	repository.presentationCalls++
	if repository.presentationCalls == 1 {
		return apperr.ErrLotVersionConflict
	}
	return nil
}

func presentationTestLot() *v1.Lot {
	return &v1.Lot{
		Id: "lot-1", RoomId: "room-1", MainAccountId: "main-1", Status: v1.LotStatus_LOT_STATUS_LIVE,
		Version: 41, EndsAtUnixMs: 1_800_000_000_000, Rule: completeBidRule(),
		TrustCards: []*v1.TrustRevealCard{{Id: "card-1", LotId: "lot-1", Title: "证书"}},
		DuelState:  &v1.DuelState{LotId: "lot-1", EndsAtUnixMs: 1_800_000_000_000, MaxExtendCount: 3},
	}
}

func TestOverlayLotPresentationRejectsInvalidIdentity(t *testing.T) {
	lot := presentationTestLot()
	if err := OverlayLotPresentation(nil, nil); err == nil {
		t.Fatal("nil presentation was accepted")
	}
	if err := OverlayLotPresentation(lot, &v1.Lot{Id: "other", MainAccountId: lot.MainAccountId, PresentationVersion: 1}); err == nil {
		t.Fatal("identity mismatch was accepted")
	}
	if err := OverlayLotPresentation(lot, &v1.Lot{Id: lot.Id, MainAccountId: lot.MainAccountId}); err == nil {
		t.Fatal("zero presentation version was accepted")
	}
}
