package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestAuctionResponseEncoderUsesAcceptedForCreatingOrder(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/rooms/room-1/me", nil)
	reply := &v1.GetRoomPersonalStateReply{
		Result: &v1.ReplyResult{},
		PersonalState: &v1.RoomPersonalState{
			OrderVisibility: v1.OrderVisibility_ORDER_VISIBILITY_CREATING,
		},
		RetryAfterMs: 1_001,
	}

	if err := auctionResponseEncoder(response, request, reply); err != nil {
		t.Fatalf("auctionResponseEncoder: %v", err)
	}
	if response.Code != http.StatusAccepted || response.Header().Get("Retry-After") != "2" {
		t.Fatalf("status=%d retry-after=%q", response.Code, response.Header().Get("Retry-After"))
	}
	if !strings.Contains(response.Body.String(), "ORDER_VISIBILITY_CREATING") {
		t.Fatalf("response body=%s", response.Body.String())
	}
}

func TestAuctionResponseEncoderKeepsReadyOrderSuccessful(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/rooms/room-1/me", nil)
	reply := &v1.GetRoomPersonalStateReply{
		Result: &v1.ReplyResult{},
		PersonalState: &v1.RoomPersonalState{
			OrderVisibility: v1.OrderVisibility_ORDER_VISIBILITY_READY,
		},
	}

	if err := auctionResponseEncoder(response, request, reply); err != nil {
		t.Fatalf("auctionResponseEncoder: %v", err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Retry-After") != "" {
		t.Fatalf("status=%d retry-after=%q", response.Code, response.Header().Get("Retry-After"))
	}
}

func TestLocalDevCORSExposesRetryAfter(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/rooms/room-1/me", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	response := httptest.NewRecorder()
	localDevCORSFilter(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)

	if !strings.Contains(response.Header().Get("Access-Control-Expose-Headers"), "Retry-After") {
		t.Fatalf("exposed headers=%q", response.Header().Get("Access-Control-Expose-Headers"))
	}
}
