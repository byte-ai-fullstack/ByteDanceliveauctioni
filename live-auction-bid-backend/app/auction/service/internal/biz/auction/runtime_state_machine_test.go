package auction

import (
	"errors"
	"reflect"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const (
	runtimeTestEventID  = "018f22f2-c640-7f5a-8c8a-9af2b3459e71"
	runtimeTestEventID2 = "018f22f2-c641-7f5a-8c8a-9af2b3459e71"
)

func TestDecideRuntimeStartLotBuildsInitialStateAndFact(t *testing.T) {
	command := RuntimeStartLotCommand{
		Meta:               RuntimeCommandMeta{EventID: runtimeTestEventID, TraceID: "trace-start"},
		Config:             runtimeTestConfig(),
		PreviousStatus:     v1.LotStatus_LOT_STATUS_QUEUED,
		PreviousLotVersion: 4,
		NowUnixMs:          1_700_000_000_000,
	}
	decision, err := DecideRuntimeStartLot(command)
	if err != nil {
		t.Fatalf("DecideRuntimeStartLot: %v", err)
	}
	if decision.State.Status != v1.LotStatus_LOT_STATUS_LIVE || decision.State.Version != 5 {
		t.Fatalf("state status/version = %s/%d", decision.State.Status, decision.State.Version)
	}
	if decision.State.StartedAtUnixMs != command.NowUnixMs || decision.State.EndsAtUnixMs != command.NowUnixMs+command.Config.DurationMs {
		t.Fatalf("unexpected start window: %+v", decision.State)
	}
	if decision.State.CurrentPriceFen != command.Config.StartPriceFen {
		t.Fatalf("current price=%d want %d", decision.State.CurrentPriceFen, command.Config.StartPriceFen)
	}
	if decision.Fact.GetCommand() != v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT || decision.Fact.GetPrevLotVersion() != 4 || decision.Fact.GetLotVersion() != 5 {
		t.Fatalf("unexpected fact: %+v", decision.Fact)
	}
	if decision.Fact.GetStateAfter().GetParticipantCount() != 0 || len(decision.Fact.GetStateAfter().GetTopRanking()) != 0 {
		t.Fatalf("new lot must have empty participation: %+v", decision.Fact.GetStateAfter())
	}
}

func TestDecideRuntimeStartLotRejectsInvalidStateAndConfig(t *testing.T) {
	base := RuntimeStartLotCommand{
		Meta:               RuntimeCommandMeta{EventID: runtimeTestEventID},
		Config:             runtimeTestConfig(),
		PreviousStatus:     v1.LotStatus_LOT_STATUS_LIVE,
		PreviousLotVersion: 4,
		NowUnixMs:          1_700_000_000_000,
	}
	_, err := DecideRuntimeStartLot(base)
	assertRuntimeErrorCode(t, err, RuntimeCodeStateAlreadyExists)
	base.PreviousStatus = v1.LotStatus_LOT_STATUS_DRAFT
	base.Config.MinIncrementFen = 0
	_, err = DecideRuntimeStartLot(base)
	assertRuntimeErrorCode(t, err, RuntimeCodeInvalidArgument)
}

func TestDecideRuntimePlaceBidExtendsAndPreservesInput(t *testing.T) {
	current := runtimeTestLiveState()
	current.EndsAtUnixMs = 1_700_000_005_000
	before := cloneRuntimeState(current)
	command := RuntimePlaceBidCommand{
		Meta:         RuntimeCommandMeta{EventID: runtimeTestEventID, TraceID: "trace-bid", IdempotencyKey: "idem-2"},
		BidID:        "bid-2",
		UserID:       "user-2",
		Nickname:     "乙用户",
		AvatarURL:    "https://example.com/u2.png",
		AmountFen:    12_100,
		Currency:     "CNY",
		RankingLimit: 20,
		NowUnixMs:    1_700_000_000_000,
	}
	decision, err := DecideRuntimePlaceBid(current, command)
	if err != nil {
		t.Fatalf("DecideRuntimePlaceBid: %v", err)
	}
	if !reflect.DeepEqual(current, before) {
		t.Fatalf("input state was mutated\nbefore=%+v\nafter=%+v", before, current)
	}
	if decision.State.Status != v1.LotStatus_LOT_STATUS_EXTENDED || decision.State.EndsAtUnixMs != current.EndsAtUnixMs+current.Config.AntiSnipeExtendMs || decision.State.ExtendCount != current.ExtendCount+1 {
		t.Fatalf("anti-snipe state mismatch: %+v", decision.State)
	}
	if decision.State.Version != current.Version+1 || decision.State.BidCount != current.BidCount+1 || len(decision.State.ParticipantIDs) != 2 {
		t.Fatalf("version/count mismatch: %+v", decision.State)
	}
	if len(decision.State.TopRanking) != 2 || decision.State.TopRanking[0].UserID != command.UserID || decision.State.TopRanking[0].MaskedNickname != "乙***" {
		t.Fatalf("ranking mismatch: %+v", decision.State.TopRanking)
	}
	if decision.Fact.GetAcceptedBid().GetBidId() != command.BidID || decision.Fact.GetStateAfter().GetParticipantCount() != 2 {
		t.Fatalf("fact mismatch: %+v", decision.Fact)
	}
}

func TestDecideRuntimePlaceBidAtCapSettlesWithOrderDraft(t *testing.T) {
	current := runtimeTestLiveState()
	capPrice := int64(13_000)
	current.Config.CapPriceFen = &capPrice
	decision, err := DecideRuntimePlaceBid(current, RuntimePlaceBidCommand{
		Meta:         RuntimeCommandMeta{EventID: runtimeTestEventID, IdempotencyKey: "idem-cap"},
		BidID:        "bid-cap",
		UserID:       "buyer-cap",
		Nickname:     "封顶买家",
		AmountFen:    capPrice,
		Currency:     "CNY",
		OrderID:      "order-cap",
		RankingLimit: 10,
		NowUnixMs:    1_700_000_000_000,
	})
	if err != nil {
		t.Fatalf("DecideRuntimePlaceBid cap: %v", err)
	}
	if decision.State.Status != v1.LotStatus_LOT_STATUS_SETTLED || decision.State.OrderID != "order-cap" || decision.State.FinalPriceFen != capPrice {
		t.Fatalf("settlement state mismatch: %+v", decision.State)
	}
	if decision.Fact.GetOrderDraft().GetBuyerUserId() != "buyer-cap" || decision.Fact.GetOrderDraft().GetTotalAmountFen() != capPrice {
		t.Fatalf("order draft mismatch: %+v", decision.Fact.GetOrderDraft())
	}
}

func TestDecideRuntimePlaceBidRejectionsDoNotMutateState(t *testing.T) {
	base := runtimeTestLiveState()
	tests := []struct {
		name     string
		mutate   func(*RuntimePlaceBidCommand)
		wantCode string
	}{
		{name: "invalid", mutate: func(command *RuntimePlaceBidCommand) { command.BidID = "" }, wantCode: RuntimeCodeInvalidArgument},
		{name: "ended", mutate: func(command *RuntimePlaceBidCommand) { command.NowUnixMs = base.EndsAtUnixMs }, wantCode: RuntimeCodeBidEnded},
		{name: "currency", mutate: func(command *RuntimePlaceBidCommand) { command.Currency = "USD" }, wantCode: RuntimeCodeBidCurrencyMismatch},
		{name: "leader", mutate: func(command *RuntimePlaceBidCommand) { command.UserID = base.LeadingUserID }, wantCode: RuntimeCodeBidAlreadyLeading},
		{name: "low", mutate: func(command *RuntimePlaceBidCommand) { command.AmountFen = base.CurrentPriceFen }, wantCode: RuntimeCodeBidTooLow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cloneRuntimeState(base)
			before := cloneRuntimeState(state)
			command := RuntimePlaceBidCommand{
				Meta:         RuntimeCommandMeta{EventID: runtimeTestEventID, IdempotencyKey: "idem-reject"},
				BidID:        "bid-reject",
				UserID:       "user-2",
				Nickname:     "用户乙",
				AmountFen:    12_100,
				Currency:     "CNY",
				RankingLimit: 20,
				NowUnixMs:    1_700_000_000_000,
			}
			test.mutate(&command)
			_, err := DecideRuntimePlaceBid(state, command)
			assertRuntimeErrorCode(t, err, test.wantCode)
			if !reflect.DeepEqual(state, before) {
				t.Fatalf("rejected bid mutated state\nbefore=%+v\nafter=%+v", before, state)
			}
		})
	}
}

