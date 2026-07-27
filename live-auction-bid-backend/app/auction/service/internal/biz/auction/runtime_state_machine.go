package auction

import (
	"fmt"
	"sort"
	"strings"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const (
	RuntimeCodeInvalidArgument       = "INVALID_ARGUMENT"
	RuntimeCodeStateMissing          = "RUNTIME_STATE_MISSING"
	RuntimeCodeNotActive             = "RUNTIME_NOT_ACTIVE"
	RuntimeCodeStateAlreadyExists    = "RUNTIME_STATE_ALREADY_EXISTS"
	RuntimeCodeConfigVersionConflict = "CONFIG_VERSION_CONFLICT"
	RuntimeCodeBidNotLive            = "BID_NOT_LIVE"
	RuntimeCodeBidEnded              = "BID_ENDED"
	RuntimeCodeLotCancelled          = "LOT_CANCELLED"
	RuntimeCodeBidCurrencyMismatch   = "BID_CURRENCY_MISMATCH"
	RuntimeCodeBidAlreadyLeading     = "BID_ALREADY_LEADING"
	RuntimeCodeBidTooLow             = "BID_TOO_LOW"
	RuntimeCodeNotLive               = "NOT_LIVE"
	RuntimeCodeNotExpired            = "NOT_EXPIRED"
	RuntimeCodeAlreadyTerminal       = "ALREADY_TERMINAL"
	RuntimeCodeConfigFrozen          = "CONFIG_FROZEN"
	RuntimeCodeLotFrozen             = "LOT_FROZEN"
	RuntimeExpiredNoBidReason        = "auction expired without accepted bid"

	maxRuntimeRankingLimit = 100
	maxRedisExactInteger   = int64(1<<53 - 1)
)

// RuntimeDecisionError is a deterministic business rejection shared by the Go model and Lua scripts.
type RuntimeDecisionError struct {
	Code         string
	EndsAtUnixMs int64
	CurrentPrice int64
	MinIncrement int64
	MinimumBid   int64
	LotVersion   int64
}

func (e *RuntimeDecisionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// RuntimeConfigSnapshot is the immutable auction configuration copied into Redis at start time.
type RuntimeConfigSnapshot struct {
	LotID             string
	RoomID            string
	MainAccountID     string
	Title             string
	ImageURL          string
	ConfigVersion     int64
	Currency          string
	StartPriceFen     int64
	MinIncrementFen   int64
	CapPriceFen       *int64
	DurationMs        int64
	AntiSnipeWindowMs int64
	AntiSnipeExtendMs int64
	MaxExtendCount    int32
}

// RuntimeRankingEntry is the bounded Top-N projection carried in each runtime fact.
type RuntimeRankingEntry struct {
	UserID         string
	MaskedNickname string
	AvatarURL      string
	AmountFen      int64
	BidAtUnixMs    int64
}

// RuntimeState is the complete pure model of the Redis auction state used by lifecycle decisions.
type RuntimeState struct {
	Config            RuntimeConfigSnapshot
	Status            v1.LotStatus
	Version           int64
	CurrentPriceFen   int64
	LeadingUserID     string
	LeadingNickname   string
	WinnerUserID      string
	WinnerNickname    string
	FinalPriceFen     int64
	StartedAtUnixMs   int64
	EndsAtUnixMs      int64
	SettledAtUnixMs   int64
	CancelledAtUnixMs int64
	CancelReason      string
	BidCount          int64
	ParticipantIDs    map[string]struct{}
	ExtendCount       int32
	OrderID           string
	TopRanking        []RuntimeRankingEntry
}

// RuntimeCommandMeta contains the identifiers copied unchanged into a runtime fact.
type RuntimeCommandMeta struct {
	EventID        string
	TraceID        string
	IdempotencyKey string
}

type RuntimeStartLotCommand struct {
	Meta               RuntimeCommandMeta
	Config             RuntimeConfigSnapshot
	PreviousStatus     v1.LotStatus
	PreviousLotVersion int64
	NowUnixMs          int64
}

type RuntimePlaceBidCommand struct {
	Meta         RuntimeCommandMeta
	BidID        string
	UserID       string
	Nickname     string
	AvatarURL    string
	AmountFen    int64
	Currency     string
	OrderID      string
	RankingLimit int
	NowUnixMs    int64
}

type RuntimeCancelLotCommand struct {
	Meta       RuntimeCommandMeta
	Reason     string
	OperatorID string
	NowUnixMs  int64
}

type RuntimeCloseIfExpiredCommand struct {
	Meta      RuntimeCommandMeta
	OrderID   string
	NowUnixMs int64
}

type RuntimeSyncLotConfigCommand struct {
	Meta                  RuntimeCommandMeta
	ExpectedConfigVersion int64
	NextConfig            RuntimeConfigSnapshot
	NowUnixMs             int64
}

// RuntimeDecision contains the new state and the exact fact that must be written to the Redis outbox.
type RuntimeDecision struct {
	State RuntimeState
	Fact  *v1.RuntimeFactV1
}

func DecideRuntimeStartLot(command RuntimeStartLotCommand) (RuntimeDecision, error) {
	if command.PreviousStatus != v1.LotStatus_LOT_STATUS_DRAFT && command.PreviousStatus != v1.LotStatus_LOT_STATUS_QUEUED {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeStateAlreadyExists)
	}
	if err := validateRuntimeConfig(command.Config); err != nil {
		return RuntimeDecision{}, err
	}
	if !isRedisExactNonNegative(command.PreviousLotVersion) || !isRedisExactPositive(command.NowUnixMs) || command.PreviousLotVersion == maxRedisExactInteger || command.NowUnixMs > maxRedisExactInteger-command.Config.DurationMs {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	state := RuntimeState{
		Config:          cloneRuntimeConfig(command.Config),
		Status:          v1.LotStatus_LOT_STATUS_LIVE,
		Version:         command.PreviousLotVersion + 1,
		CurrentPriceFen: command.Config.StartPriceFen,
		StartedAtUnixMs: command.NowUnixMs,
		EndsAtUnixMs:    command.NowUnixMs + command.Config.DurationMs,
		ParticipantIDs:  make(map[string]struct{}),
		TopRanking:      []RuntimeRankingEntry{},
	}
	return newRuntimeDecision(command.Meta, v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT, RuntimeState{Version: command.PreviousLotVersion}, state, nil, nil, command.NowUnixMs)
}

func DecideRuntimePlaceBid(current RuntimeState, command RuntimePlaceBidCommand) (RuntimeDecision, error) {
	state := cloneRuntimeState(current)
	if err := validateRuntimeState(state); err != nil {
		return RuntimeDecision{}, err
	}
	if !isRedisExactPositive(command.NowUnixMs) || !isRedisExactNonNegative(command.AmountFen) || strings.TrimSpace(command.BidID) == "" || strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.Nickname) == "" || strings.TrimSpace(command.Meta.IdempotencyKey) == "" || state.Version == maxRedisExactInteger || state.BidCount == maxRedisExactInteger {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	if command.RankingLimit <= 0 || command.RankingLimit > maxRuntimeRankingLimit {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	switch state.Status {
	case v1.LotStatus_LOT_STATUS_CANCELLED:
		return RuntimeDecision{}, runtimeReject(RuntimeCodeLotCancelled)
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_FAILED:
		return RuntimeDecision{}, runtimeReject(RuntimeCodeBidEnded)
	case v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED:
	default:
		return RuntimeDecision{}, runtimeReject(RuntimeCodeBidNotLive)
	}
	if state.CurrentPriceFen > maxRedisExactInteger-state.Config.MinIncrementFen {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	minimumBid := state.CurrentPriceFen + state.Config.MinIncrementFen
	if state.EndsAtUnixMs > 0 && command.NowUnixMs >= state.EndsAtUnixMs {
		return RuntimeDecision{}, &RuntimeDecisionError{Code: RuntimeCodeBidEnded, EndsAtUnixMs: state.EndsAtUnixMs, CurrentPrice: state.CurrentPriceFen, MinimumBid: minimumBid}
	}
	if command.Currency != state.Config.Currency {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeBidCurrencyMismatch)
	}
	if state.LeadingUserID == command.UserID {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeBidAlreadyLeading)
	}
	if command.AmountFen < minimumBid {
		return RuntimeDecision{}, &RuntimeDecisionError{Code: RuntimeCodeBidTooLow, EndsAtUnixMs: state.EndsAtUnixMs, CurrentPrice: state.CurrentPriceFen, MinimumBid: minimumBid}
	}

	previous := cloneRuntimeState(state)
	state.Version++
	state.CurrentPriceFen = command.AmountFen
	state.LeadingUserID = command.UserID
	state.LeadingNickname = command.Nickname
	state.BidCount++
	if state.ParticipantIDs == nil {
		state.ParticipantIDs = make(map[string]struct{})
	}
	state.ParticipantIDs[command.UserID] = struct{}{}
	state.TopRanking = updateRuntimeRanking(state.TopRanking, command, command.RankingLimit)

	var orderDraft *v1.OrderDraftV1
	if state.Config.CapPriceFen != nil && command.AmountFen >= *state.Config.CapPriceFen {
		if strings.TrimSpace(command.OrderID) == "" {
			return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
		}
		settleRuntimeState(&state, command.UserID, command.Nickname, command.AmountFen, command.OrderID, command.NowUnixMs)
		orderDraft = runtimeOrderDraft(state, command.OrderID, command.UserID, command.Nickname, command.AmountFen, command.NowUnixMs)
	} else {
		remainingMs := state.EndsAtUnixMs - command.NowUnixMs
		if remainingMs > 0 && remainingMs <= state.Config.AntiSnipeWindowMs && state.ExtendCount < state.Config.MaxExtendCount {
			if state.EndsAtUnixMs > maxRedisExactInteger-state.Config.AntiSnipeExtendMs {
				return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
			}
			state.EndsAtUnixMs += state.Config.AntiSnipeExtendMs
			state.ExtendCount++
			state.Status = v1.LotStatus_LOT_STATUS_EXTENDED
		}
	}

	acceptedBid := &v1.AcceptedBidV1{
		BidId:            command.BidID,
		UserId:           command.UserID,
		Nickname:         command.Nickname,
		AvatarUrl:        command.AvatarURL,
		AmountFen:        command.AmountFen,
		AcceptedAtUnixMs: command.NowUnixMs,
	}
	return newRuntimeDecision(command.Meta, v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID, previous, state, acceptedBid, orderDraft, command.NowUnixMs)
}

func DecideRuntimeCancelLot(current RuntimeState, command RuntimeCancelLotCommand) (RuntimeDecision, error) {
	state := cloneRuntimeState(current)
	if err := validateRuntimeState(state); err != nil {
		return RuntimeDecision{}, err
	}
	reason := strings.TrimSpace(command.Reason)
	if !isRedisExactPositive(command.NowUnixMs) || reason == "" || strings.TrimSpace(command.OperatorID) == "" || state.Version == maxRedisExactInteger {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	if isRuntimeTerminal(state.Status) {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeAlreadyTerminal)
	}
	previous := cloneRuntimeState(state)
	state.Status = v1.LotStatus_LOT_STATUS_CANCELLED
	state.Version++
	state.CancelReason = reason
	state.CancelledAtUnixMs = command.NowUnixMs
	return newRuntimeDecision(command.Meta, v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT, previous, state, nil, nil, command.NowUnixMs)
}

func DecideRuntimeCloseIfExpired(current RuntimeState, command RuntimeCloseIfExpiredCommand) (RuntimeDecision, error) {
	state := cloneRuntimeState(current)
	if err := validateRuntimeState(state); err != nil {
		return RuntimeDecision{}, err
	}
	if !isRedisExactPositive(command.NowUnixMs) || state.Version == maxRedisExactInteger {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	if state.Status != v1.LotStatus_LOT_STATUS_LIVE && state.Status != v1.LotStatus_LOT_STATUS_EXTENDED {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeNotLive)
	}
	if state.EndsAtUnixMs <= 0 || state.EndsAtUnixMs > command.NowUnixMs {
		return RuntimeDecision{}, &RuntimeDecisionError{Code: RuntimeCodeNotExpired, EndsAtUnixMs: state.EndsAtUnixMs}
	}

	previous := cloneRuntimeState(state)
	state.Version++
	var orderDraft *v1.OrderDraftV1
	if state.LeadingUserID == "" {
		state.Status = v1.LotStatus_LOT_STATUS_FAILED
		state.CancelReason = RuntimeExpiredNoBidReason
		state.CancelledAtUnixMs = command.NowUnixMs
	} else {
		if strings.TrimSpace(command.OrderID) == "" {
			return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
		}
		settleRuntimeState(&state, state.LeadingUserID, state.LeadingNickname, state.CurrentPriceFen, command.OrderID, command.NowUnixMs)
		orderDraft = runtimeOrderDraft(state, command.OrderID, state.LeadingUserID, state.LeadingNickname, state.CurrentPriceFen, command.NowUnixMs)
	}
	return newRuntimeDecision(command.Meta, v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED, previous, state, nil, orderDraft, command.NowUnixMs)
}

func DecideRuntimeSyncLotConfig(current RuntimeState, command RuntimeSyncLotConfigCommand) (RuntimeDecision, error) {
	state := cloneRuntimeState(current)
	if err := validateRuntimeState(state); err != nil {
		return RuntimeDecision{}, err
	}
	if !isRedisExactPositive(command.NowUnixMs) {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	if command.ExpectedConfigVersion != state.Config.ConfigVersion {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeConfigVersionConflict)
	}
	if err := validateRuntimeConfig(command.NextConfig); err != nil {
		return RuntimeDecision{}, err
	}
	if state.Config.ConfigVersion == maxRedisExactInteger || command.NextConfig.ConfigVersion != state.Config.ConfigVersion+1 || command.NextConfig.LotID != state.Config.LotID || command.NextConfig.RoomID != state.Config.RoomID || command.NextConfig.MainAccountID != state.Config.MainAccountID || command.NextConfig.Title != state.Config.Title || command.NextConfig.ImageURL != state.Config.ImageURL || command.NextConfig.Currency != state.Config.Currency {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeConfigVersionConflict)
	}
	if isRuntimeTerminal(state.Status) {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeConfigFrozen)
	}
	if command.NextConfig.MaxExtendCount < state.ExtendCount || (command.NextConfig.CapPriceFen != nil && *command.NextConfig.CapPriceFen <= state.CurrentPriceFen) {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeConfigFrozen)
	}
	if state.Version == maxRedisExactInteger {
		return RuntimeDecision{}, runtimeReject(RuntimeCodeInvalidArgument)
	}
	previous := cloneRuntimeState(state)
	state.Config = cloneRuntimeConfig(command.NextConfig)
	state.Version++
	return newRuntimeDecision(command.Meta, v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG, previous, state, nil, nil, command.NowUnixMs)
}

