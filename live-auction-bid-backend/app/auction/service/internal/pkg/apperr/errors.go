package apperr

import "errors"

type BusinessCode string

const (
	CodeInvalidArgument              BusinessCode = "INVALID_ARGUMENT"
	CodeBidRejected                  BusinessCode = "BID_REJECTED"
	CodeBidTooLow                    BusinessCode = "BID_TOO_LOW"
	CodeBidNotLive                   BusinessCode = "BID_NOT_LIVE"
	CodeBidEnded                     BusinessCode = "BID_ENDED"
	CodeBidAlreadyLeading            BusinessCode = "BID_ALREADY_LEADING"
	CodeBidCurrencyMismatch          BusinessCode = "BID_CURRENCY_MISMATCH"
	CodeBidVersionStale              BusinessCode = "BID_VERSION_STALE"
	CodeLotCancelled                 BusinessCode = "LOT_CANCELLED"
	CodeRoomActiveLotExists          BusinessCode = "ROOM_ACTIVE_LOT_EXISTS"
	CodeProjectionPending            BusinessCode = "PROJECTION_PENDING"
	CodeOrderEnrichmentPending       BusinessCode = "ORDER_ENRICHMENT_PENDING"
	CodeAddressRequired              BusinessCode = "ADDRESS_REQUIRED"
	CodeAddressNotFound              BusinessCode = "ADDRESS_NOT_FOUND"
	CodeDepositRequired              BusinessCode = "DEPOSIT_REQUIRED"
	CodePaymentProviderNotConfigured BusinessCode = "PAYMENT_PROVIDER_NOT_CONFIGURED"
	CodeOverloaded                   BusinessCode = "OVERLOADED"
)

// ErrLotVersionConflict is the stable sentinel for optimistic-lock conflicts on a lot aggregate.
// Repository implementations should wrap or return this value so service adapters can expose a
// consistent user-facing retry semantic instead of leaking storage-specific errors.
var ErrLotVersionConflict = errors.New("lot version conflict")

var (
	ErrInvalidArgument              = errors.New("invalid argument")
	ErrBidRejected                  = errors.New(string(CodeBidRejected))
	ErrBidTooLow                    = errors.New(string(CodeBidTooLow))
	ErrBidNotLive                   = errors.New(string(CodeBidNotLive))
	ErrBidEnded                     = errors.New(string(CodeBidEnded))
	ErrBidAlreadyLeading            = errors.New(string(CodeBidAlreadyLeading))
	ErrBidCurrencyMismatch          = errors.New(string(CodeBidCurrencyMismatch))
	ErrLotCancelled                 = errors.New(string(CodeLotCancelled))
	ErrUnauthenticated              = errors.New("unauthenticated")
	ErrPermissionDenied             = errors.New("permission denied")
	ErrRoomActiveLotExists          = errors.New("room active lot exists")
	ErrQueuePositionConflict        = errors.New("queue position conflict")
	ErrRuntimeProjectionGap         = errors.New("runtime projection gap")
	ErrRuntimeProjectionConflict    = errors.New("runtime projection conflict")
	ErrOrderEnrichmentPending       = errors.New(string(CodeOrderEnrichmentPending))
	ErrUsernameTaken                = errors.New("username already exists")
	ErrInvalidCredentials           = errors.New("invalid username or password")
	ErrInvalidToken                 = errors.New("invalid token")
	ErrTokenExpired                 = errors.New("token expired")
	ErrSessionExpired               = errors.New("session expired")
	ErrAccountDisabled              = errors.New("account disabled")
	ErrUserNotFound                 = errors.New("user not found")
	ErrNotFound                     = errors.New("not found")
	ErrAddressRequired              = errors.New(string(CodeAddressRequired))
	ErrAddressNotFound              = errors.New(string(CodeAddressNotFound))
	ErrDepositRequired              = errors.New(string(CodeDepositRequired))
	ErrPaymentProviderNotConfigured = errors.New(string(CodePaymentProviderNotConfigured))
	ErrOverloaded                   = errors.New(string(CodeOverloaded))
)

func IsLotVersionConflict(err error) bool {
	return errors.Is(err, ErrLotVersionConflict)
}

