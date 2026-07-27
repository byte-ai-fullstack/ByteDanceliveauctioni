package eventcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const (
	RuntimeSchemaVersionV1 uint32 = 1
	MaxRuntimeFactBytes           = 256 << 10
	MaxRuntimeRankingItems        = 50
	maxRedisExactInteger   int64  = 1<<53 - 1
)

var (
	ErrInvalidEventID         = errors.New("invalid event id")
	ErrInvalidMessageID       = errors.New("invalid message id")
	ErrInvalidRuntimeFact     = errors.New("invalid runtime fact")
	ErrInvalidLotStateDomain  = errors.New("invalid lot state domain event")
	ErrUnsupportedSchema      = errors.New("unsupported schema version")
	ErrConfigVersionExhausted = errors.New("config version exhausted")
)

// NewEventID returns a time-ordered UUIDv7 suitable for a global message identity.
func NewEventID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("new UUIDv7: %w", err)
	}
	return id.String(), nil
}

// ValidateEventID accepts only canonical UUIDv7 strings.
func ValidateEventID(value string) error {
	value = strings.TrimSpace(value)
	id, err := uuid.Parse(value)
	if err != nil || id.Version() != 7 || id.String() != value {
		return fmt.Errorf("%w: expected canonical UUIDv7", ErrInvalidEventID)
	}
	return nil
}

