package data

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestModelToLotUsesColumnsAsAuthoritativeState(t *testing.T) {
	payloadLot := &v1.Lot{
		Id:            "payload-lot",
		MainAccountId: "payload-main",
		RoomId:        "payload-room",
		Title:         "payload title",
		Description:   "payload description",
		ImageUrl:      "payload.jpg",
		Status:        v1.LotStatus_LOT_STATUS_READY,
		QueueStatus:   v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED,
		QueuePosition: 9,
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 1, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1, Currency: "CNY"},
			DurationSeconds:        60,
			AntiSnipeWindowSeconds: 1,
			AntiSnipeExtendSeconds: 1,
			MaxExtendCount:         1,
		},
		CurrentPrice:    &v1.Money{Amount: 1, Currency: "CNY"},
		LeadingUserId:   "payload-buyer",
		LeadingNickname: "payload buyer",
		Version:         3,
		PlaybookStage:   v1.PlaybookStage_PLAYBOOK_STAGE_WARM_UP,
		Stock:           1,
	}
	payload, err := protojson.Marshal(payloadLot)
	if err != nil {
		t.Fatalf("marshal payload lot: %v", err)
	}

	now := time.UnixMilli(1718000000000)
	model := &AuctionLotModel{
		ID:                     "lot-db",
		MainAccountID:          "main-db",
		RoomID:                 "room-db",
		Title:                  "column title",
		Description:            "column description",
		ImageURL:               "column.jpg",
		Status:                 int32(v1.LotStatus_LOT_STATUS_LIVE),
		QueueStatus:            int32(v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE),
		QueuePosition:          0,
		Currency:               "CNY",
		StartPriceAmount:       1000,
		MinIncrementAmount:     500,
		DurationSeconds:        300,
		AntiSnipeWindowSeconds: 10,
		AntiSnipeExtendSeconds: 20,
		MaxExtendCount:         5,
		CurrentPriceAmount:     2500,
		LeadingUserID:          "buyer-db",
		LeadingNickname:        "buyer db",
		StartedAtUnixMs:        1718000000100,
		EndsAtUnixMs:           1718000300000,
		Version:                18,
		PlaybookStage:          int32(v1.PlaybookStage_PLAYBOOK_STAGE_BIDDING_ACTIVE),
		Payload:                string(payload),
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	lot, err := modelToLot(model)
	if err != nil {
		t.Fatalf("modelToLot: %v", err)
	}

	if lot.Id != model.ID || lot.MainAccountId != model.MainAccountID || lot.RoomId != model.RoomID {
		t.Fatalf("identity should come from columns, got id=%q main=%q room=%q", lot.Id, lot.MainAccountId, lot.RoomId)
	}
	if lot.Status != v1.LotStatus_LOT_STATUS_LIVE || lot.Version != model.Version {
		t.Fatalf("state should come from columns, got status=%s version=%d", lot.Status, lot.Version)
	}
	if lot.QueueStatus != v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE || lot.QueuePosition != 0 {
		t.Fatalf("queue should come from columns, got status=%s position=%d", lot.QueueStatus, lot.QueuePosition)
	}
	if got := lot.GetCurrentPrice().GetAmount(); got != model.CurrentPriceAmount {
		t.Fatalf("current price should come from columns, got %d", got)
	}
	if lot.LeadingUserId != model.LeadingUserID || lot.LeadingNickname != model.LeadingNickname {
		t.Fatalf("leading user should come from columns, got %q/%q", lot.LeadingUserId, lot.LeadingNickname)
	}
	if lot.GetRule().GetDurationSeconds() != model.DurationSeconds || lot.GetRule().GetMinIncrement().GetAmount() != model.MinIncrementAmount {
		t.Fatalf("rule should come from columns, got duration=%d increment=%d", lot.GetRule().GetDurationSeconds(), lot.GetRule().GetMinIncrement().GetAmount())
	}
}

func TestAuthoritativeRuntimeLotRejectsIdentityAndVersionRegression(t *testing.T) {
	base := &v1.Lot{Id: "lot-1", RoomId: "room-1", MainAccountId: "main-1", Version: 7}
	overlay, err := authoritativeRuntimeLot(base, map[string]string{
		"lot_id": "lot-1", "room_id": "room-1", "main_account_id": "main-1", "version": "8",
		"status": "5", "current_price_fen": "12000", "currency": "CNY",
	})
	if err != nil || overlay.GetVersion() != 8 || overlay.GetCurrentPrice().GetAmount() != 12_000 {
		t.Fatalf("overlay=%+v error=%v", overlay, err)
	}
	if _, err := authoritativeRuntimeLot(base, map[string]string{"lot_id": "other", "version": "8"}); err == nil {
		t.Fatal("runtime identity fork was accepted")
	}
	if _, err := authoritativeRuntimeLot(base, map[string]string{"lot_id": "lot-1", "version": "6"}); err == nil {
		t.Fatal("runtime version regression was accepted")
	}
}

