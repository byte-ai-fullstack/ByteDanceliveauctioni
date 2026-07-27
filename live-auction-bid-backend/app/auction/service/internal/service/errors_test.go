package service

import (
	"context"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
)

func TestErrorResultMapsOverloadedContract(t *testing.T) {
	t.Parallel()

	ctx := requestctx.WithRequestContext(context.Background(), requestctx.RequestContext{TraceID: "trace-overload"})
	result := ErrorResult(ctx, apperr.ErrOverloaded)
	if result.GetCode() != ResultCodeOverloaded || result.GetMessage() != string(apperr.CodeOverloaded) ||
		result.GetTraceId() != "trace-overload" {
		t.Fatalf("ErrorResult(OVERLOADED) = %+v", result)
	}
}