func TestDecideRuntimePlaceBidClassifiesTerminalStateWithoutAProjectionLookup(t *testing.T) {
	command := RuntimePlaceBidCommand{
		Meta:         RuntimeCommandMeta{EventID: runtimeTestEventID, IdempotencyKey: "idem-terminal"},
		BidID:        "bid-terminal",
		UserID:       "user-2",
		Nickname:     "用户乙",
		AmountFen:    12_100,
		Currency:     "CNY",
		RankingLimit: 20,
		NowUnixMs:    1_700_000_000_000,
	}
	for _, test := range []struct {
		status   v1.LotStatus
		wantCode string
	}{
		{status: v1.LotStatus_LOT_STATUS_CANCELLED, wantCode: RuntimeCodeLotCancelled},
		{status: v1.LotStatus_LOT_STATUS_SETTLED, wantCode: RuntimeCodeBidEnded},
		{status: v1.LotStatus_LOT_STATUS_FAILED, wantCode: RuntimeCodeBidEnded},
	} {
		state := runtimeTestLiveState()
		state.Status = test.status
		_, err := DecideRuntimePlaceBid(state, command)
		assertRuntimeErrorCode(t, err, test.wantCode)
	}
}

func TestDecideRuntimeCancelLotProducesTerminalFact(t *testing.T) {
	current := runtimeTestLiveState()
	decision, err := DecideRuntimeCancelLot(current, RuntimeCancelLotCommand{
		Meta:       RuntimeCommandMeta{EventID: runtimeTestEventID},
		Reason:     "  operator cancelled  ",
		OperatorID: "operator-1",
		NowUnixMs:  1_700_000_000_000,
	})
	if err != nil {
		t.Fatalf("DecideRuntimeCancelLot: %v", err)
	}
	if decision.State.Status != v1.LotStatus_LOT_STATUS_CANCELLED || decision.State.CancelReason != "operator cancelled" || decision.State.Version != current.Version+1 {
		t.Fatalf("cancel state mismatch: %+v", decision.State)
	}
	if decision.Fact.GetCommand() != v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT || decision.Fact.GetStateAfter().GetCancelledAtUnixMs() == 0 {
		t.Fatalf("cancel fact mismatch: %+v", decision.Fact)
	}
	terminal := cloneRuntimeState(current)
	terminal.Status = v1.LotStatus_LOT_STATUS_SETTLED
	_, err = DecideRuntimeCancelLot(terminal, RuntimeCancelLotCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID}, Reason: "x", OperatorID: "op", NowUnixMs: 1})
	assertRuntimeErrorCode(t, err, RuntimeCodeAlreadyTerminal)
}