// DomainMessageID derives one stable identity per causation and domain event type.
func DomainMessageID(causationID, eventType string) (string, error) {
	if err := ValidateEventID(causationID); err != nil {
		return "", fmt.Errorf("%w: causation id: %v", ErrInvalidMessageID, err)
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || !validEventType(eventType) {
		return "", fmt.Errorf("%w: event type must contain only lowercase letters, digits, dot, dash, or underscore", ErrInvalidMessageID)
	}
	messageID := causationID + ":" + eventType
	if len(messageID) > 128 {
		return "", fmt.Errorf("%w: length %d exceeds 128", ErrInvalidMessageID, len(messageID))
	}
	return messageID, nil
}

// RuntimeOrderID returns the one stable order identity for a runtime lot.
// A close-worker retry, replica race, or Redis reconstruction must not mint a second order ID.
func RuntimeOrderID(lotID string) (string, error) {
	lotID = strings.TrimSpace(lotID)
	if lotID == "" {
		return "", errors.New("runtime order lot id is required")
	}
	sum := sha256.Sum256([]byte("auction-runtime-order-v1:" + lotID))
	return "order_" + hex.EncodeToString(sum[:16]), nil
}

// PayloadHash is the SHA-256 hex digest of deterministic protobuf bytes.
func PayloadHash(message proto.Message) (string, error) {
	payload, err := deterministicBytes(message)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// LotStateContentHash returns the canonical identity of a complete search
// document, excluding transport metadata and the hash field itself.
func LotStateContentHash(event *v1.LotStateDomainEventV1) (string, error) {
	if event == nil {
		return "", errors.New("lot state domain event is required")
	}
	canonical := proto.Clone(event).(*v1.LotStateDomainEventV1)
	canonical.Metadata = nil
	canonical.ContentHash = ""
	return PayloadHash(canonical)
}

func ValidateLotStateDomainEvent(event *v1.LotStateDomainEventV1) error {
	if event == nil || !validRequiredText(event.GetLotId(), 64) || !validRequiredText(event.GetRoomId(), 64) ||
		!validRequiredText(event.GetMainAccountId(), 64) || event.GetLotVersion() <= 0 ||
		event.GetStatus() == v1.LotStatus_LOT_STATUS_UNSPECIFIED || !validRequiredText(event.GetTitle(), 255) ||
		len(event.GetDescription()) > 65_535 || len(event.GetCategory()) > 64 || len(event.GetImageUrl()) > 1024 ||
		len(event.GetTags()) > 32 || !validLotStateTags(event.GetTags()) || !validCurrency(event.GetCurrency()) ||
		event.GetStartPriceFen() < 0 || event.GetCurrentPriceFen() < event.GetStartPriceFen() ||
		event.GetStartsAtUnixMs() < 0 || event.GetEndsAtUnixMs() < 0 {
		return ErrInvalidLotStateDomain
	}
	contentHash, err := LotStateContentHash(event)
	if err != nil || len(event.GetContentHash()) != 64 || contentHash != event.GetContentHash() {
		return ErrInvalidLotStateDomain
	}
	return nil
}

func validLotStateTags(tags []string) bool {
	for _, tag := range tags {
		if !validRequiredText(tag, 64) {
			return false
		}
	}
	return true
}

// CanonicalStateHash is a 128-bit truncated SHA-256 digest of deterministic state bytes.
func CanonicalStateHash(state *v1.LotRuntimeStateV1) (string, error) {
	payload, err := deterministicBytes(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:16]), nil
}

// NextConfigVersion starts configuration at 1 and advances it monotonically.
func NextConfigVersion(current int64) (int64, error) {
	if current < 0 {
		return 0, errors.New("config version cannot be negative")
	}
	if current == math.MaxInt64 {
		return 0, ErrConfigVersionExhausted
	}
	return current + 1, nil
}

// ValidateRuntimeFact enforces the Runtime Topic's non-negotiable V1 contract.
func ValidateRuntimeFact(fact *v1.RuntimeFactV1) error {
	if fact == nil {
		return fmt.Errorf("%w: fact is required", ErrInvalidRuntimeFact)
	}
	if fact.SchemaVersion != RuntimeSchemaVersionV1 {
		return fmt.Errorf("%w: got %d want %d", ErrUnsupportedSchema, fact.SchemaVersion, RuntimeSchemaVersionV1)
	}
	if err := ValidateEventID(fact.EventId); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRuntimeFact, err)
	}
	if !validRequiredText(fact.LotId, 64) || !validRequiredText(fact.RoomId, 64) {
		return fmt.Errorf("%w: lot_id and room_id are required", ErrInvalidRuntimeFact)
	}
	if len(fact.TraceId) > 128 || strings.TrimSpace(fact.TraceId) != fact.TraceId {
		return fmt.Errorf("%w: trace_id is invalid", ErrInvalidRuntimeFact)
	}
	if fact.PrevLotVersion < 0 || fact.LotVersion != fact.PrevLotVersion+1 {
		return fmt.Errorf("%w: lot version must advance exactly once", ErrInvalidRuntimeFact)
	}
	if !validRedisPositive(fact.OccurredAtUnixMs) {
		return fmt.Errorf("%w: occurred_at_unix_ms must be positive", ErrInvalidRuntimeFact)
	}
	if !validRedisPositive(fact.ConfigVersion) {
		return fmt.Errorf("%w: config_version must be positive", ErrInvalidRuntimeFact)
	}
	if fact.Command == v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_UNSPECIFIED {
		return fmt.Errorf("%w: command is required", ErrInvalidRuntimeFact)
	}
	state := fact.StateAfter
	if state == nil {
		return fmt.Errorf("%w: state_after is required", ErrInvalidRuntimeFact)
	}
	if state.LotId != fact.LotId || state.RoomId != fact.RoomId {
		return fmt.Errorf("%w: state_after identity mismatch", ErrInvalidRuntimeFact)
	}
	if state.Status == v1.LotStatus_LOT_STATUS_UNSPECIFIED {
		return fmt.Errorf("%w: state_after status is required", ErrInvalidRuntimeFact)
	}
	if !validCurrency(state.Currency) {
		return fmt.Errorf("%w: state_after currency must be three uppercase ASCII letters", ErrInvalidRuntimeFact)
	}
	if state.DurationMs == nil || !validRedisPositive(state.GetDurationMs()) {
		return fmt.Errorf("%w: state_after duration_ms must be positive", ErrInvalidRuntimeFact)
	}
	if state.AntiSnipeWindowMs == nil || !validRedisNonNegative(state.GetAntiSnipeWindowMs()) {
		return fmt.Errorf("%w: state_after anti_snipe_window_ms cannot be negative", ErrInvalidRuntimeFact)
	}
	if state.AntiSnipeExtendMs == nil || !validRedisNonNegative(state.GetAntiSnipeExtendMs()) {
		return fmt.Errorf("%w: state_after anti_snipe_extend_ms cannot be negative", ErrInvalidRuntimeFact)
	}
	if err := validateRuntimeState(state); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRuntimeFact, err)
	}
	if err := validateRuntimeCommand(fact); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRuntimeFact, err)
	}
	payload, err := deterministicBytes(fact)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRuntimeFact, err)
	}
	if len(payload) > MaxRuntimeFactBytes {
		return fmt.Errorf("%w: encoded size %d exceeds %d", ErrInvalidRuntimeFact, len(payload), MaxRuntimeFactBytes)
	}
	return nil
}

