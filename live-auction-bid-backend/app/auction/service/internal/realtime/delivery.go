package realtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"
)

var ErrSnapshotVersionRegressed = errors.New("realtime snapshot version moved backwards")

type preparedRoomFrames struct {
	public        *immutablePayload
	admin         *immutablePayload
	mainAccountID string
	personal      map[string]*immutablePayload
}

type readyOrderState struct {
	lotID      string
	lotVersion int64
	orderID    string
}

func isOrderReadySignal(event *v1.AuctionEvent) bool {
	return event != nil &&
		event.GetType() == v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED &&
		event.GetOrderVisibility() == v1.OrderVisibility_ORDER_VISIBILITY_READY
}

func (h *Hub) deliverOrderReady(ctx context.Context, event *v1.AuctionEvent) error {
	roomID := strings.TrimSpace(event.GetRoomId())
	lotID := strings.TrimSpace(event.GetLotId())
	buyerUserID := strings.TrimSpace(event.GetBuyerUserId())
	orderID := strings.TrimSpace(event.GetOrderId())
	if roomID == "" || lotID == "" || buyerUserID == "" || orderID == "" || event.GetLotVersion() <= 0 || event.GetOccurredAtUnixMs() <= 0 || strings.TrimSpace(event.GetId()) == "" {
		return errors.New("order READY signal identity is incomplete")
	}
	expectedOrderID, err := eventcontract.RuntimeOrderID(lotID)
	if err != nil || orderID != expectedOrderID {
		return errors.New("order READY signal does not match the deterministic order identity")
	}
	connections := h.roomConnections(roomID)
	if len(connections) == 0 {
		return nil
	}
	if h.snapshot == nil {
		return errors.New("order READY signal requires an authoritative snapshot provider")
	}
	snapshot, err := h.snapshot.Snapshot(ctx, roomID)
	if err != nil {
		return fmt.Errorf("load order READY snapshot: %w", err)
	}
	lot := snapshot.GetCurrentLot()
	if lot == nil || lot.GetId() != lotID || lot.GetVersion() != event.GetLotVersion() ||
		lot.GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED || strings.TrimSpace(lot.GetWinnerUserId()) != buyerUserID {
		return errors.New("order READY signal does not match the authoritative runtime settlement")
	}
	delta := personalDeltaV1(snapshot, buyerUserID, event.GetType(), false)
	if delta.GetTombstone() || delta.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_CREATING {
		return errors.New("order READY signal cannot derive the settled buyer overlay")
	}
	delta.YourOrderId = &orderID
	delta.OrderVisibility = v1.OrderVisibility_ORDER_VISIBILITY_READY
	frame, err := encodeRealtimeEnvelope(event.GetId()+":personal-ready:"+buyerUserID, event.GetOccurredAtUnixMs(), delta)
	if err != nil {
		return err
	}
	h.rememberReadyOrder(roomID, buyerUserID, readyOrderState{lotID: lotID, lotVersion: event.GetLotVersion(), orderID: orderID})
	for _, conn := range connections {
		userID, _ := conn.viewerIdentity()
		if userID == buyerUserID {
			conn.enqueueLatest(&wireBatch{frames: []*immutablePayload{frame}})
		}
	}
	return nil
}

func (h *Hub) rememberReadyOrder(roomID, userID string, ready readyOrderState) {
	h.wireMu.Lock()
	defer h.wireMu.Unlock()
	if h.readyOrders == nil {
		h.readyOrders = make(map[string]map[string]readyOrderState)
	}
	if h.readyOrders[roomID] == nil {
		h.readyOrders[roomID] = make(map[string]readyOrderState)
	}
	h.readyOrders[roomID][userID] = ready
}