func TestDecideRuntimeCloseIfExpiredRechecksDeadlineAndSettles(t *testing.T) {
	current := runtimeTestLiveState()
	current.EndsAtUnixMs = 1_700_000_000_100
	before := cloneRuntimeState(current)
	_, err := DecideRuntimeCloseIfExpired(current, RuntimeCloseIfExpiredCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID}, OrderID: "order-close", NowUnixMs: current.EndsAtUnixMs - 1})
	assertRuntimeErrorCode(t, err, RuntimeCodeNotExpired)
	if !reflect.DeepEqual(current, before) {
		t.Fatal("not-expired decision mutated input")
	}

	decision, err := DecideRuntimeCloseIfExpired(current, RuntimeCloseIfExpiredCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID2}, OrderID: "order-close", NowUnixMs: current.EndsAtUnixMs})
	if err != nil {
		t.Fatalf("DecideRuntimeCloseIfExpired: %v", err)
	}
	if decision.State.Status != v1.LotStatus_LOT_STATUS_SETTLED || decision.State.WinnerUserID != current.LeadingUserID || decision.Fact.GetOrderDraft().GetOrderId() != "order-close" {
		t.Fatalf("close settlement mismatch: %+v", decision)
	}
}

func TestDecideRuntimeCloseIfExpiredWithoutBidFailsLot(t *testing.T) {
	current := runtimeTestLiveState()
	current.LeadingUserID = ""
	current.LeadingNickname = ""
	current.CurrentPriceFen = current.Config.StartPriceFen
	current.EndsAtUnixMs = 100
	current.TopRanking = nil
	current.ParticipantIDs = nil
	decision, err := DecideRuntimeCloseIfExpired(current, RuntimeCloseIfExpiredCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID}, NowUnixMs: 100})
	if err != nil {
		t.Fatalf("DecideRuntimeCloseIfExpired no bid: %v", err)
	}
	if decision.State.Status != v1.LotStatus_LOT_STATUS_FAILED || decision.State.CancelReason == "" || decision.Fact.GetOrderDraft() != nil {
		t.Fatalf("failed close mismatch: %+v", decision)
	}
}

