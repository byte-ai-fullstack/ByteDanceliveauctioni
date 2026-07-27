package realtime

import (
	"errors"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	auctionbiz "live-auction-bid/backend/app/auction/service/internal/biz/auction"
)

const realtimeSchemaVersion uint32 = 1

var realtimeJSONMarshal = protojson.MarshalOptions{
	UseEnumNumbers: false,
	UseProtoNames:  false,
}

// immutablePayload owns serialized bytes that are never exposed for mutation.
// The same instance can therefore be shared by every connection in a room.
type immutablePayload struct {
	data []byte
	kind string
}

type wireBatch struct {
	frames []*immutablePayload
}

type criticalFrame struct {
	frame      *immutablePayload
	closeAfter bool
	closeCode  int
	closeText  string
}

func encodeRealtimeEnvelope(messageID string, occurredAtUnixMs int64, payload any) (*immutablePayload, error) {
	envelope := &v1.RealtimeEnvelopeV1{
		MessageId:        strings.TrimSpace(messageID),
		SchemaVersion:    realtimeSchemaVersion,
		OccurredAtUnixMs: occurredAtUnixMs,
	}
	var kind string
	switch value := payload.(type) {
	case *v1.RoomSnapshotPublicV1:
		envelope.Payload = &v1.RealtimeEnvelopeV1_PublicSnapshot{PublicSnapshot: value}
		kind = "public_snapshot"
	case *v1.PersonalDeltaV1:
		envelope.Payload = &v1.RealtimeEnvelopeV1_PersonalDelta{PersonalDelta: value}
		kind = "personal_delta"
	case *v1.RoomHeartbeatV1:
		envelope.Payload = &v1.RealtimeEnvelopeV1_Heartbeat{Heartbeat: value}
		kind = "heartbeat"
	case *v1.RoomSnapshotAdminV1:
		envelope.Payload = &v1.RealtimeEnvelopeV1_AdminSnapshot{AdminSnapshot: value}
		kind = "admin_snapshot"
	case *v1.ReconnectControlV1:
		envelope.Payload = &v1.RealtimeEnvelopeV1_Reconnect{Reconnect: value}
		kind = "reconnect"
	default:
		return nil, errors.New("unsupported realtime envelope payload")
	}
	raw, err := realtimeJSONMarshal.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return &immutablePayload{data: raw, kind: kind}, nil
}

func publicSnapshotV1(snapshot *v1.RoomSnapshot) *v1.RoomSnapshotPublicV1 {
	result := &v1.RoomSnapshotPublicV1{}
	if snapshot == nil {
		return result
	}
	result.RoomId = snapshot.GetRoomId()
	lot := snapshot.GetCurrentLot()
	if lot == nil {
		return result
	}
	result.LotId = lot.GetId()
	result.LotVersion = lot.GetVersion()
	result.Status = lot.GetStatus()
	result.CurrentPriceFen = lot.GetCurrentPrice().GetAmount()
	result.EndsAtUnixMs = lot.GetEndsAtUnixMs()
	result.BidCount = lot.GetStats().GetBidCount()
	result.TopRanking = make([]*v1.PublicRankingItemV1, 0, len(snapshot.GetRanking()))
	for _, item := range snapshot.GetRanking() {
		if item == nil {
			continue
		}
		result.TopRanking = append(result.TopRanking, &v1.PublicRankingItemV1{
			Rank:            item.GetRank(),
			MaskedNickname:  auctionbiz.MaskBuyerNickname(item.GetNickname()),
			MaskedAvatarUrl: "",
			AmountFen:       item.GetAmount().GetAmount(),
			BidAtUnixMs:     item.GetBidAtUnixMs(),
		})
	}
	if isTerminalLotStatus(lot.GetStatus()) {
		settledAt := lot.GetSettledAtUnixMs()
		if settledAt == 0 {
			settledAt = lot.GetCancelledAtUnixMs()
		}
		result.Settlement = &v1.PublicSettlementV1{
			Status:               lot.GetStatus(),
			FinalPriceFen:        lot.GetFinalPrice().GetAmount(),
			MaskedWinnerNickname: auctionbiz.MaskBuyerNickname(lot.GetWinnerNickname()),
			SettledAtUnixMs:      settledAt,
			CancelReason:         lot.GetCancelReason(),
		}
	}
	return result
}

