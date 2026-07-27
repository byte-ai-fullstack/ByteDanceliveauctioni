package requestctx

import (
	"context"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
)

// RPCMiddleware rebuilds transport-neutral request metadata at an internal RPC
// boundary. Only the explicit tracing and client headers are accepted; user
// identity is independently verified by the auth middleware.
func RPCMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			rc := RequestContext{ServerTimeMs: time.Now().UnixMilli(), ClientType: ClientTypeUnknown}
			if tr, ok := transport.FromServerContext(ctx); ok && tr.RequestHeader() != nil {
				header := tr.RequestHeader()
				rc.RequestID = strings.TrimSpace(header.Get(HeaderRequestID))
				rc.TraceID = strings.TrimSpace(header.Get(HeaderTraceID))
				rc.ClientApp = strings.TrimSpace(header.Get(HeaderClientApp))
				rc.ClientVer = strings.TrimSpace(header.Get(HeaderClientVer))
				rc.ClientTime = strings.TrimSpace(header.Get(HeaderClientTime))
				rc.ClientType = inferClientType(rc.ClientApp)
			}
			if rc.RequestID == "" {
				rc.RequestID = newID()
			}
			if rc.TraceID == "" {
				rc.TraceID = rc.RequestID
			}
			return next(WithRequestContext(ctx, rc), req)
		}
	}
}
