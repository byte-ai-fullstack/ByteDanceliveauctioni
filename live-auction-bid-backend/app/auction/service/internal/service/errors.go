package service

import (
	"context"
	"strings"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/requestctx"
)

const (
	ResultCodeOK                           int32 = 0
	ResultCodeInvalidArgument              int32 = 400001
	ResultCodeLoginRequired                int32 = 401001
	ResultCodeTokenExpired                 int32 = 401002
	ResultCodeTokenInvalid                 int32 = 401003
	ResultCodeSessionExpired               int32 = 401004
	ResultCodeInvalidCredentials           int32 = 401005
	ResultCodeForbidden                    int32 = 403001
	ResultCodeAccountDisabled              int32 = 403002
	ResultCodeLotVersionConflict           int32 = 409001
	ResultCodeUsernameTaken                int32 = 409002
	ResultCodeRoomActiveLotExists          int32 = 409003
	ResultCodeQueuePositionConflict        int32 = 409004
	ResultCodeBidTooLow                    int32 = 409101
	ResultCodeBidNotLive                   int32 = 409102
	ResultCodeBidEnded                     int32 = 409103
	ResultCodeBidAlreadyLeading            int32 = 409104
	ResultCodeBidCurrencyMismatch          int32 = 409105
	ResultCodeBidVersionStale              int32 = 409106
	ResultCodeLotCancelled                 int32 = 409107
	ResultCodeProjectionPending            int32 = 409108
	ResultCodeDepositRequired              int32 = 409109
	ResultCodeAddressRequired              int32 = 409110
	ResultCodeAddressNotFound              int32 = 409111
	ResultCodeUserNotFound                 int32 = 404001
	ResultCodePaymentProviderNotConfigured int32 = 500101
	ResultCodeOverloaded                   int32 = 503001
	ResultCodeInternalError                int32 = 500000

	ResultCodeUnauthenticated  = ResultCodeLoginRequired
	ResultCodeInvalidToken     = ResultCodeTokenInvalid
	ResultCodePermissionDenied = ResultCodeForbidden
)

const (
	MessageOK                    = "ok"
	MessageLotVersionConflict    = string(apperr.CodeBidVersionStale)
	MessageRoomActiveLotExists   = string(apperr.CodeRoomActiveLotExists)
	MessageQueuePositionConflict = "当前直播间队列正在更新，请刷新后重试"
	MessageInternalError         = "internal error, please try again later"
	MessageTokenExpired          = "access token expired, please refresh"
	MessageSessionExpired        = "session expired, please login again"
)

func OKResult(ctx context.Context) *v1.ReplyResult {
	return okResult(ctx)
}

func okResult(ctx context.Context) *v1.ReplyResult {
	return &v1.ReplyResult{Code: ResultCodeOK, Message: MessageOK, TraceId: requestctx.TraceID(ctx)}
}

func ErrorResult(ctx context.Context, err error) *v1.ReplyResult {
	if err == nil {
		return okResult(ctx)
	}
	traceID := requestctx.TraceID(ctx)
	if code := apperr.BusinessCodeForError(err); code != "" {
		return &v1.ReplyResult{Code: resultCodeForBusinessCode(code), Message: string(code), TraceId: traceID}
	}
	if apperr.IsLotVersionConflict(err) {
		return &v1.ReplyResult{Code: ResultCodeLotVersionConflict, Message: MessageLotVersionConflict, TraceId: traceID}
	}
	if apperr.IsRoomActiveLotExists(err) {
		return &v1.ReplyResult{Code: ResultCodeRoomActiveLotExists, Message: MessageRoomActiveLotExists, TraceId: traceID}
	}
	if apperr.IsQueuePositionConflict(err) {
		return &v1.ReplyResult{Code: ResultCodeQueuePositionConflict, Message: MessageQueuePositionConflict, TraceId: traceID}
	}
	if apperr.IsInvalidArgument(err) {
		return &v1.ReplyResult{Code: ResultCodeInvalidArgument, Message: invalidArgumentMessage(err), TraceId: traceID}
	}
	if apperr.IsUnauthenticated(err) {
		return &v1.ReplyResult{Code: ResultCodeLoginRequired, Message: "login required", TraceId: traceID}
	}
	if apperr.IsPermissionDenied(err) {
		return &v1.ReplyResult{Code: ResultCodeForbidden, Message: "permission denied", TraceId: traceID}
	}
	if apperr.IsAccountDisabled(err) {
		return &v1.ReplyResult{Code: ResultCodeAccountDisabled, Message: "account disabled", TraceId: traceID}
	}
	if apperr.IsUsernameTaken(err) {
		return &v1.ReplyResult{Code: ResultCodeUsernameTaken, Message: "username already exists", TraceId: traceID}
	}
	if apperr.IsInvalidCredentials(err) {
		return &v1.ReplyResult{Code: ResultCodeInvalidCredentials, Message: "invalid username or password", TraceId: traceID}
	}
	if apperr.IsSessionExpired(err) {
		return &v1.ReplyResult{Code: ResultCodeSessionExpired, Message: MessageSessionExpired, TraceId: traceID}
	}
	if apperr.IsTokenExpired(err) {
		return &v1.ReplyResult{Code: ResultCodeTokenExpired, Message: MessageTokenExpired, TraceId: traceID}
	}
	if apperr.IsInvalidToken(err) {
		return &v1.ReplyResult{Code: ResultCodeTokenInvalid, Message: "invalid token", TraceId: traceID}
	}
	if apperr.IsUserNotFound(err) {
		return &v1.ReplyResult{Code: ResultCodeUserNotFound, Message: "user not found", TraceId: traceID}
	}
	if apperr.IsNotFound(err) {
		return &v1.ReplyResult{Code: ResultCodeUserNotFound, Message: "not found", TraceId: traceID}
	}
	return &v1.ReplyResult{Code: ResultCodeInternalError, Message: MessageInternalError, TraceId: traceID}
}