func newRuntimeDecision(meta RuntimeCommandMeta, command v1.RuntimeCommandType, previous, next RuntimeState, acceptedBid *v1.AcceptedBidV1, orderDraft *v1.OrderDraftV1, occurredAtUnixMs int64) (RuntimeDecision, error) {
	fact := &v1.RuntimeFactV1{
		EventId:          meta.EventID,
		TraceId:          meta.TraceID,
		LotId:            next.Config.LotID,
		RoomId:           next.Config.RoomID,
		PrevLotVersion:   previous.Version,
		LotVersion:       next.Version,
		OccurredAtUnixMs: occurredAtUnixMs,
		ConfigVersion:    next.Config.ConfigVersion,
		Command:          command,
		StateAfter:       runtimeStateProto(next),
		AcceptedBid:      acceptedBid,
		OrderDraft:       orderDraft,
		IdempotencyKey:   meta.IdempotencyKey,
		SchemaVersion:    eventcontract.RuntimeSchemaVersionV1,
	}
	if err := eventcontract.ValidateRuntimeFact(fact); err != nil {
		return RuntimeDecision{}, fmt.Errorf("build runtime fact: %w", err)
	}
	return RuntimeDecision{State: next, Fact: fact}, nil
}

func runtimeStateProto(state RuntimeState) *v1.LotRuntimeStateV1 {
	ranking := make([]*v1.RuntimeRankingItemV1, 0, len(state.TopRanking))
	for index, item := range state.TopRanking {
		ranking = append(ranking, &v1.RuntimeRankingItemV1{
			Rank:           int32(index + 1),
			UserId:         item.UserID,
			MaskedNickname: item.MaskedNickname,
			AvatarUrl:      item.AvatarURL,
			AmountFen:      item.AmountFen,
			BidAtUnixMs:    item.BidAtUnixMs,
		})
	}
	return &v1.LotRuntimeStateV1{
		LotId:             state.Config.LotID,
		RoomId:            state.Config.RoomID,
		Status:            state.Status,
		Currency:          state.Config.Currency,
		StartPriceFen:     state.Config.StartPriceFen,
		MinIncrementFen:   state.Config.MinIncrementFen,
		CapPriceFen:       cloneInt64Pointer(state.Config.CapPriceFen),
		CurrentPriceFen:   state.CurrentPriceFen,
		LeadingUserId:     state.LeadingUserID,
		LeadingNickname:   state.LeadingNickname,
		WinnerUserId:      state.WinnerUserID,
		WinnerNickname:    state.WinnerNickname,
		FinalPriceFen:     state.FinalPriceFen,
		StartedAtUnixMs:   state.StartedAtUnixMs,
		EndsAtUnixMs:      state.EndsAtUnixMs,
		SettledAtUnixMs:   state.SettledAtUnixMs,
		CancelledAtUnixMs: state.CancelledAtUnixMs,
		CancelReason:      state.CancelReason,
		BidCount:          state.BidCount,
		ParticipantCount:  int64(len(state.ParticipantIDs)),
		ExtendCount:       state.ExtendCount,
		MaxExtendCount:    state.Config.MaxExtendCount,
		OrderId:           state.OrderID,
		TopRanking:        ranking,
		DurationMs:        cloneInt64Pointer(&state.Config.DurationMs),
		AntiSnipeWindowMs: cloneInt64Pointer(&state.Config.AntiSnipeWindowMs),
		AntiSnipeExtendMs: cloneInt64Pointer(&state.Config.AntiSnipeExtendMs),
	}
}