func validateRuntimeState(state *v1.LotRuntimeStateV1) error {
	if !validRedisNonNegative(state.GetStartPriceFen()) || !validRedisPositive(state.GetMinIncrementFen()) ||
		!validRedisNonNegative(state.GetCurrentPriceFen()) || state.GetCurrentPriceFen() < state.GetStartPriceFen() ||
		!validRedisNonNegative(state.GetFinalPriceFen()) {
		return errors.New("state_after monetary fields are invalid")
	}
	if state.CapPriceFen != nil && (!validRedisNonNegative(state.GetCapPriceFen()) || state.GetCapPriceFen() < state.GetStartPriceFen()) {
		return errors.New("state_after cap_price_fen is invalid")
	}
	for _, value := range []int64{
		state.GetStartedAtUnixMs(), state.GetEndsAtUnixMs(), state.GetSettledAtUnixMs(), state.GetCancelledAtUnixMs(),
		state.GetBidCount(), state.GetParticipantCount(),
	} {
		if !validRedisNonNegative(value) {
			return errors.New("state_after time or count is outside Redis exact integer range")
		}
	}
	if state.GetParticipantCount() > state.GetBidCount() || state.GetExtendCount() < 0 ||
		state.GetMaxExtendCount() < 0 || state.GetExtendCount() > state.GetMaxExtendCount() {
		return errors.New("state_after counters are inconsistent")
	}
	if len(state.GetTopRanking()) > MaxRuntimeRankingItems {
		return errors.New("state_after top_ranking exceeds its bounded limit")
	}
	for index, item := range state.GetTopRanking() {
		if item == nil || item.GetRank() != int32(index+1) || !validRequiredText(item.GetUserId(), 64) ||
			len(item.GetMaskedNickname()) > 128 || len(item.GetAvatarUrl()) > 1024 ||
			!validRedisNonNegative(item.GetAmountFen()) || !validRedisPositive(item.GetBidAtUnixMs()) {
			return errors.New("state_after top_ranking item is invalid")
		}
	}
	if len(state.GetLeadingUserId()) > 64 || len(state.GetLeadingNickname()) > 128 ||
		len(state.GetWinnerUserId()) > 64 || len(state.GetWinnerNickname()) > 128 || len(state.GetOrderId()) > 64 ||
		len(state.GetCancelReason()) > 512 {
		return errors.New("state_after string exceeds its storage limit")
	}
	switch state.GetStatus() {
	case v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED:
		if !validRedisPositive(state.GetStartedAtUnixMs()) || !validRedisPositive(state.GetEndsAtUnixMs()) || state.GetEndsAtUnixMs() <= state.GetStartedAtUnixMs() {
			return errors.New("live state has an invalid auction window")
		}
	case v1.LotStatus_LOT_STATUS_SETTLED:
		if !validRequiredText(state.GetWinnerUserId(), 64) || !validRequiredText(state.GetOrderId(), 64) ||
			!validRedisPositive(state.GetSettledAtUnixMs()) || state.GetFinalPriceFen() != state.GetCurrentPriceFen() {
			return errors.New("settled state is incomplete")
		}
	case v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		if !validRequiredText(state.GetCancelReason(), 512) || !validRedisPositive(state.GetCancelledAtUnixMs()) {
			return errors.New("cancelled or failed state is incomplete")
		}
	default:
		return errors.New("state_after status is not a runtime status")
	}
	return nil
}