func resultCodeForBusinessCode(code apperr.BusinessCode) int32 {
	switch code {
	case apperr.CodeBidTooLow:
		return ResultCodeBidTooLow
	case apperr.CodeBidNotLive:
		return ResultCodeBidNotLive
	case apperr.CodeBidEnded:
		return ResultCodeBidEnded
	case apperr.CodeBidAlreadyLeading:
		return ResultCodeBidAlreadyLeading
	case apperr.CodeBidCurrencyMismatch:
		return ResultCodeBidCurrencyMismatch
	case apperr.CodeBidVersionStale:
		return ResultCodeBidVersionStale
	case apperr.CodeLotCancelled:
		return ResultCodeLotCancelled
	case apperr.CodeRoomActiveLotExists:
		return ResultCodeRoomActiveLotExists
	case apperr.CodeProjectionPending:
		return ResultCodeProjectionPending
	case apperr.CodeDepositRequired:
		return ResultCodeDepositRequired
	case apperr.CodeAddressRequired:
		return ResultCodeAddressRequired
	case apperr.CodeAddressNotFound:
		return ResultCodeAddressNotFound
	case apperr.CodePaymentProviderNotConfigured:
		return ResultCodePaymentProviderNotConfigured
	case apperr.CodeOverloaded:
		return ResultCodeOverloaded
	default:
		return ResultCodeInvalidArgument
	}
}

func invalidArgumentMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	message = strings.TrimPrefix(message, apperr.ErrInvalidArgument.Error()+": ")
	switch {
	case strings.Contains(message, "leading bidder must wait") || strings.Contains(message, "最高价"):
		return "你当前已经是最高价，等其他人出价后再加价"
	case strings.Contains(message, "bid amount is lower"):
		return "出价金额太低，请按当前加价幅度重新出价"
	case strings.Contains(message, "lot is not live"), strings.Contains(message, "auction has ended"):
		return "当前商品还未开始或已结束"
	case strings.Contains(message, "currency"):
		return "出价币种异常，请刷新后重试"
	case strings.Contains(message, "runtime state is missing"):
		return "竞拍状态正在同步，请稍后重试"
	case strings.Contains(message, "lot title"):
		return "拍品标题不能为空"
	case strings.Contains(message, "lot image url"):
		return "拍品图片不能为空"
	case strings.Contains(message, "bid rule, start price and min increment"):
		return "请填写起拍价和最低加价"
	case strings.Contains(message, "start price and min increment currency are required"):
		return "起拍价和最低加价币种不能为空"
	case strings.Contains(message, "currency must match"):
		return "价格币种必须一致"
	case strings.Contains(message, "start price amount"):
		return "起拍价不能小于 0"
	case strings.Contains(message, "min increment amount"):
		return "最低加价必须大于 0"
	case strings.Contains(message, "duration seconds"):
		return "竞拍时长不能少于 60 秒"
	case strings.Contains(message, "address"):
		return "请先选择收货地址"
	}
	return "参数不正确，请检查后重试"
}