func BusinessCodeForError(err error) BusinessCode {
	switch {
	case errors.Is(err, ErrBidTooLow):
		return CodeBidTooLow
	case errors.Is(err, ErrBidNotLive):
		return CodeBidNotLive
	case errors.Is(err, ErrBidEnded):
		return CodeBidEnded
	case errors.Is(err, ErrBidAlreadyLeading):
		return CodeBidAlreadyLeading
	case errors.Is(err, ErrBidCurrencyMismatch):
		return CodeBidCurrencyMismatch
	case errors.Is(err, ErrLotVersionConflict):
		return CodeBidVersionStale
	case errors.Is(err, ErrLotCancelled):
		return CodeLotCancelled
	case errors.Is(err, ErrRoomActiveLotExists):
		return CodeRoomActiveLotExists
	case errors.Is(err, ErrRuntimeProjectionGap), errors.Is(err, ErrRuntimeProjectionConflict):
		return CodeProjectionPending
	case errors.Is(err, ErrOrderEnrichmentPending):
		return CodeOrderEnrichmentPending
	case errors.Is(err, ErrAddressRequired):
		return CodeAddressRequired
	case errors.Is(err, ErrAddressNotFound):
		return CodeAddressNotFound
	case errors.Is(err, ErrDepositRequired):
		return CodeDepositRequired
	case errors.Is(err, ErrPaymentProviderNotConfigured):
		return CodePaymentProviderNotConfigured
	case errors.Is(err, ErrOverloaded):
		return CodeOverloaded
	case errors.Is(err, ErrBidRejected):
		return CodeBidRejected
	}
	return ""
}

func ErrorForBusinessCode(code string) error {
	switch BusinessCode(code) {
	case CodeBidTooLow:
		return ErrBidTooLow
	case CodeBidNotLive:
		return ErrBidNotLive
	case CodeBidEnded:
		return ErrBidEnded
	case CodeBidAlreadyLeading:
		return ErrBidAlreadyLeading
	case CodeBidCurrencyMismatch:
		return ErrBidCurrencyMismatch
	case CodeBidVersionStale:
		return ErrLotVersionConflict
	case CodeLotCancelled:
		return ErrLotCancelled
	case CodeRoomActiveLotExists:
		return ErrRoomActiveLotExists
	case CodeProjectionPending:
		return ErrRuntimeProjectionGap
	case CodeOrderEnrichmentPending:
		return ErrOrderEnrichmentPending
	case CodeAddressRequired:
		return ErrAddressRequired
	case CodeAddressNotFound:
		return ErrAddressNotFound
	case CodeDepositRequired:
		return ErrDepositRequired
	case CodePaymentProviderNotConfigured:
		return ErrPaymentProviderNotConfigured
	case CodeOverloaded:
		return ErrOverloaded
	case CodeBidRejected:
		return ErrBidRejected
	default:
		return ErrInvalidArgument
	}
}

func IsInvalidArgument(err error) bool {
	return errors.Is(err, ErrInvalidArgument)
}

func IsUnauthenticated(err error) bool {
	return errors.Is(err, ErrUnauthenticated)
}

func IsPermissionDenied(err error) bool {
	return errors.Is(err, ErrPermissionDenied)
}

func IsRoomActiveLotExists(err error) bool {
	return errors.Is(err, ErrRoomActiveLotExists)
}

func IsQueuePositionConflict(err error) bool {
	return errors.Is(err, ErrQueuePositionConflict)
}

func IsRuntimeProjectionGap(err error) bool {
	return errors.Is(err, ErrRuntimeProjectionGap)
}

func IsRuntimeProjectionConflict(err error) bool {
	return errors.Is(err, ErrRuntimeProjectionConflict)
}

func IsUsernameTaken(err error) bool {
	return errors.Is(err, ErrUsernameTaken)
}

func IsInvalidCredentials(err error) bool {
	return errors.Is(err, ErrInvalidCredentials)
}

func IsInvalidToken(err error) bool {
	return errors.Is(err, ErrInvalidToken)
}

func IsTokenExpired(err error) bool {
	return errors.Is(err, ErrTokenExpired)
}

func IsSessionExpired(err error) bool {
	return errors.Is(err, ErrSessionExpired)
}

func IsAccountDisabled(err error) bool {
	return errors.Is(err, ErrAccountDisabled)
}

func IsUserNotFound(err error) bool {
	return errors.Is(err, ErrUserNotFound)
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrUserNotFound)
}