func (h *Hub) deliver(ctx context.Context, event *v1.AuctionEvent) error {
	if event == nil || strings.TrimSpace(event.GetRoomId()) == "" {
		return nil
	}
	if h.IsDraining() {
		return nil
	}
	connections := h.roomConnections(event.GetRoomId())
	if len(connections) == 0 {
		return nil
	}
	snapshot, err := h.snapshotForEvent(ctx, event)
	if err != nil {
		return err
	}
	prepared, err := h.prepareRoomFrames(snapshot, event.GetType(), event.GetId(), event.GetOccurredAtUnixMs())
	if err != nil {
		return err
	}
	h.enqueuePreparedRoomFrames(connections, prepared)
	return nil
}

func (h *Hub) enqueuePreparedRoomFrames(connections []*connection, prepared preparedRoomFrames) {
	for _, conn := range connections {
		if conn.canReceivePrivateEvents() {
			_, mainAccountID := conn.viewerIdentity()
			if mainAccountID == prepared.mainAccountID && prepared.admin != nil {
				conn.enqueueLatest(&wireBatch{frames: []*immutablePayload{prepared.admin}})
				continue
			}
		}
		frames := []*immutablePayload{prepared.public}
		userID, _ := conn.viewerIdentity()
		if personal := prepared.personal[userID]; personal != nil {
			frames = append(frames, personal)
		}
		conn.enqueueLatest(&wireBatch{frames: frames})
	}
}

func (h *Hub) snapshotForEvent(ctx context.Context, event *v1.AuctionEvent) (*v1.RoomSnapshot, error) {
	if event.GetSnapshot() != nil {
		return cloneRoomSnapshot(event.GetSnapshot()), nil
	}
	fallback := snapshotFromAuctionEvent(event)
	if event.GetLot() != nil && isTerminalLotStatus(event.GetLot().GetStatus()) {
		return fallback, nil
	}
	if h.snapshot != nil {
		snapshot, err := h.snapshot.Snapshot(ctx, event.GetRoomId())
		if err == nil && snapshot != nil {
			current := snapshot.GetCurrentLot()
			if event.GetLot() == nil || (current != nil && current.GetId() == event.GetLotId() && current.GetVersion() >= event.GetLot().GetVersion()) {
				return cloneRoomSnapshot(snapshot), nil
			}
		}
		if fallback == nil {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("authoritative realtime snapshot is unavailable")
		}
	}
	if fallback == nil {
		return nil, errors.New("realtime event has no snapshot state")
	}
	return fallback, nil
}

func snapshotFromAuctionEvent(event *v1.AuctionEvent) *v1.RoomSnapshot {
	if event == nil || event.GetLot() == nil {
		return nil
	}
	return cloneRoomSnapshot(&v1.RoomSnapshot{
		RoomId:           event.GetRoomId(),
		CurrentLot:       event.GetLot(),
		Ranking:          event.GetRanking(),
		PlaybookStage:    event.GetLot().GetPlaybookStage(),
		ServerTimeUnixMs: event.GetOccurredAtUnixMs(),
	})
}