func runtimeOrderDraft(state RuntimeState, orderID, buyerID, buyerNickname string, amountFen, nowUnixMs int64) *v1.OrderDraftV1 {
	return &v1.OrderDraftV1{
		OrderId:         orderID,
		LotId:           state.Config.LotID,
		RoomId:          state.Config.RoomID,
		MainAccountId:   state.Config.MainAccountID,
		BuyerUserId:     buyerID,
		BuyerNickname:   buyerNickname,
		Title:           state.Config.Title,
		ImageUrl:        state.Config.ImageURL,
		TotalAmountFen:  amountFen,
		Currency:        state.Config.Currency,
		CreatedAtUnixMs: nowUnixMs,
	}
}

func settleRuntimeState(state *RuntimeState, userID, nickname string, amountFen int64, orderID string, nowUnixMs int64) {
	state.Status = v1.LotStatus_LOT_STATUS_SETTLED
	state.WinnerUserID = userID
	state.WinnerNickname = nickname
	state.FinalPriceFen = amountFen
	state.SettledAtUnixMs = nowUnixMs
	state.OrderID = orderID
}

func updateRuntimeRanking(current []RuntimeRankingEntry, command RuntimePlaceBidCommand, limit int) []RuntimeRankingEntry {
	next := append([]RuntimeRankingEntry(nil), current...)
	entry := RuntimeRankingEntry{
		UserID:         command.UserID,
		MaskedNickname: MaskBuyerNickname(command.Nickname),
		AvatarURL:      command.AvatarURL,
		AmountFen:      command.AmountFen,
		BidAtUnixMs:    command.NowUnixMs,
	}
	replaced := false
	for index := range next {
		if next[index].UserID == command.UserID {
			next[index] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		next = append(next, entry)
	}
	sort.Slice(next, func(left, right int) bool {
		if next[left].AmountFen != next[right].AmountFen {
			return next[left].AmountFen > next[right].AmountFen
		}
		if next[left].BidAtUnixMs != next[right].BidAtUnixMs {
			return next[left].BidAtUnixMs < next[right].BidAtUnixMs
		}
		return next[left].UserID < next[right].UserID
	})
	if len(next) > limit {
		next = next[:limit]
	}
	return next
}

func validateRuntimeConfig(config RuntimeConfigSnapshot) error {
	if strings.TrimSpace(config.LotID) == "" || strings.TrimSpace(config.RoomID) == "" || strings.TrimSpace(config.MainAccountID) == "" || strings.TrimSpace(config.Title) == "" || !isRedisExactPositive(config.ConfigVersion) || !isRedisExactNonNegative(config.StartPriceFen) || !isRedisExactPositive(config.MinIncrementFen) || !isRedisExactPositive(config.DurationMs) || !isRedisExactNonNegative(config.AntiSnipeWindowMs) || !isRedisExactNonNegative(config.AntiSnipeExtendMs) || config.MaxExtendCount < 0 {
		return runtimeReject(RuntimeCodeInvalidArgument)
	}
	if !validRuntimeCurrency(config.Currency) || config.StartPriceFen > maxRedisExactInteger-config.MinIncrementFen {
		return runtimeReject(RuntimeCodeInvalidArgument)
	}
	if config.CapPriceFen != nil && (!isRedisExactNonNegative(*config.CapPriceFen) || *config.CapPriceFen < config.StartPriceFen) {
		return runtimeReject(RuntimeCodeInvalidArgument)
	}
	return nil
}

func validateRuntimeState(state RuntimeState) error {
	if err := validateRuntimeConfig(state.Config); err != nil {
		return err
	}
	if !validRuntimeStatus(state.Status) || !isRedisExactNonNegative(state.Version) || !isRedisExactNonNegative(state.CurrentPriceFen) || !isRedisExactNonNegative(state.StartedAtUnixMs) || !isRedisExactNonNegative(state.EndsAtUnixMs) || !isRedisExactNonNegative(state.SettledAtUnixMs) || !isRedisExactNonNegative(state.CancelledAtUnixMs) || !isRedisExactNonNegative(state.FinalPriceFen) || !isRedisExactNonNegative(state.BidCount) || state.ExtendCount < 0 || state.ExtendCount > state.Config.MaxExtendCount || len(state.TopRanking) > maxRuntimeRankingLimit {
		return runtimeReject(RuntimeCodeInvalidArgument)
	}
	for _, item := range state.TopRanking {
		if strings.TrimSpace(item.UserID) == "" || !isRedisExactNonNegative(item.AmountFen) || !isRedisExactPositive(item.BidAtUnixMs) {
			return runtimeReject(RuntimeCodeInvalidArgument)
		}
	}
	return nil
}

func cloneRuntimeState(state RuntimeState) RuntimeState {
	cloned := state
	cloned.Config = cloneRuntimeConfig(state.Config)
	cloned.TopRanking = append([]RuntimeRankingEntry(nil), state.TopRanking...)
	cloned.ParticipantIDs = make(map[string]struct{}, len(state.ParticipantIDs))
	for userID := range state.ParticipantIDs {
		cloned.ParticipantIDs[userID] = struct{}{}
	}
	return cloned
}

func cloneRuntimeConfig(config RuntimeConfigSnapshot) RuntimeConfigSnapshot {
	cloned := config
	cloned.CapPriceFen = cloneInt64Pointer(config.CapPriceFen)
	return cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func isRuntimeTerminal(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

func runtimeReject(code string) error {
	return &RuntimeDecisionError{Code: code}
}

func isRedisExactNonNegative(value int64) bool {
	return value >= 0 && value <= maxRedisExactInteger
}

func isRedisExactPositive(value int64) bool {
	return value > 0 && value <= maxRedisExactInteger
}

func validRuntimeCurrency(value string) bool {
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

func validRuntimeStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_DRAFT,
		v1.LotStatus_LOT_STATUS_READY,
		v1.LotStatus_LOT_STATUS_QUEUED,
		v1.LotStatus_LOT_STATUS_LIVE,
		v1.LotStatus_LOT_STATUS_EXTENDED,
		v1.LotStatus_LOT_STATUS_SETTLED,
		v1.LotStatus_LOT_STATUS_CANCELLED,
		v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}
