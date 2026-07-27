package eventcontract

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func TestNewEventIDProducesCanonicalUUIDv7(t *testing.T) {
	first, err := NewEventID()
	if err != nil {
		t.Fatalf("NewEventID: %v", err)
	}
	second, err := NewEventID()
	if err != nil {
		t.Fatalf("NewEventID second: %v", err)
	}
	if first == second {
		t.Fatal("event IDs must be unique")
	}
	parsed, err := uuid.Parse(first)
	if err != nil {
		t.Fatalf("parse UUIDv7: %v", err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("version=%d want=7", parsed.Version())
	}
	if err := ValidateEventID(first); err != nil {
		t.Fatalf("ValidateEventID: %v", err)
	}
}

func TestValidateEventIDRejectsNonV7AndNonCanonicalValues(t *testing.T) {
	for _, value := range []string{"", uuid.NewString(), "{018f22f2-c640-7f5a-8c8a-9af2b3459e71}"} {
		if err := ValidateEventID(value); !errors.Is(err, ErrInvalidEventID) {
			t.Fatalf("ValidateEventID(%q) error=%v want ErrInvalidEventID", value, err)
		}
	}
}

func TestDomainMessageIDIsDeterministicAndScopedByType(t *testing.T) {
	eventID, err := NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DomainMessageID(eventID, "order.created.v1")
	if err != nil {
		t.Fatalf("DomainMessageID: %v", err)
	}
	want := eventID + ":order.created.v1"
	if got != want {
		t.Fatalf("message ID=%q want=%q", got, want)
	}
	if _, err := DomainMessageID(eventID, "Order Created"); !errors.Is(err, ErrInvalidMessageID) {
		t.Fatalf("invalid event type error=%v want ErrInvalidMessageID", err)
	}
}

func TestHashesAreDeterministicAndUseSpecifiedWidths(t *testing.T) {
	state := validState()
	first, err := CanonicalStateHash(state)
	if err != nil {
		t.Fatalf("CanonicalStateHash: %v", err)
	}
	second, err := CanonicalStateHash(state)
	if err != nil {
		t.Fatalf("CanonicalStateHash second: %v", err)
	}
	if first != second || len(first) != 32 {
		t.Fatalf("canonical hashes first=%q second=%q", first, second)
	}
	payloadHash, err := PayloadHash(validFact(t))
	if err != nil {
		t.Fatalf("PayloadHash: %v", err)
	}
	if len(payloadHash) != 64 {
		t.Fatalf("payload hash length=%d want=64", len(payloadHash))
	}
	state.CurrentPriceFen++
	changed, err := CanonicalStateHash(state)
	if err != nil {
		t.Fatalf("CanonicalStateHash changed: %v", err)
	}
	if changed == first {
		t.Fatal("state change must change canonical hash")
	}
}

func TestLotStateContentHashExcludesEnvelopeAndDetectsDocumentChanges(t *testing.T) {
	event := &v1.LotStateDomainEventV1{
		Metadata: &v1.DomainEventMetadataV1{MessageId: "message-1"},
		LotId:    "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: 3,
		Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "Jade vase", Currency: "CNY", CurrentPriceFen: 12_000,
	}
	first, err := LotStateContentHash(event)
	if err != nil {
		t.Fatalf("LotStateContentHash: %v", err)
	}
	clone := proto.Clone(event).(*v1.LotStateDomainEventV1)
	clone.Metadata.MessageId = "message-2"
	clone.ContentHash = strings.Repeat("f", 64)
	second, err := LotStateContentHash(clone)
	if err != nil {
		t.Fatalf("LotStateContentHash envelope change: %v", err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("hashes first=%q second=%q", first, second)
	}
	clone.Title = "Different title"
	changed, err := LotStateContentHash(clone)
	if err != nil {
		t.Fatalf("LotStateContentHash content change: %v", err)
	}
	if changed == first {
		t.Fatal("search document change must change content hash")
	}
	if _, err := LotStateContentHash(nil); err == nil {
		t.Fatal("nil lot state event was accepted")
	}
}

func TestValidateLotStateDomainEvent(t *testing.T) {
	event := &v1.LotStateDomainEventV1{
		LotId: "lot-1", RoomId: "room-1", MainAccountId: "merchant-1", LotVersion: 3,
		Status: v1.LotStatus_LOT_STATUS_LIVE, Title: "Jade vase", Description: "Vintage",
		Category: "jewelry", Tags: []string{"jade"}, Currency: "CNY", StartPriceFen: 10_000,
		CurrentPriceFen: 12_000, StartsAtUnixMs: 100, EndsAtUnixMs: 200,
	}
	var err error
	event.ContentHash, err = LotStateContentHash(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateLotStateDomainEvent(event); err != nil {
		t.Fatalf("ValidateLotStateDomainEvent: %v", err)
	}
	for name, mutate := range map[string]func(*v1.LotStateDomainEventV1){
		"price": func(value *v1.LotStateDomainEventV1) { value.CurrentPriceFen = 1 },
		"title": func(value *v1.LotStateDomainEventV1) { value.Title = "" },
		"hash":  func(value *v1.LotStateDomainEventV1) { value.ContentHash = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := proto.Clone(event).(*v1.LotStateDomainEventV1)
			mutate(invalid)
			if err := ValidateLotStateDomainEvent(invalid); !errors.Is(err, ErrInvalidLotStateDomain) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := ValidateLotStateDomainEvent(nil); !errors.Is(err, ErrInvalidLotStateDomain) {
		t.Fatalf("nil error=%v", err)
	}
}

func TestNextConfigVersion(t *testing.T) {
	if got, err := NextConfigVersion(0); err != nil || got != 1 {
		t.Fatalf("NextConfigVersion(0)=(%d,%v) want=(1,nil)", got, err)
	}
	if got, err := NextConfigVersion(41); err != nil || got != 42 {
		t.Fatalf("NextConfigVersion(41)=(%d,%v) want=(42,nil)", got, err)
	}
	if _, err := NextConfigVersion(-1); err == nil {
		t.Fatal("negative config version should fail")
	}
	if _, err := NextConfigVersion(math.MaxInt64); !errors.Is(err, ErrConfigVersionExhausted) {
		t.Fatalf("max config version error=%v want ErrConfigVersionExhausted", err)
	}
}

func TestRuntimeOrderIDIsStableAndLotScoped(t *testing.T) {
	first, err := RuntimeOrderID("lot-1")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := RuntimeOrderID(" lot-1 ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RuntimeOrderID("lot-2")
	if err != nil {
		t.Fatal(err)
	}
	if first != replayed || first == second || !strings.HasPrefix(first, "order_") || len(first) != len("order_")+32 {
		t.Fatalf("first=%q replayed=%q second=%q", first, replayed, second)
	}
	if _, err := RuntimeOrderID(" "); err == nil {
		t.Fatal("blank lot id was accepted")
	}
}

func TestValidateRuntimeFact(t *testing.T) {
	if err := ValidateRuntimeFact(validFact(t)); err != nil {
		t.Fatalf("ValidateRuntimeFact: %v", err)
	}
}

func TestValidateRuntimeFactRejectsContractViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1.RuntimeFactV1)
		want   error
	}{
		{name: "unsupported schema", mutate: func(fact *v1.RuntimeFactV1) { fact.SchemaVersion = 2 }, want: ErrUnsupportedSchema},
		{name: "version gap", mutate: func(fact *v1.RuntimeFactV1) { fact.LotVersion++ }, want: ErrInvalidRuntimeFact},
		{name: "missing state", mutate: func(fact *v1.RuntimeFactV1) { fact.StateAfter = nil }, want: ErrInvalidRuntimeFact},
		{name: "identity mismatch", mutate: func(fact *v1.RuntimeFactV1) { fact.StateAfter.LotId = "other" }, want: ErrInvalidRuntimeFact},
		{name: "invalid currency", mutate: func(fact *v1.RuntimeFactV1) { fact.StateAfter.Currency = "cny" }, want: ErrInvalidRuntimeFact},
		{name: "invalid duration", mutate: func(fact *v1.RuntimeFactV1) { zero := int64(0); fact.StateAfter.DurationMs = &zero }, want: ErrInvalidRuntimeFact},
		{name: "missing duration", mutate: func(fact *v1.RuntimeFactV1) { fact.StateAfter.DurationMs = nil }, want: ErrInvalidRuntimeFact},
		{name: "invalid anti snipe window", mutate: func(fact *v1.RuntimeFactV1) { negative := int64(-1); fact.StateAfter.AntiSnipeWindowMs = &negative }, want: ErrInvalidRuntimeFact},
		{name: "invalid anti snipe extension", mutate: func(fact *v1.RuntimeFactV1) { negative := int64(-1); fact.StateAfter.AntiSnipeExtendMs = &negative }, want: ErrInvalidRuntimeFact},
		{name: "missing accepted bid", mutate: func(fact *v1.RuntimeFactV1) { fact.AcceptedBid = nil }, want: ErrInvalidRuntimeFact},
		{name: "accepted bid mismatch", mutate: func(fact *v1.RuntimeFactV1) { fact.AcceptedBid.AmountFen++ }, want: ErrInvalidRuntimeFact},
		{name: "missing bid idempotency", mutate: func(fact *v1.RuntimeFactV1) { fact.IdempotencyKey = "" }, want: ErrInvalidRuntimeFact},
		{name: "invalid live window", mutate: func(fact *v1.RuntimeFactV1) { fact.StateAfter.EndsAtUnixMs = fact.StateAfter.StartedAtUnixMs }, want: ErrInvalidRuntimeFact},
		{name: "participant count exceeds bids", mutate: func(fact *v1.RuntimeFactV1) { fact.StateAfter.ParticipantCount = fact.StateAfter.BidCount + 1 }, want: ErrInvalidRuntimeFact},
		{name: "wrong command result", mutate: func(fact *v1.RuntimeFactV1) { fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT }, want: ErrInvalidRuntimeFact},
		{name: "oversized lot id", mutate: func(fact *v1.RuntimeFactV1) {
			fact.LotId = strings.Repeat("x", 65)
			fact.StateAfter.LotId = fact.LotId
		}, want: ErrInvalidRuntimeFact},
		{name: "oversized payload", mutate: func(fact *v1.RuntimeFactV1) { fact.IdempotencyKey = strings.Repeat("x", MaxRuntimeFactBytes) }, want: ErrInvalidRuntimeFact},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fact := validFact(t)
			tc.mutate(fact)
			if err := ValidateRuntimeFact(fact); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	if err := ValidateRuntimeFact(nil); !errors.Is(err, ErrInvalidRuntimeFact) {
		t.Fatalf("nil error=%v want ErrInvalidRuntimeFact", err)
	}
}

func TestValidateRuntimeFactAcceptsSettlementAndRejectsOrderContradictions(t *testing.T) {
	fact := validFact(t)
	fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED
	fact.AcceptedBid = nil
	fact.IdempotencyKey = ""
	fact.StateAfter.Status = v1.LotStatus_LOT_STATUS_SETTLED
	fact.StateAfter.WinnerUserId = "user-1"
	fact.StateAfter.WinnerNickname = "User 1"
	fact.StateAfter.FinalPriceFen = fact.StateAfter.CurrentPriceFen
	fact.StateAfter.SettledAtUnixMs = fact.OccurredAtUnixMs
	fact.StateAfter.OrderId = "order-1"
	fact.OrderDraft = &v1.OrderDraftV1{
		OrderId:         "order-1",
		LotId:           fact.LotId,
		RoomId:          fact.RoomId,
		MainAccountId:   "merchant-1",
		BuyerUserId:     "user-1",
		BuyerNickname:   "User 1",
		Title:           "Lot 1",
		ImageUrl:        "https://example.test/lot.png",
		TotalAmountFen:  fact.StateAfter.FinalPriceFen,
		Currency:        fact.StateAfter.Currency,
		CreatedAtUnixMs: fact.OccurredAtUnixMs,
	}
	if err := ValidateRuntimeFact(fact); err != nil {
		t.Fatalf("valid settlement: %v", err)
	}

	missing := proto.Clone(fact).(*v1.RuntimeFactV1)
	missing.OrderDraft = nil
	if err := ValidateRuntimeFact(missing); !errors.Is(err, ErrInvalidRuntimeFact) {
		t.Fatalf("missing order error=%v", err)
	}
	mismatch := proto.Clone(fact).(*v1.RuntimeFactV1)
	mismatch.OrderDraft.BuyerUserId = "other"
	if err := ValidateRuntimeFact(mismatch); !errors.Is(err, ErrInvalidRuntimeFact) {
		t.Fatalf("mismatched order error=%v", err)
	}
}

func TestValidateRuntimeFactAcceptsEveryNonBidLifecycleShape(t *testing.T) {
	base := func() *v1.RuntimeFactV1 {
		fact := validFact(t)
		fact.AcceptedBid = nil
		fact.IdempotencyKey = ""
		return fact
	}
	tests := []struct {
		name  string
		build func() *v1.RuntimeFactV1
	}{
		{name: "start", build: func() *v1.RuntimeFactV1 {
			fact := base()
			fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_START_LOT
			fact.StateAfter.CurrentPriceFen = fact.StateAfter.StartPriceFen
			fact.StateAfter.LeadingUserId = ""
			fact.StateAfter.LeadingNickname = ""
			fact.StateAfter.BidCount = 0
			fact.StateAfter.ParticipantCount = 0
			return fact
		}},
		{name: "cancel", build: func() *v1.RuntimeFactV1 {
			fact := base()
			fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CANCEL_LOT
			fact.StateAfter.Status = v1.LotStatus_LOT_STATUS_CANCELLED
			fact.StateAfter.CancelReason = "operator cancelled"
			fact.StateAfter.CancelledAtUnixMs = fact.OccurredAtUnixMs
			return fact
		}},
		{name: "close without bid", build: func() *v1.RuntimeFactV1 {
			fact := base()
			fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_CLOSE_IF_EXPIRED
			fact.StateAfter.Status = v1.LotStatus_LOT_STATUS_FAILED
			fact.StateAfter.CancelReason = "expired_without_bid"
			fact.StateAfter.CancelledAtUnixMs = fact.OccurredAtUnixMs
			return fact
		}},
		{name: "sync config", build: func() *v1.RuntimeFactV1 {
			fact := base()
			fact.Command = v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_SYNC_LOT_CONFIG
			return fact
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateRuntimeFact(test.build()); err != nil {
				t.Fatalf("ValidateRuntimeFact: %v", err)
			}
		})
	}
}

func TestValidateRuntimeFactRejectsMalformedBoundedRanking(t *testing.T) {
	fact := validFact(t)
	fact.StateAfter.TopRanking = []*v1.RuntimeRankingItemV1{{
		Rank:        2,
		UserId:      "user-1",
		AmountFen:   fact.StateAfter.CurrentPriceFen,
		BidAtUnixMs: fact.OccurredAtUnixMs,
	}}
	if err := ValidateRuntimeFact(fact); !errors.Is(err, ErrInvalidRuntimeFact) {
		t.Fatalf("bad ranking error=%v", err)
	}
	fact = validFact(t)
	fact.StateAfter.TopRanking = make([]*v1.RuntimeRankingItemV1, MaxRuntimeRankingItems+1)
	if err := ValidateRuntimeFact(fact); !errors.Is(err, ErrInvalidRuntimeFact) {
		t.Fatalf("oversized ranking error=%v", err)
	}
}

func validFact(t *testing.T) *v1.RuntimeFactV1 {
	t.Helper()
	eventID, err := NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	return &v1.RuntimeFactV1{
		EventId:          eventID,
		TraceId:          "trace-1",
		LotId:            "lot-1",
		RoomId:           "room-1",
		PrevLotVersion:   6,
		LotVersion:       7,
		OccurredAtUnixMs: 1_700_000_000_000,
		ConfigVersion:    3,
		Command:          v1.RuntimeCommandType_RUNTIME_COMMAND_TYPE_PLACE_BID,
		StateAfter:       validState(),
		AcceptedBid: &v1.AcceptedBidV1{
			BidId:            "bid-1",
			UserId:           "user-1",
			Nickname:         "User 1",
			AmountFen:        12_000,
			AcceptedAtUnixMs: 1_700_000_000_000,
		},
		IdempotencyKey: "idem-1",
		SchemaVersion:  RuntimeSchemaVersionV1,
	}
}

func validState() *v1.LotRuntimeStateV1 {
	durationMs := int64(60_000)
	antiSnipeWindowMs := int64(10_000)
	antiSnipeExtendMs := int64(30_000)
	return &v1.LotRuntimeStateV1{
		LotId:             "lot-1",
		RoomId:            "room-1",
		Status:            v1.LotStatus_LOT_STATUS_LIVE,
		Currency:          "CNY",
		StartPriceFen:     10_000,
		MinIncrementFen:   100,
		CurrentPriceFen:   12_000,
		LeadingUserId:     "user-1",
		LeadingNickname:   "User 1",
		StartedAtUnixMs:   1_699_999_940_000,
		EndsAtUnixMs:      1_700_000_060_000,
		BidCount:          4,
		ParticipantCount:  3,
		DurationMs:        &durationMs,
		AntiSnipeWindowMs: &antiSnipeWindowMs,
		AntiSnipeExtendMs: &antiSnipeExtendMs,
	}
}
