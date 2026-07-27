package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
	"live-auction-bid/backend/app/auction/service/internal/realtime"
	appsvc "live-auction-bid/backend/app/auction/service/internal/service"
	"live-auction-bid/backend/app/auction/service/internal/storage"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	httptransport "github.com/go-kratos/kratos/v2/transport/http"
)

func NewHTTPServer(addr string, auctionHTTP v1.AuctionServiceHTTPServer, auction *appsvc.AuctionService, users *appsvc.UserService, shop *appsvc.ShopService, orders *appsvc.OrderService, hub *realtime.Hub, health HealthChecker, authManager *auth.Manager, authMiddleware middleware.Middleware, imageStorage storage.StorageProvider, assetStore assetStore) *httptransport.Server {
	middlewares := []middleware.Middleware{recovery.Recovery()}
	if authMiddleware != nil {
		middlewares = append(middlewares, authMiddleware)
	}
	srv := httptransport.NewServer(
		httptransport.Address(addr),
		httptransport.Middleware(middlewares...),
		httptransport.Filter(localDevCORSFilter, requestctx.HTTPMiddleware),
		httptransport.ResponseEncoder(auctionResponseEncoder),
		httptransport.ErrorEncoder(resultEnvelopeErrorEncoder),
	)

	registerAuctionHTTP(srv, auctionHTTP)
	registerShopHTTP(srv, shop, orders, auction)
	registerUserHTTP(srv, users)
	registerRealtimeHTTP(srv, hub)
	registerUploadHTTP(srv, authManager, assetStore, imageStorage)
	registerOperationHTTP(srv, health, "auction-gateway")

	return srv
}

func auctionResponseEncoder(w http.ResponseWriter, r *http.Request, payload any) error {
	if reply, ok := payload.(*v1.GetRoomPersonalStateReply); ok &&
		reply.GetResult() != nil && reply.GetResult().GetCode() == 0 &&
		reply.GetPersonalState().GetOrderVisibility() == v1.OrderVisibility_ORDER_VISIBILITY_CREATING &&
		reply.GetRetryAfterMs() > 0 {
		retryAfterMs := reply.GetRetryAfterMs()
		retryAfterSeconds := retryAfterMs / 1_000
		if retryAfterMs%1_000 != 0 {
			retryAfterSeconds++
		}
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
		w.WriteHeader(http.StatusAccepted)
	}
	return httptransport.DefaultResponseEncoder(w, r, payload)
}

func registerAuctionHTTP(srv *httptransport.Server, auction v1.AuctionServiceHTTPServer) {
	v1.RegisterAuctionServiceHTTPServer(srv, auction)
}

func registerUserHTTP(srv *httptransport.Server, users *appsvc.UserService) {
	v1.RegisterUserServiceHTTPServer(srv, users)
}

func registerShopHTTP(srv *httptransport.Server, shop *appsvc.ShopService, orders *appsvc.OrderService, auction *appsvc.AuctionService) {
	v1.RegisterShopServiceHTTPServer(srv, appsvc.NewShopServiceHTTPAdapter(shop, orders, auction))
}

func resultEnvelopeErrorEncoder(w http.ResponseWriter, r *http.Request, err error) {
	mapped := err
	if se := kerrors.FromError(err); se != nil && se.Code == int32(http.StatusBadRequest) {
		mapped = fmt.Errorf("%w: invalid request", apperr.ErrInvalidArgument)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": appsvc.ErrorResult(r.Context(), mapped),
	})
}

func localDevCORSFilter(next http.Handler) http.Handler {
	allowed := allowedCORSOrigins()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && allowed[origin] {
			header := w.Header()
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Vary", "Origin")
			header.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			header.Set("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Request-Id,X-Trace-Id,X-Client-App,X-Client-Version,X-Client-Time,Idempotency-Key")
			header.Set("Access-Control-Expose-Headers", "X-Request-Id,X-Trace-Id,X-Server-Time,Retry-After")
			header.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedCORSOrigins() map[string]bool {
	origins := map[string]bool{
		"http://localhost:5173": true,
		"http://localhost:5174": true,
		"http://127.0.0.1:5173": true,
		"http://127.0.0.1:5174": true,
	}
	for _, origin := range strings.Split(os.Getenv("AUCTION_HTTP_CORS_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			origins[origin] = true
		}
	}
	return origins
}