func (h *Hub) prepareRoomFrames(snapshot *v1.RoomSnapshot, eventType v1.AuctionEventType, messageID string, occurredAtUnixMs int64) (preparedRoomFrames, error) {
	if snapshot == nil {
		return preparedRoomFrames{}, errors.New("realtime snapshot is required")
	}
	roomID := strings.TrimSpace(snapshot.GetRoomId())
	if roomID == "" {
		return preparedRoomFrames{}, errors.New("realtime snapshot room id is required")
	}
	if messageID == "" {
		messageID = idgen.New("evt")
	}
	if occurredAtUnixMs <= 0 {
		occurredAtUnixMs = time.Now().UnixMilli()
	}
	lot := snapshot.GetCurrentLot()
	lotID := ""
	lotVersion := int64(0)
	status := v1.LotStatus_LOT_STATUS_UNSPECIFIED
	mainAccountID := ""
	if lot != nil {
		lotID = lot.GetId()
		lotVersion = lot.GetVersion()
		status = lot.GetStatus()
		mainAccountID = strings.TrimSpace(lot.GetMainAccountId())
	}

	h.wireMu.Lock()
	defer h.wireMu.Unlock()
	cache := h.wireRooms[roomID]
	if cache != nil && cache.lotID == lotID && cache.lotVersion > lotVersion {
		return preparedRoomFrames{}, ErrSnapshotVersionRegressed
	}
	previousUsers := make(map[string]struct{})
	if cache != nil {
		for userID := range cache.privateUsers {
			previousUsers[userID] = struct{}{}
		}
	}
	if cache == nil || cache.lotID != lotID || cache.lotVersion != lotVersion {
		public, err := encodeRealtimeEnvelope(messageID+":public", occurredAtUnixMs, publicSnapshotV1(snapshot))
		if err != nil {
			return preparedRoomFrames{}, err
		}
		admin, err := encodeRealtimeEnvelope(messageID+":admin", occurredAtUnixMs, adminSnapshotV1(snapshot))
		if err != nil {
			return preparedRoomFrames{}, err
		}
		cache = &roomWireCache{
			lotID:         lotID,
			lotVersion:    lotVersion,
			status:        status,
			mainAccountID: mainAccountID,
			public:        public,
			admin:         admin,
			privateUsers:  make(map[string]struct{}),
		}
		h.wireRooms[roomID] = cache
	} else {
		cache.status = status
		cache.mainAccountID = mainAccountID
	}

	currentUsers := privateUsers(snapshot)
	readyByUser := h.readyOrders[roomID]
	for userID, ready := range readyByUser {
		if ready.lotID != lotID || ready.lotVersion != lotVersion {
			delete(readyByUser, userID)
		}
	}
	if len(readyByUser) == 0 {
		delete(h.readyOrders, roomID)
	}
	personal := make(map[string]*immutablePayload, len(currentUsers)+len(previousUsers))
	for userID := range currentUsers {
		delta := personalDeltaV1(snapshot, userID, eventType, false)
		if ready, exists := readyByUser[userID]; exists && !delta.GetTombstone() {
			orderID := ready.orderID
			delta.YourOrderId = &orderID
			delta.OrderVisibility = v1.OrderVisibility_ORDER_VISIBILITY_READY
		}
		frame, err := encodeRealtimeEnvelope(messageID+":personal:"+userID, occurredAtUnixMs, delta)
		if err != nil {
			return preparedRoomFrames{}, err
		}
		personal[userID] = frame
	}
	for userID := range previousUsers {
		if _, exists := currentUsers[userID]; exists {
			continue
		}
		frame, err := encodeRealtimeEnvelope(messageID+":personal-tombstone:"+userID, occurredAtUnixMs, personalDeltaV1(snapshot, userID, eventType, true))
		if err != nil {
			return preparedRoomFrames{}, err
		}
		personal[userID] = frame
	}
	cache.privateUsers = currentUsers
	return preparedRoomFrames{
		public:        cache.public,
		admin:         cache.admin,
		mainAccountID: cache.mainAccountID,
		personal:      personal,
	}, nil
}

func (h *Hub) enqueueSnapshotState(conn *connection, snapshot *v1.RoomSnapshot, eventType v1.AuctionEventType, messageID string, occurredAtUnixMs int64) {
	prepared, err := h.prepareRoomFrames(snapshot, eventType, messageID, occurredAtUnixMs)
	if err != nil {
		return
	}
	if conn.canReceivePrivateEvents() {
		_, mainAccountID := conn.viewerIdentity()
		if mainAccountID == prepared.mainAccountID && prepared.admin != nil {
			conn.enqueueLatest(&wireBatch{frames: []*immutablePayload{prepared.admin}})
			return
		}
	}
	frames := []*immutablePayload{prepared.public}
	if userID, _ := conn.viewerIdentity(); userID != "" {
		personal := prepared.personal[userID]
		if personal == nil {
			delta := personalDeltaV1(snapshot, userID, eventType, true)
			personal, err = encodeRealtimeEnvelope(messageID+":personal:"+userID, occurredAtUnixMs, delta)
		}
		if err == nil && personal != nil {
			frames = append(frames, personal)
		}
	}
	conn.enqueueLatest(&wireBatch{frames: frames})
}