func TestDecideRuntimeSyncLotConfigChangesOnlyConfigAndVersions(t *testing.T) {
	current := runtimeTestLiveState()
	nextConfig := cloneRuntimeConfig(current.Config)
	nextConfig.ConfigVersion++
	nextConfig.MinIncrementFen = 250
	nextConfig.AntiSnipeWindowMs = 20_000
	decision, err := DecideRuntimeSyncLotConfig(current, RuntimeSyncLotConfigCommand{
		Meta:                  RuntimeCommandMeta{EventID: runtimeTestEventID},
		ExpectedConfigVersion: current.Config.ConfigVersion,
		NextConfig:            nextConfig,
		NowUnixMs:             1_700_000_000_000,
	})
	if err != nil {
		t.Fatalf("DecideRuntimeSyncLotConfig: %v", err)
	}
	if decision.State.Config.MinIncrementFen != 250 || decision.State.Version != current.Version+1 || decision.Fact.GetConfigVersion() != current.Config.ConfigVersion+1 {
		t.Fatalf("sync result mismatch: %+v", decision)
	}
	if decision.State.CurrentPriceFen != current.CurrentPriceFen || decision.State.EndsAtUnixMs != current.EndsAtUnixMs || decision.State.Status != current.Status {
		t.Fatalf("sync changed bidding state: %+v", decision.State)
	}
	bad := nextConfig
	bad.ConfigVersion++
	_, err = DecideRuntimeSyncLotConfig(current, RuntimeSyncLotConfigCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID}, ExpectedConfigVersion: current.Config.ConfigVersion, NextConfig: bad, NowUnixMs: 1})
	assertRuntimeErrorCode(t, err, RuntimeCodeConfigVersionConflict)

	frozen := nextConfig
	frozen.MaxExtendCount = current.ExtendCount - 1
	_, err = DecideRuntimeSyncLotConfig(current, RuntimeSyncLotConfigCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID}, ExpectedConfigVersion: current.Config.ConfigVersion, NextConfig: frozen, NowUnixMs: 1})
	assertRuntimeErrorCode(t, err, RuntimeCodeConfigFrozen)
	capAtCurrent := current.CurrentPriceFen
	frozen = nextConfig
	frozen.CapPriceFen = &capAtCurrent
	_, err = DecideRuntimeSyncLotConfig(current, RuntimeSyncLotConfigCommand{Meta: RuntimeCommandMeta{EventID: runtimeTestEventID}, ExpectedConfigVersion: current.Config.ConfigVersion, NextConfig: frozen, NowUnixMs: 1})
	assertRuntimeErrorCode(t, err, RuntimeCodeConfigFrozen)
}

