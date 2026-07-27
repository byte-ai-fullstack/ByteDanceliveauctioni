package requestctx

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/transport"
)

type rpcTestHeader map[string]string

func (h rpcTestHeader) Get(key string) string { return h[key] }
func (h rpcTestHeader) Set(key, value string) { h[key] = value }
func (h rpcTestHeader) Add(key, value string) { h[key] = value }
func (h rpcTestHeader) Values(key string) []string {
	if value, ok := h[key]; ok {
		return []string{value}
	}
	return nil
}
func (h rpcTestHeader) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	return keys
}

type rpcTestTransport struct{ header transport.Header }

func (t rpcTestTransport) Kind() transport.Kind            { return transport.KindGRPC }
func (t rpcTestTransport) Endpoint() string                { return "grpc://auction-service" }
func (t rpcTestTransport) Operation() string               { return "StartLot" }
func (t rpcTestTransport) RequestHeader() transport.Header { return t.header }
func (t rpcTestTransport) ReplyHeader() transport.Header   { return rpcTestHeader{} }

func TestRPCMiddlewareRebuildsRequestContext(t *testing.T) {
	header := rpcTestHeader{
		HeaderRequestID:  "request-1",
		HeaderTraceID:    "trace-1",
		HeaderClientApp:  "buyer-h5",
		HeaderClientVer:  "1.2.3",
		HeaderClientTime: "12345",
	}
	ctx := transport.NewServerContext(context.Background(), rpcTestTransport{header: header})
	handler := RPCMiddleware()(func(ctx context.Context, _ any) (any, error) {
		return Snapshot(ctx), nil
	})
	value, err := handler(ctx, nil)
	if err != nil {
		t.Fatalf("run middleware: %v", err)
	}
	rc := value.(RequestContext)
	if rc.RequestID != "request-1" || rc.TraceID != "trace-1" || rc.ClientType != ClientTypeBuyerH5 {
		t.Fatalf("unexpected request context: %+v", rc)
	}
}

func TestRPCMiddlewareGeneratesMissingCorrelationIDs(t *testing.T) {
	handler := RPCMiddleware()(func(ctx context.Context, _ any) (any, error) {
		return Snapshot(ctx), nil
	})
	value, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("run middleware: %v", err)
	}
	rc := value.(RequestContext)
	if rc.RequestID == "" || rc.TraceID != rc.RequestID {
		t.Fatalf("missing generated correlation ids: %+v", rc)
	}
}
