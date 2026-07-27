package aiassistant

import (
	"context"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestMockBuyerConsultUsesCandidates(t *testing.T) {
	assistant := New(Config{Provider: "mock"})
	reply, err := assistant.ConsultBuyer(context.Background(), BuyerConsultRequest{
		Query:          "想看翡翠手镯",
		Budget:         2000000,
		RiskPreference: "steady",
	}, BuyerConsultContext{
		Candidates: []LotCandidate{{
			Type:         "lot",
			Title:        "冰糯翡翠手镯",
			RoomID:       "room-live",
			LotID:        "lot-live",
			Status:       "LOT_STATUS_LIVE",
			CurrentPrice: &v1.Money{Amount: 1800000, Currency: "CNY"},
			Href:         "/m/room/room-live",
			Reason:       "标题命中 翡翠 手镯",
		}},
	})
	if err != nil {
		t.Fatalf("mock consult should not fail: %v", err)
	}
	if !reply.FallbackUsed {
		t.Fatal("mock provider should mark fallbackUsed")
	}
	if reply.Intent != "find_auction" || len(reply.Results) != 1 || reply.Results[0].LotID != "lot-live" {
		t.Fatalf("unexpected consult reply: %+v", reply)
	}
	if len(reply.Sources) == 0 || reply.Sources[0].LotID != "lot-live" {
		t.Fatalf("expected cited lot source, got %+v", reply.Sources)
	}
}

func TestAssistantModeReflectsEffectiveRuntimePath(t *testing.T) {
	if got := New(Config{Provider: "mock"}).Mode(); got != "mock" {
		t.Fatalf("mock mode = %q", got)
	}
	if got := New(Config{Provider: "deepseek"}).Mode(); got != "mock" {
		t.Fatalf("incomplete external provider must use mock path, got %q", got)
	}
	if got := New(Config{
		Provider: "deepseek",
		BaseURL:  "https://ai.example.test/v1",
		Model:    "test-model",
		APIKey:   "test-key",
	}).Mode(); got != "external" {
		t.Fatalf("configured provider mode = %q", got)
	}
	var nilAssistant *Assistant
	if got := nilAssistant.Mode(); got != "mock" {
		t.Fatalf("nil assistant mode = %q", got)
	}
}

func TestBuyerReplyFromMapAcceptsLooseModelShape(t *testing.T) {
	reply := buyerReplyFromMap(map[string]any{
		"answer": "found",
		"intent": "find_auction",
		"results": []any{
			map[string]any{
				"type":         "lot",
				"title":        "精选翡翠拍品",
				"roomId":       "room-ai",
				"lotId":        "lot-ai",
				"status":       "LOT_STATUS_LIVE",
				"currentPrice": map[string]any{"amount": float64(1800000), "currency": "CNY"},
				"href":         "/m/room/room-ai",
				"reason":       "matched query",
			},
		},
		"sources": []any{"精选翡翠拍品"},
	})

	if reply.Answer != "found" || reply.Intent != "find_auction" {
		t.Fatalf("basic fields not decoded: %+v", reply)
	}
	if len(reply.Results) != 1 || reply.Results[0].CurrentPrice.GetAmount() != 1800000 {
		t.Fatalf("results not decoded: %+v", reply.Results)
	}
	if len(reply.Sources) != 1 || reply.Sources[0].Title != "精选翡翠拍品" {
		t.Fatalf("sources not decoded: %+v", reply.Sources)
	}
}

func TestMockBuyerSuggestionsUseCandidates(t *testing.T) {
	assistant := New(Config{Provider: "mock"})
	reply, err := assistant.SuggestBuyerPrompts(context.Background(), BuyerSuggestionRequest{Limit: 4}, BuyerSuggestionContext{
		Candidates: []LotCandidate{{
			Title:  "绿玛瑙翡翠手镯",
			RoomID: "room-real",
			LotID:  "lot-real",
			Status: "LOT_STATUS_QUEUED",
			Reason: "即将开拍 · 适合送礼",
		}},
	})
	if err != nil {
		t.Fatalf("mock suggestions should not fail: %v", err)
	}
	if !reply.FallbackUsed {
		t.Fatal("mock provider should mark fallbackUsed")
	}
	if len(reply.Suggestions) == 0 || len(reply.Suggestions) > 4 {
		t.Fatalf("expected bounded suggestions, got %+v", reply.Suggestions)
	}
}

func TestNormalizeBuyerSuggestionReplyDropsUnsupportedPrompts(t *testing.T) {
	fallback := BuyerSuggestionReply{FallbackUsed: true, Suggestions: []BuyerSuggestion{{Text: "翡翠手镯"}}}
	reply := normalizeBuyerSuggestionReply(BuyerSuggestionReply{Suggestions: []BuyerSuggestion{
		{Text: "不存在的劳力士"},
		{Text: "翡翠手镯"},
	}}, fallback, []LotCandidate{{Title: "绿玛瑙翡翠手镯", Reason: "即将开拍"}}, 6)

	if reply.FallbackUsed {
		t.Fatal("supported model suggestion should keep model path")
	}
	if len(reply.Suggestions) != 1 || reply.Suggestions[0].Text != "翡翠手镯" {
		t.Fatalf("unsupported suggestions should be dropped, got %+v", reply.Suggestions)
	}
}

func TestNormalizeBuyerReplyDropsModelResultsOutsideCandidates(t *testing.T) {
	fallback := BuyerConsultReply{
		Answer:       "fallback answer",
		Intent:       "find_auction",
		FallbackUsed: true,
		Results: []BuyerResult{{
			Type:   "lot",
			Title:  "真实候选",
			RoomID: "room-real",
			LotID:  "lot-real",
			Href:   "/m/room/room-real",
			Reason: "预算内",
		}},
	}
	reply := normalizeBuyerReply(BuyerConsultReply{
		Answer: "model answer",
		Intent: "find_auction",
		Results: []BuyerResult{{
			Type:   "lot",
			Title:  "模型编造",
			RoomID: "room-fake",
			LotID:  "lot-fake",
			Href:   "/m/room/room-fake",
			Reason: "not allowed",
		}},
	}, fallback)

	if !reply.FallbackUsed {
		t.Fatal("invalid model results should fall back to local candidates")
	}
	if len(reply.Results) != 1 || reply.Results[0].LotID != "lot-real" {
		t.Fatalf("expected fallback candidate only, got %+v", reply.Results)
	}
}

func TestNormalizeBuyerReplyKeepsCandidateFactsAuthoritative(t *testing.T) {
	fallback := BuyerConsultReply{
		Answer:       "fallback answer",
		Intent:       "find_auction",
		FallbackUsed: true,
		Results: []BuyerResult{{
			Type:         "lot",
			Title:        "真实标题",
			RoomID:       "room-real",
			LotID:        "lot-real",
			Status:       "LOT_STATUS_LIVE",
			CurrentPrice: &v1.Money{Amount: 30000, Currency: "CNY"},
			Href:         "/m/room/room-real",
			Reason:       "预算内",
			ImageURL:     "https://example.com/real.jpg",
		}},
	}
	reply := normalizeBuyerReply(BuyerConsultReply{
		Answer: "model answer",
		Intent: "find_auction",
		Results: []BuyerResult{{
			Type:         "lot",
			Title:        "模型改标题",
			RoomID:       "room-other",
			LotID:        "lot-real",
			Status:       "LOT_STATUS_QUEUED",
			CurrentPrice: &v1.Money{Amount: 999999, Currency: "CNY"},
			Href:         "/m/room/fake",
			Reason:       "模型只允许改理由",
			ImageURL:     "https://example.com/fake.jpg",
		}},
	}, fallback)

	if reply.FallbackUsed {
		t.Fatal("valid candidate reference should keep model answer path")
	}
	got := reply.Results[0]
	if got.Title != "真实标题" || got.RoomID != "room-real" || got.Href != "/m/room/room-real" || got.CurrentPrice.GetAmount() != 30000 || got.ImageURL != "https://example.com/real.jpg" {
		t.Fatalf("candidate facts must remain authoritative, got %+v", got)
	}
	if got.Reason != "模型只允许改理由" {
		t.Fatalf("expected model reason to be retained, got %q", got.Reason)
	}
}