func TestRuntimeDecisionErrorNilAndStructuredDetails(t *testing.T) {
	var nilError *RuntimeDecisionError
	if nilError.Error() != "" {
		t.Fatalf("nil error text=%q", nilError.Error())
	}
	errorValue := &RuntimeDecisionError{Code: RuntimeCodeBidTooLow, CurrentPrice: 100, MinimumBid: 110}
	if errorValue.Error() != RuntimeCodeBidTooLow {
		t.Fatalf("error text=%q", errorValue.Error())
	}
}

func TestRuntimeModelRejectsIntegersRedisLuaCannotRepresentExactly(t *testing.T) {
	start := RuntimeStartLotCommand{
		Meta:               RuntimeCommandMeta{EventID: runtimeTestEventID},
		Config:             runtimeTestConfig(),
		PreviousStatus:     v1.LotStatus_LOT_STATUS_DRAFT,
		PreviousLotVersion: maxRedisExactInteger,
		NowUnixMs:          1,
	}
	_, err := DecideRuntimeStartLot(start)
	assertRuntimeErrorCode(t, err, RuntimeCodeInvalidArgument)

	state := runtimeTestLiveState()
	state.CurrentPriceFen = maxRedisExactInteger
	_, err = DecideRuntimePlaceBid(state, RuntimePlaceBidCommand{
		Meta:  RuntimeCommandMeta{EventID: runtimeTestEventID, IdempotencyKey: "idem-overflow"},
		BidID: "bid-overflow", UserID: "user-2", Nickname: "用户乙", AmountFen: maxRedisExactInteger,
		Currency: "CNY", RankingLimit: 20, NowUnixMs: 1_700_000_000_000,
	})
	assertRuntimeErrorCode(t, err, RuntimeCodeInvalidArgument)

	config := runtimeTestConfig()
	overflowCap := maxRedisExactInteger + 1
	config.CapPriceFen = &overflowCap
	start.Config = config
	start.PreviousLotVersion = 1
	_, err = DecideRuntimeStartLot(start)
	assertRuntimeErrorCode(t, err, RuntimeCodeInvalidArgument)
}

func runtimeTestConfig() RuntimeConfigSnapshot {
	return RuntimeConfigSnapshot{
		LotID:             "lot-1",
		RoomID:            "room-1",
		MainAccountID:     "main-1",
		Title:             "测试拍品",
		ImageURL:          "https://example.com/lot.png",
		ConfigVersion:     3,
		Currency:          "CNY",
		StartPriceFen:     10_000,
		MinIncrementFen:   100,
		DurationMs:        60_000,
		AntiSnipeWindowMs: 10_000,
		AntiSnipeExtendMs: 30_000,
		MaxExtendCount:    3,
	}
}

func runtimeTestLiveState() RuntimeState {
	config := runtimeTestConfig()
	return RuntimeState{
		Config:          config,
		Status:          v1.LotStatus_LOT_STATUS_LIVE,
		Version:         7,
		CurrentPriceFen: 12_000,
		LeadingUserID:   "user-1",
		LeadingNickname: "甲用户",
		StartedAtUnixMs: 1_699_999_940_000,
		EndsAtUnixMs:    1_700_000_060_000,
		BidCount:        4,
		ParticipantIDs:  map[string]struct{}{"user-1": {}},
		ExtendCount:     1,
		TopRanking: []RuntimeRankingEntry{{
			UserID:         "user-1",
			MaskedNickname: "甲***",
			AmountFen:      12_000,
			BidAtUnixMs:    1_699_999_999_000,
		}},
	}
}

func assertRuntimeErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var decisionError *RuntimeDecisionError
	if !errors.As(err, &decisionError) {
		t.Fatalf("error=%v want RuntimeDecisionError(%s)", err, want)
	}
	if decisionError.Code != want {
		t.Fatalf("code=%q want %q", decisionError.Code, want)
	}
}