func (h *Hub) heartbeatFrame(roomID string, nowUnixMs int64) *immutablePayload {
	bucket := nowUnixMs / heartbeatInterval.Milliseconds()
	h.wireMu.Lock()
	defer h.wireMu.Unlock()
	cache := h.wireRooms[roomID]
	if cache == nil {
		return nil
	}
	if cache.heartbeat != nil && cache.heartbeatBucket == bucket {
		return cache.heartbeat
	}
	frame, err := encodeRealtimeEnvelope(idgen.New("heartbeat"), nowUnixMs, &v1.RoomHeartbeatV1{
		LotId:                   cache.lotID,
		AuthoritativeLotVersion: cache.lotVersion,
		Status:                  cache.status,
		ServerTimeUnixMs:        nowUnixMs,
	})
	if err != nil {
		return nil
	}
	cache.heartbeatBucket = bucket
	cache.heartbeat = frame
	return frame
}

func (h *Hub) forgetRoomWireState(roomID string) {
	h.wireMu.Lock()
	delete(h.wireRooms, roomID)
	delete(h.readyOrders, roomID)
	h.wireMu.Unlock()
}

func (c *connection) enqueueLatest(batch *wireBatch) {
	if batch == nil || len(batch.frames) == 0 {
		return
	}
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.latestState <- batch:
		return
	default:
	}
	select {
	case replaced := <-c.latestState:
		for _, frame := range replaced.frames {
			if frame != nil {
				observability.RecordWSEventDropped(frame.kind)
			}
		}
	default:
	}
	select {
	case c.latestState <- batch:
	case <-c.done:
	}
}

func (c *connection) enqueueCritical(frame *criticalFrame) bool {
	if frame == nil || frame.frame == nil {
		return true
	}
	select {
	case c.critical <- frame:
		return true
	case <-c.done:
		return false
	default:
		observability.RecordWSEventDropped("critical_queue_full")
		c.closeWithResync("resync required")
		return false
	}
}

func (c *connection) writeBatch(batch *wireBatch) bool {
	if batch == nil {
		return true
	}
	for _, frame := range batch.frames {
		if !c.writeFrame(frame) {
			return false
		}
	}
	return true
}

func (c *connection) writeCritical(critical *criticalFrame) bool {
	if critical == nil {
		return true
	}
	if !c.writeFrame(critical.frame) {
		return false
	}
	if !critical.closeAfter {
		return true
	}
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(critical.closeCode, critical.closeText), time.Now().Add(writeTimeout))
	return false
}

func (c *connection) writeFrame(frame *immutablePayload) bool {
	if frame == nil {
		return true
	}
	select {
	case <-c.done:
		return false
	default:
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.conn.WriteMessage(websocket.TextMessage, frame.data); err != nil {
		return false
	}
	observability.RecordWSEventSent(frame.kind)
	return true
}

func (c *connection) closeWithResync(reason string) {
	if c.conn != nil {
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, reason), time.Now().Add(writeTimeout))
	}
	c.close()
}

func (c *connection) enqueueReconnectAndClose(reason string, retryAfterMs int64, closeCode int) {
	frame, err := encodeRealtimeEnvelope(idgen.New("reconnect"), time.Now().UnixMilli(), &v1.ReconnectControlV1{
		RetryAfterMs: retryAfterMs,
		Reason:       reason,
	})
	if err != nil || !c.enqueueCritical(&criticalFrame{frame: frame, closeAfter: true, closeCode: closeCode, closeText: reason}) {
		c.closeWithResync("resync required")
	}
}