func adminSnapshotV1(snapshot *v1.RoomSnapshot) *v1.RoomSnapshotAdminV1 {
	result := &v1.RoomSnapshotAdminV1{}
	if snapshot == nil {
		return result
	}
	result.RoomId = snapshot.GetRoomId()
	lot := snapshot.GetCurrentLot()
	if lot == nil {
		return result
	}
	result.MainAccountId = lot.GetMainAccountId()
	result.LotId = lot.GetId()
	result.LotVersion = lot.GetVersion()
	result.Status = lot.GetStatus()
	result.CurrentPriceFen = lot.GetCurrentPrice().GetAmount()
	result.EndsAtUnixMs = lot.GetEndsAtUnixMs()
	result.TopRanking = make([]*v1.AdminRankingItemV1, 0, len(snapshot.GetRanking()))
	for _, item := range snapshot.GetRanking() {
		if item == nil {
			continue
		}
		result.TopRanking = append(result.TopRanking, &v1.AdminRankingItemV1{
			Rank:        item.GetRank(),
			UserId:      item.GetUserId(),
			Nickname:    item.GetNickname(),
			AvatarUrl:   item.GetAvatarUrl(),
			AmountFen:   item.GetAmount().GetAmount(),
			BidAtUnixMs: item.GetBidAtUnixMs(),
		})
	}
	return result
}

func personalDeltaV1(snapshot *v1.RoomSnapshot, userID string, eventType v1.AuctionEventType, forceTombstone bool) *v1.PersonalDeltaV1 {
	// ORDER_CREATED is emitted optimistically with the runtime fact; it does
	// not prove that the MySQL order projection is queryable. Domain relay may
	// later accelerate READY after durable publication; GET /api/rooms/{id}/me
	// remains the recovery path when that non-authoritative signal is lost.
	_ = eventType
	state, err := auctionbiz.PersonalStateForSnapshot(snapshot, userID)
	if err != nil {
		return &v1.PersonalDeltaV1{UserId: strings.TrimSpace(userID), Tombstone: true, OrderVisibility: v1.OrderVisibility_ORDER_VISIBILITY_NONE}
	}
	delta := &v1.PersonalDeltaV1{
		UserId:          state.GetUserId(),
		LotId:           state.GetLotId(),
		LotVersion:      state.GetLotVersion(),
		YourRank:        state.YourRank,
		YourAmountFen:   state.YourAmountFen,
		YouAreLeading:   state.GetYouAreLeading(),
		YourOrderId:     state.YourOrderId,
		OrderVisibility: state.GetOrderVisibility(),
		Tombstone:       state.GetTombstone(),
	}
	delta.Tombstone = forceTombstone || delta.GetTombstone()
	if delta.Tombstone {
		delta.YourRank = nil
		delta.YourAmountFen = nil
		delta.YouAreLeading = false
		delta.YourOrderId = nil
		delta.OrderVisibility = v1.OrderVisibility_ORDER_VISIBILITY_NONE
	}
	return delta
}

func privateUsers(snapshot *v1.RoomSnapshot) map[string]struct{} {
	users := make(map[string]struct{})
	if snapshot == nil || snapshot.GetCurrentLot() == nil {
		return users
	}
	for _, item := range snapshot.GetRanking() {
		if item == nil {
			continue
		}
		if userID := strings.TrimSpace(item.GetUserId()); userID != "" {
			users[userID] = struct{}{}
		}
	}
	lot := snapshot.GetCurrentLot()
	for _, userID := range []string{lot.GetLeadingUserId(), lot.GetWinnerUserId()} {
		if userID = strings.TrimSpace(userID); userID != "" {
			users[userID] = struct{}{}
		}
	}
	return users
}

func isTerminalLotStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_SETTLED, v1.LotStatus_LOT_STATUS_CANCELLED, v1.LotStatus_LOT_STATUS_FAILED:
		return true
	default:
		return false
	}
}

func cloneRoomSnapshot(snapshot *v1.RoomSnapshot) *v1.RoomSnapshot {
	if snapshot == nil {
		return nil
	}
	return proto.Clone(snapshot).(*v1.RoomSnapshot)
}

func cloneAuctionEvent(event *v1.AuctionEvent) *v1.AuctionEvent {
	if event == nil {
		return nil
	}
	return proto.Clone(event).(*v1.AuctionEvent)
}