func validateRuntimeCommand(fact *v1.RuntimeFactV1) error {
	state := fact.GetStateAfter()
	switch fact.GetCommand() {
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT:
		if state.GetStatus() != v1.LotStatus_LOT_STATUS_LIVE || fact.GetAcceptedBid() != nil || fact.GetOrderDraft() != nil {
			return errors.New("start_lot result is inconsistent")
		}
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID:
		if fact.GetAcceptedBid() == nil || (state.GetStatus() != v1.LotStatus_LOT_STATUS_LIVE && state.GetStatus() != v1.LotStatus_LOT_STATUS_EXTENDED && state.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED) {
			return errors.New("place_bid result is inconsistent")
		}
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT:
		if state.GetStatus() != v1.LotStatus_LOT_STATUS_CANCELLED || fact.GetAcceptedBid() != nil || fact.GetOrderDraft() != nil {
			return errors.New("cancel_lot result is inconsistent")
		}
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED:
		if (state.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED && state.GetStatus() != v1.LotStatus_LOT_STATUS_FAILED) || fact.GetAcceptedBid() != nil {
			return errors.New("close_if_expired result is inconsistent")
		}
	case v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG:
		if (state.GetStatus() != v1.LotStatus_LOT_STATUS_LIVE && state.GetStatus() != v1.LotStatus_LOT_STATUS_EXTENDED) || fact.GetAcceptedBid() != nil || fact.GetOrderDraft() != nil {
			return errors.New("sync_lot_config result is inconsistent")
		}
	default:
		return errors.New("command is unsupported")
	}
	if err := validateAcceptedBid(fact); err != nil {
		return err
	}
	return validateOrderDraft(fact)
}

func validateAcceptedBid(fact *v1.RuntimeFactV1) error {
	bid := fact.GetAcceptedBid()
	if bid == nil {
		return nil
	}
	state := fact.GetStateAfter()
	if !validRequiredText(fact.GetIdempotencyKey(), 128) || !validRequiredText(bid.GetBidId(), 64) ||
		!validRequiredText(bid.GetUserId(), 64) || !validRequiredText(bid.GetNickname(), 128) || len(bid.GetAvatarUrl()) > 1024 ||
		!validRedisPositive(bid.GetAmountFen()) || !validRedisPositive(bid.GetAcceptedAtUnixMs()) {
		return errors.New("accepted_bid fields are invalid")
	}
	if bid.GetAmountFen() != state.GetCurrentPriceFen() || bid.GetUserId() != state.GetLeadingUserId() ||
		bid.GetNickname() != state.GetLeadingNickname() || bid.GetAcceptedAtUnixMs() != fact.GetOccurredAtUnixMs() {
		return errors.New("accepted_bid does not match state_after")
	}
	return nil
}

func validateOrderDraft(fact *v1.RuntimeFactV1) error {
	draft := fact.GetOrderDraft()
	state := fact.GetStateAfter()
	if state.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED {
		if draft != nil {
			return errors.New("order_draft is only valid for a settled state")
		}
		return nil
	}
	if draft == nil {
		return errors.New("settled state requires order_draft")
	}
	if !validRequiredText(draft.GetOrderId(), 64) || !validRequiredText(draft.GetMainAccountId(), 64) ||
		!validRequiredText(draft.GetBuyerUserId(), 64) || !validRequiredText(draft.GetBuyerNickname(), 128) ||
		!validRequiredText(draft.GetTitle(), 255) || len(draft.GetImageUrl()) > 1024 ||
		!validRedisPositive(draft.GetTotalAmountFen()) || !validRedisPositive(draft.GetCreatedAtUnixMs()) || !validCurrency(draft.GetCurrency()) {
		return errors.New("order_draft fields are invalid")
	}
	if draft.GetLotId() != fact.GetLotId() || draft.GetRoomId() != fact.GetRoomId() || draft.GetOrderId() != state.GetOrderId() ||
		draft.GetBuyerUserId() != state.GetWinnerUserId() || draft.GetBuyerNickname() != state.GetWinnerNickname() ||
		draft.GetTotalAmountFen() != state.GetFinalPriceFen() || draft.GetCurrency() != state.GetCurrency() ||
		draft.GetCreatedAtUnixMs() != fact.GetOccurredAtUnixMs() {
		return errors.New("order_draft does not match state_after")
	}
	return nil
}

func validRequiredText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && strings.TrimSpace(value) == value
}

func validRedisNonNegative(value int64) bool {
	return value >= 0 && value <= maxRedisExactInteger
}

func validRedisPositive(value int64) bool {
	return value > 0 && value <= maxRedisExactInteger
}

func deterministicBytes(message proto.Message) ([]byte, error) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return nil, errors.New("protobuf message is required")
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal deterministic protobuf: %w", err)
	}
	return payload, nil
}

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for index := range value {
		if value[index] < 'A' || value[index] > 'Z' {
			return false
		}
	}
	return true
}

func validEventType(value string) bool {
	for index := range value {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