func TestUnifiedLotCurrencyRejectsMixedAndInvalidCurrencies(t *testing.T) {
	if currency, err := unifiedLotCurrency(nil, &v1.Money{}); err != nil || currency != "CNY" {
		t.Fatalf("default currency=%q error=%v", currency, err)
	}
	if currency, err := unifiedLotCurrency(
		&v1.Money{Currency: "CNY"}, &v1.Money{Currency: "CNY"},
	); err != nil || currency != "CNY" {
		t.Fatalf("currency=%q error=%v", currency, err)
	}
	if _, err := unifiedLotCurrency(&v1.Money{Currency: "CNY"}, &v1.Money{Currency: "USD"}); err == nil {
		t.Fatal("mixed lot currencies were accepted")
	}
	if _, err := unifiedLotCurrency(&v1.Money{Currency: "cny"}); err == nil {
		t.Fatal("lowercase lot currency was accepted")
	}
}

func TestLotToModelWrapsCurrencyValidationAsInvalidArgument(t *testing.T) {
	lot := &v1.Lot{
		Id:            "lot-1",
		MainAccountId: "main-1",
		RoomId:        "room-1",
		Title:         "mixed currency draft",
		ImageUrl:      "https://example.test/lot.jpg",
		Status:        v1.LotStatus_LOT_STATUS_DRAFT,
		Version:       1,
		ConfigVersion: 1,
		Rule: &v1.BidRule{
			StartPrice:   &v1.Money{Amount: 10_000, Currency: "CNY"},
			MinIncrement: &v1.Money{Amount: 100, Currency: "USD"},
		},
	}

	if _, err := lotToModel(lot); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("lotToModel error=%v want invalid argument", err)
	}
}

func TestPreStartLotSaveGuardRejectsRuntimeAndQueueWrites(t *testing.T) {
	request := &v1.Lot{
		Id: "lot-1", MainAccountId: "main-1", Status: v1.LotStatus_LOT_STATUS_DRAFT,
		QueueStatus: v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE, Version: 8,
	}
	if err := validatePreStartLotSaveRequest(request, 7); err != nil {
		t.Fatalf("valid pre-start request rejected: %v", err)
	}

	runtimeRequest := proto.Clone(request).(*v1.Lot)
	runtimeRequest.Status = v1.LotStatus_LOT_STATUS_LIVE
	if err := validatePreStartLotSaveRequest(runtimeRequest, 7); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("runtime save error=%v want invalid argument", err)
	}
	queuedRequest := proto.Clone(request).(*v1.Lot)
	queuedRequest.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED
	if err := validatePreStartLotSaveRequest(queuedRequest, 7); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("queued save error=%v want invalid argument", err)
	}
	badVersionRequest := proto.Clone(request).(*v1.Lot)
	badVersionRequest.Version = 9
	if err := validatePreStartLotSaveRequest(badVersionRequest, 7); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("multi-version save error=%v want invalid argument", err)
	}
}

func TestPreStartLotUpdateAdvancesBothVersionsAndWhitelistsColumns(t *testing.T) {
	current := &AuctionLotModel{
		ID: "lot-1", MainAccountID: "main-1", RoomID: "room-1",
		Status: int32(v1.LotStatus_LOT_STATUS_DRAFT), QueueStatus: int32(v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE),
		Version: 7, ConfigVersion: 3,
	}
	next := *current
	next.RoomID = "room-2"
	next.Version = 8
	next.ConfigVersion = 4
	if err := validatePreStartLotUpdate(current, &next, 7); err != nil {
		t.Fatalf("valid pre-start update rejected: %v", err)
	}

	wrongConfigVersion := next
	wrongConfigVersion.ConfigVersion = 3
	if err := validatePreStartLotUpdate(current, &wrongConfigVersion, 7); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("config version error=%v want invalid argument", err)
	}
	wrongStatus := next
	wrongStatus.Status = int32(v1.LotStatus_LOT_STATUS_LIVE)
	if err := validatePreStartLotUpdate(current, &wrongStatus, 7); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("status mutation error=%v want invalid argument", err)
	}

	updates := preStartLotUpdateColumns(&next)
	for _, required := range []string{"room_id", "title", "current_price_amount", "version", "config_version", "payload"} {
		if _, ok := updates[required]; !ok {
			t.Fatalf("required pre-start column %q missing from %+v", required, updates)
		}
	}
	for _, forbidden := range []string{"status", "queue_status", "queue_position", "leading_user_id", "started_at_unix_ms", "ends_at_unix_ms", "settled_at_unix_ms", "winner_user_id", "playbook_stage"} {
		if _, ok := updates[forbidden]; ok {
			t.Fatalf("runtime/queue column %q must not be writable through generic save", forbidden)
		}
	}
}

func TestQueueLotUpdateWhitelistsPreStartQueueColumns(t *testing.T) {
	updates := queueLotUpdateColumns(&AuctionLotModel{Status: 6, QueueStatus: 3, QueuePosition: 2, Version: 4})
	for _, required := range []string{"status", "queue_status", "queue_position", "current_price_amount", "final_price_amount", "version", "payload"} {
		if _, ok := updates[required]; !ok {
			t.Fatalf("required queue column %q missing from %+v", required, updates)
		}
	}
	for _, forbidden := range []string{"config_version", "leading_user_id", "started_at_unix_ms", "ends_at_unix_ms", "settled_at_unix_ms", "winner_user_id", "playbook_stage"} {
		if _, ok := updates[forbidden]; ok {
			t.Fatalf("runtime/config column %q must not be writable through queue transition", forbidden)
		}
	}
}
