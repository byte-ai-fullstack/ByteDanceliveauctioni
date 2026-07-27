package test

import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/aiassistant"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"
)

func TestBuyerHybridRetrievalRunsElasticsearchAndVectorInParallel(t *testing.T) {
	store := newTestStore()
	store.rooms["room-public"] = auction.Room{
		ID: "room-public", MainAccountID: testMainAccountID, Name: "翡翠专场", Platform: "douyin", Status: auction.RoomStatusActive,
	}
	store.lots["lot-1"] = aiTestLot("lot-1", "room-public", "冰糯翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	release := make(chan struct{})
	keyword := &blockingKeywordSearch{started: make(chan struct{}), release: release, documents: []searchindex.LotDocument{{LotID: "lot-1", RoomID: "room-public"}}}
	vector := &blockingVectorSearch{started: make(chan struct{}), release: release, documents: []searchindex.LotDocument{{LotID: "lot-1", RoomID: "room-public"}}}
	svc := appsvc.NewAuctionService(uc).
		SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"})).
		SetBuyerSearch(keyword, vector, staticEmbeddingClient{})

	type consultResult struct {
		reply *v1.BuyerConsultReply
		err   error
	}
	done := make(chan consultResult, 1)
	go func() {
		reply, err := svc.ConsultBuyer(context.Background(), &v1.BuyerConsultRequest{Query: "翡翠手镯"})
		done <- consultResult{reply: reply, err: err}
	}()

	for name, started := range map[string]<-chan struct{}{"elasticsearch": keyword.started, "pgvector": vector.started} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s retrieval did not start while the peer was blocked", name)
		}
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil || result.reply == nil || len(result.reply.Results) != 1 || result.reply.Results[0].GetLotId() != "lot-1" {
			t.Fatalf("reply=%+v error=%v", result.reply, result.err)
		}
		if store.batchFindCalls != 1 || store.singleFindCalls != 0 {
			t.Fatalf("batch calls=%d single calls=%d", store.batchFindCalls, store.singleFindCalls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hybrid buyer consultation did not finish")
	}
}

func TestBuyerHybridRetrievalUsesHealthySideWhenPeerFails(t *testing.T) {
	for _, test := range []struct {
		name       string
		keywordErr error
		vectorErr  error
	}{
		{name: "Elasticsearch failed", keywordErr: errors.New("Elasticsearch unavailable")},
		{name: "pgvector failed", vectorErr: errors.New("pgvector unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore()
			store.rooms["room-public"] = auction.Room{
				ID: "room-public", MainAccountID: testMainAccountID, Name: "翡翠专场", Platform: "douyin", Status: auction.RoomStatusActive,
			}
			store.lots["lot-1"] = aiTestLot("lot-1", "room-public", "冰糯翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
			uc := auction.NewAuctionUsecase(store, store, store, nil)
			documents := []searchindex.LotDocument{{LotID: "lot-1", RoomID: "room-public"}}
			svc := appsvc.NewAuctionService(uc).
				SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"})).
				SetBuyerSearch(
					staticKeywordSearch{documents: documents, err: test.keywordErr},
					staticVectorSearch{documents: documents, err: test.vectorErr},
					staticEmbeddingClient{},
				)
			reply, err := svc.ConsultBuyer(context.Background(), &v1.BuyerConsultRequest{Query: "翡翠手镯"})
			if err != nil || reply == nil || len(reply.Results) != 1 || reply.Results[0].GetLotId() != "lot-1" {
				t.Fatalf("reply=%+v error=%v", reply, err)
			}
		})
	}
}

func TestBuyerHybridRetrievalFallsBackWhenIndexedCandidatesAreStale(t *testing.T) {
	store := newTestStore()
	store.rooms["room-public"] = auction.Room{
		ID: "room-public", MainAccountID: testMainAccountID, Name: "翡翠专场", Platform: "douyin", Status: auction.RoomStatusActive,
	}
	store.lots["lot-1"] = aiTestLot("lot-1", "room-public", "冰糯翡翠手镯", v1.LotStatus_LOT_STATUS_LIVE)
	uc := auction.NewAuctionUsecase(store, store, store, nil)
	stale := []searchindex.LotDocument{{LotID: "missing-lot"}}
	svc := appsvc.NewAuctionService(uc).
		SetAIAssistant(aiassistant.New(aiassistant.Config{Provider: "mock"})).
		SetBuyerSearch(staticKeywordSearch{documents: stale}, staticVectorSearch{documents: stale}, staticEmbeddingClient{})

	reply, err := svc.ConsultBuyer(context.Background(), &v1.BuyerConsultRequest{Query: "翡翠手镯"})
	if err != nil || reply == nil || len(reply.Results) != 1 || reply.Results[0].GetLotId() != "lot-1" {
		t.Fatalf("reply=%+v error=%v", reply, err)
	}
}

type blockingKeywordSearch struct {
	started   chan struct{}
	release   <-chan struct{}
	documents []searchindex.LotDocument
}

func (search *blockingKeywordSearch) SearchKeywords(context.Context, searchindex.KeywordSearchQuery) ([]searchindex.LotDocument, error) {
	close(search.started)
	<-search.release
	return append([]searchindex.LotDocument(nil), search.documents...), nil
}

type blockingVectorSearch struct {
	started   chan struct{}
	release   <-chan struct{}
	documents []searchindex.LotDocument
}

func (search *blockingVectorSearch) Search(context.Context, searchindex.SearchQuery) ([]searchindex.LotDocument, error) {
	close(search.started)
	<-search.release
	return append([]searchindex.LotDocument(nil), search.documents...), nil
}

func (*blockingVectorSearch) RandomPublicDocuments(context.Context, int) ([]searchindex.LotDocument, error) {
	return nil, nil
}

type staticEmbeddingClient struct{}

func (staticEmbeddingClient) Configured() bool { return true }
func (staticEmbeddingClient) Embed(context.Context, []string) ([][]float64, error) {
	return [][]float64{{0.1, 0.2}}, nil
}

type staticKeywordSearch struct {
	documents []searchindex.LotDocument
	err       error
}

func (search staticKeywordSearch) SearchKeywords(context.Context, searchindex.KeywordSearchQuery) ([]searchindex.LotDocument, error) {
	return append([]searchindex.LotDocument(nil), search.documents...), search.err
}

type staticVectorSearch struct {
	documents []searchindex.LotDocument
	err       error
}

func (search staticVectorSearch) Search(context.Context, searchindex.SearchQuery) ([]searchindex.LotDocument, error) {
	return append([]searchindex.LotDocument(nil), search.documents...), search.err
}

func (staticVectorSearch) RandomPublicDocuments(context.Context, int) ([]searchindex.LotDocument, error) {
	return nil, nil
}
