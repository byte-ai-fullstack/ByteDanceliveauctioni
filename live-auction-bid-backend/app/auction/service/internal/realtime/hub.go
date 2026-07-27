package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
	"live-auction-bid/backend/app/auction/service/internal/pkg/clock"
	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"

	"github.com/gorilla/websocket"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const (
	writeTimeout          = 3 * time.Second
	pingInterval          = 20 * time.Second
	heartbeatInterval     = 5 * time.Second
	pongTimeout           = 60 * time.Second
	criticalQueueCapacity = 4
	roomCoalesceDelay     = 75 * time.Millisecond
)

var ErrHubDraining = errors.New("realtime gateway is draining")

type SnapshotProvider interface {
	Snapshot(ctx context.Context, roomID string) (*v1.RoomSnapshot, error)
}

type RoomAccessValidator interface {
	ValidateRoomInMainAccount(ctx context.Context, roomID, mainAccountID string) error
}

type RoomSubscriptionManager interface {
	RetainRoom(ctx context.Context, roomID string) error
	ReleaseRoom(roomID string) error
}

type Hub struct {
	mu             sync.RWMutex // guards rooms and draining
	subscriptionMu sync.Mutex
	rooms          map[string]map[*connection]struct{}
	draining       bool
	coalesceMu     sync.Mutex
	coalescedRooms map[string]*coalescedRoomEvents
	wireMu         sync.Mutex
	wireRooms      map[string]*roomWireCache
	readyOrders    map[string]map[string]readyOrderState
	snapshot       SnapshotProvider
	roomAccess     RoomAccessValidator
	roomSubs       RoomSubscriptionManager
	auth           *auth.Manager
	config         Config
	allowedOrigins map[string]struct{}
	tickets        wsTicketCodec
	upgrader       websocket.Upgrader
}

type coalescedRoomEvents struct {
	timer *time.Timer
	event *v1.AuctionEvent
}

type roomWireCache struct {
	lotID           string
	lotVersion      int64
	status          v1.LotStatus
	mainAccountID   string
	public          *immutablePayload
	admin           *immutablePayload
	privateUsers    map[string]struct{}
	heartbeatBucket int64
	heartbeat       *immutablePayload
}

type connection struct {
	hub         *Hub
	roomID      string
	scope       string
	mu          sync.RWMutex
	ctx         context.Context
	authCtx     auth.AuthContext
	conn        *websocket.Conn
	queueMu     sync.Mutex
	latestState chan *wireBatch
	critical    chan *criticalFrame
	done        chan struct{}
	closeOnce   sync.Once
}

type clientMessage struct {
	Type          string `json:"type"`
	AccessToken   string `json:"accessToken"`
	Authorization string `json:"authorization"`
}

func NewHub(snapshot SnapshotProvider, configs ...Config) *Hub {
	cfg := DefaultConfig()
	if len(configs) > 0 {
		cfg = configs[0]
	}
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		panic(err)
	}
	allowedOrigins := make(map[string]struct{}, len(normalized.AllowedOrigins))
	for _, origin := range normalized.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	h := &Hub{
		rooms:          make(map[string]map[*connection]struct{}),
		coalescedRooms: make(map[string]*coalescedRoomEvents),
		wireRooms:      make(map[string]*roomWireCache),
		readyOrders:    make(map[string]map[string]readyOrderState),
		snapshot:       snapshot,
		config:         normalized,
		allowedOrigins: allowedOrigins,
		tickets:        newWSTicketCodec(normalized),
	}
	h.upgrader = websocket.Upgrader{CheckOrigin: h.checkOrigin}
	return h
}

func (h *Hub) BindSnapshotProvider(snapshot SnapshotProvider) {
	h.snapshot = snapshot
}

func (h *Hub) BindAuthManager(authManager *auth.Manager) {
	h.auth = authManager
}

func (h *Hub) BindRoomAccessValidator(validator RoomAccessValidator) {
	h.roomAccess = validator
}

func (h *Hub) BindRoomSubscriptionManager(manager RoomSubscriptionManager) {
	h.subscriptionMu.Lock()
	h.roomSubs = manager
	h.subscriptionMu.Unlock()
}

func (h *Hub) Publish(ctx context.Context, event *v1.AuctionEvent) error {
	if event == nil {
		return nil
	}
	if h.IsDraining() {
		return nil
	}
	if isOrderReadySignal(event) {
		return h.deliverOrderReady(ctx, event)
	}
	if isPrivateRejectEvent(event.GetType()) {
		observability.RecordWSEventDropped(event.GetType().String())
		return nil
	}
	if isImmediateEvent(event.GetType()) {
		h.discardCoalescedRoom(event.GetRoomId(), event.GetLot().GetVersion())
		return h.deliver(ctx, event)
	}
	if event.GetRoomId() != "" && (event.GetLot() != nil || event.GetSnapshot() != nil) {
		h.enqueueCoalesced(event)
		return nil
	}
	return h.deliver(ctx, event)
}

func (h *Hub) enqueueCoalesced(event *v1.AuctionEvent) {
	roomID := event.GetRoomId()
	h.coalesceMu.Lock()
	pending := h.coalescedRooms[roomID]
	if pending == nil {
		pending = &coalescedRoomEvents{}
		h.coalescedRooms[roomID] = pending
		pending.timer = time.AfterFunc(roomCoalesceDelay, func() {
			h.flushCoalescedRoom(roomID)
		})
	}
	if pending.event != nil {
		observability.RecordWSEventCoalesced(event.GetType().String())
	}
	pending.event = cloneAuctionEvent(event)
	h.coalesceMu.Unlock()
}

func (h *Hub) flushCoalescedRoom(roomID string) {
	if roomID == "" {
		return
	}
	h.coalesceMu.Lock()
	pending := h.coalescedRooms[roomID]
	if pending == nil {
		h.coalesceMu.Unlock()
		return
	}
	delete(h.coalescedRooms, roomID)
	if pending.timer != nil {
		pending.timer.Stop()
	}
	event := pending.event
	h.coalesceMu.Unlock()
	if event != nil {
		_ = h.deliver(context.Background(), event)
	}
}

func (h *Hub) discardCoalescedRoom(roomID string, throughVersion int64) {
	if roomID == "" {
		return
	}
	h.coalesceMu.Lock()
	pending := h.coalescedRooms[roomID]
	if pending == nil || (throughVersion > 0 && pending.event.GetLot().GetVersion() > throughVersion) {
		h.coalesceMu.Unlock()
		return
	}
	delete(h.coalescedRooms, roomID)
	if pending.timer != nil {
		pending.timer.Stop()
	}
	h.coalesceMu.Unlock()
	if pending.event != nil {
		observability.RecordWSEventCoalesced(pending.event.GetType().String())
	}
}

func isPrivateRejectEvent(eventType v1.AuctionEventType) bool {
	return eventType == v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_REJECTED
}

func isImmediateEvent(eventType v1.AuctionEventType) bool {
	switch eventType {
	case v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_CANCELLED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_AUCTION_CLOSED,
		v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED:
		return true
	default:
		return false
	}
}

func (h *Hub) ServeRoom(w http.ResponseWriter, r *http.Request, roomID string) {
	if h.IsDraining() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, ErrHubDraining.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, authCtx, scope, ok := h.authenticateUpgrade(w, r, roomID)
	if !ok {
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	client := &connection{
		hub:         h,
		roomID:      roomID,
		scope:       scope,
		ctx:         ctx,
		authCtx:     authCtx,
		conn:        conn,
		latestState: make(chan *wireBatch, 1),
		critical:    make(chan *criticalFrame, criticalQueueCapacity),
		done:        make(chan struct{}),
	}
	if err := h.join(client); err != nil {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "room subscription unavailable"), time.Now().Add(writeTimeout))
		client.close()
		return
	}
	observability.IncWSConnection(roomID, scope)
	defer func() {
		observability.DecWSConnection(roomID, scope)
		h.leave(client)
		client.close()
	}()

	h.enqueueSnapshot(client.ctx, client)
	go client.writePump()
	client.readPump()
}

func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return h.config.AllowMissingOrigin
	}
	normalized, ok := normalizeOrigin(origin)
	if !ok {
		return false
	}
	if _, ok := h.allowedOrigins[normalized]; ok {
		return true
	}
	if !isProdEnv(h.config.Environment) && isLocalhostOrigin(normalized) {
		return true
	}
	return false
}

func (h *Hub) authenticateUpgrade(w http.ResponseWriter, r *http.Request, roomID string) (context.Context, auth.AuthContext, string, bool) {
	scope, ok := normalizeScope(r.URL.Query().Get("scope"))
	if !ok {
		http.Error(w, "invalid websocket scope", http.StatusBadRequest)
		return r.Context(), auth.AuthContext{}, "", false
	}
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if scope == ScopePublic {
		if ticket == "" {
			return r.Context(), auth.AuthContext{TokenStatus: auth.TokenStatusNone}, scope, true
		}
		ctx, authCtx, err := h.authContextFromTicket(r.Context(), ticket, roomID, scope)
		if err != nil {
			http.Error(w, "invalid websocket ticket", http.StatusUnauthorized)
			return r.Context(), auth.AuthContext{}, "", false
		}
		return ctx, authCtx, scope, true
	}
	if ticket == "" {
		http.Error(w, "websocket ticket is required", http.StatusUnauthorized)
		return r.Context(), auth.AuthContext{}, "", false
	}
	ctx, authCtx, err := h.authContextFromTicket(r.Context(), ticket, roomID, scope)
	if err != nil {
		http.Error(w, "invalid websocket ticket", http.StatusUnauthorized)
		return r.Context(), auth.AuthContext{}, "", false
	}
	if !canOpenAdminScope(authCtx.Claims) {
		http.Error(w, "websocket admin scope is forbidden", http.StatusForbidden)
		return r.Context(), auth.AuthContext{}, "", false
	}
	mainAccountID := auth.EffectiveMainAccountID(authCtx.Claims)
	if mainAccountID == "" {
		http.Error(w, "main account id is required", http.StatusForbidden)
		return r.Context(), auth.AuthContext{}, "", false
	}
	if h.roomAccess == nil {
		http.Error(w, "room access validator is not configured", http.StatusServiceUnavailable)
		return r.Context(), auth.AuthContext{}, "", false
	}
	if err := h.roomAccess.ValidateRoomInMainAccount(ctx, roomID, mainAccountID); err != nil {
		http.Error(w, "room access denied", http.StatusForbidden)
		return r.Context(), auth.AuthContext{}, "", false
	}
	return ctx, authCtx, scope, true
}

func (h *Hub) authContextFromAuthorization(ctx context.Context, authorization string) (context.Context, auth.AuthContext) {
	if h.auth == nil {
		return ctx, auth.AuthContext{TokenStatus: auth.TokenStatusNone}
	}
	authCtx := h.auth.AuthContextFromBearer(authorization)
	if authCtx.TokenStatus == auth.TokenStatusValid {
		ctx = auth.WithClaims(ctx, authCtx.Claims)
	}
	return ctx, authCtx
}

func (h *Hub) authContextFromTicket(ctx context.Context, ticket, roomID, scope string) (context.Context, auth.AuthContext, error) {
	claims, err := h.tickets.parse(ticket, roomID, scope)
	if err != nil {
		return ctx, auth.AuthContext{}, err
	}
	authCtx := authContextFromTicketClaims(ticket, claims)
	return auth.WithClaims(ctx, authCtx.Claims), authCtx, nil
}

func websocketAuthorization(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("Authorization")); value != "" {
		return value
	}
	if value := strings.TrimSpace(r.Header.Get("authorization")); value != "" {
		return value
	}
	return ""
}

func (h *Hub) join(conn *connection) error {
	h.subscriptionMu.Lock()
	defer h.subscriptionMu.Unlock()

	h.mu.Lock()
	if h.draining {
		h.mu.Unlock()
		return ErrHubDraining
	}
	first := len(h.rooms[conn.roomID]) == 0
	if h.rooms[conn.roomID] == nil {
		h.rooms[conn.roomID] = make(map[*connection]struct{})
	}
	h.rooms[conn.roomID][conn] = struct{}{}
	h.mu.Unlock()

	if first && h.roomSubs != nil {
		if err := h.roomSubs.RetainRoom(conn.context(), conn.roomID); err != nil {
			h.mu.Lock()
			delete(h.rooms[conn.roomID], conn)
			if len(h.rooms[conn.roomID]) == 0 {
				delete(h.rooms, conn.roomID)
			}
			h.mu.Unlock()
			return err
		}
	}
	return nil
}

func (h *Hub) Ping(context.Context) error {
	if h.IsDraining() {
		return ErrHubDraining
	}
	return nil
}

func (h *Hub) IsDraining() bool {
	h.mu.RLock()
	draining := h.draining
	h.mu.RUnlock()
	return draining
}

// BeginDrain closes admission before the load balancer removes this instance.
// Repeated calls are safe.
func (h *Hub) BeginDrain() {
	h.mu.Lock()
	h.draining = true
	h.mu.Unlock()
	h.stopCoalescing()
}

// Drain asks clients to reconnect in bounded batches and waits for every
// connection to leave. Context expiry force-closes the remaining sockets.
func (h *Hub) Drain(ctx context.Context, cfg DrainConfig) error {
	normalized, err := NormalizeDrainConfig(cfg)
	if err != nil {
		return err
	}
	h.BeginDrain()
	if err := waitForDrainStep(ctx, normalized.AdmissionDelay); err != nil {
		h.forceCloseConnections()
		return err
	}

	connections := h.allConnections()
	for start := 0; start < len(connections); start += normalized.BatchSize {
		end := start + normalized.BatchSize
		if end > len(connections) {
			end = len(connections)
		}
		for index, conn := range connections[start:end] {
			retryAfter := drainRetryAfter(start+index, normalized)
			conn.enqueueReconnectAndClose("server draining", retryAfter.Milliseconds(), websocket.CloseServiceRestart)
		}
		if end < len(connections) {
			if err := waitForDrainStep(ctx, normalized.BatchInterval); err != nil {
				h.forceCloseConnections()
				return err
			}
		}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if h.ConnectionCount() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			h.forceCloseConnections()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *Hub) ConnectionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	for _, room := range h.rooms {
		total += len(room)
	}
	return total
}

func (h *Hub) allConnections() []*connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	connections := make([]*connection, 0)
	for _, room := range h.rooms {
		for conn := range room {
			connections = append(connections, conn)
		}
	}
	return connections
}

func (h *Hub) forceCloseConnections() {
	for _, conn := range h.allConnections() {
		conn.close()
	}
}

func (h *Hub) stopCoalescing() {
	h.coalesceMu.Lock()
	rooms := h.coalescedRooms
	h.coalescedRooms = make(map[string]*coalescedRoomEvents)
	h.coalesceMu.Unlock()
	for _, pending := range rooms {
		if pending != nil && pending.timer != nil {
			pending.timer.Stop()
		}
	}
}

func waitForDrainStep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func drainRetryAfter(index int, cfg DrainConfig) time.Duration {
	minimumMs := cfg.RetryAfterMin.Milliseconds()
	maximumMs := cfg.RetryAfterMax.Milliseconds()
	spanMs := maximumMs - minimumMs
	if spanMs <= 0 {
		return time.Duration(minimumMs) * time.Millisecond
	}
	offsetMs := (int64(index) * 7919) % (spanMs + 1)
	return time.Duration(minimumMs+offsetMs) * time.Millisecond
}

func (h *Hub) leave(conn *connection) {
	h.subscriptionMu.Lock()
	defer h.subscriptionMu.Unlock()

	h.mu.Lock()
	room, exists := h.rooms[conn.roomID]
	if !exists {
		h.mu.Unlock()
		return
	}
	if _, exists := room[conn]; !exists {
		h.mu.Unlock()
		return
	}
	delete(room, conn)
	last := len(room) == 0
	if last {
		delete(h.rooms, conn.roomID)
	}
	h.mu.Unlock()

	if last && h.roomSubs != nil {
		_ = h.roomSubs.ReleaseRoom(conn.roomID)
	}
	if last {
		h.forgetRoomWireState(conn.roomID)
	}
}

func (h *Hub) roomConnections(roomID string) []*connection {
	h.mu.RLock()
	defer h.mu.RUnlock()

	connections := make([]*connection, 0, len(h.rooms[roomID]))
	for conn := range h.rooms[roomID] {
		connections = append(connections, conn)
	}
	return connections
}

func (h *Hub) RoomPresence(roomID string) *v1.RoomPresence {
	connections := h.roomConnections(roomID)
	presence := &v1.RoomPresence{
		RoomId:           roomID,
		TotalConnections: int32(len(connections)),
		ServerTimeUnixMs: clock.NowMs(),
	}
	for _, conn := range connections {
		if conn.canReceivePrivateEvents() {
			presence.OperatorConnections++
			continue
		}
		presence.ViewerConnections++
	}
	return presence
}

func (h *Hub) enqueueSnapshot(ctx context.Context, conn *connection) {
	if h.snapshot == nil {
		return
	}
	snapshot, err := h.snapshot.Snapshot(ctx, conn.roomID)
	if err != nil {
		return
	}
	h.enqueueSnapshotState(conn, snapshot, v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT, idgen.New("evt"), clock.NowMs())
}

func (c *connection) readPump() {
	_ = c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	})
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.handleClientMessage(payload)
	}
}

func (c *connection) handleClientMessage(payload []byte) {
	var msg clientMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}
	if !strings.EqualFold(msg.Type, "AUTH") {
		return
	}
	if c.scope != ScopePublic {
		return
	}
	if c.hub.auth == nil {
		return
	}
	token := strings.TrimSpace(msg.AccessToken)
	authorization := strings.TrimSpace(msg.Authorization)
	if authorization == "" && token != "" {
		authorization = "Bearer " + token
	}
	if authorization == "" {
		return
	}
	ctx, authCtx := c.hub.authContextFromAuthorization(c.context(), authorization)
	if authCtx.TokenStatus != auth.TokenStatusValid {
		c.enqueueReconnectAndClose("invalid auth", 0, websocket.ClosePolicyViolation)
		return
	}
	c.mu.Lock()
	c.authCtx = authCtx
	c.ctx = ctx
	c.mu.Unlock()
	c.hub.enqueueSnapshot(c.context(), c)
}

func (c *connection) context() context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ctx
}

func (c *connection) canReceivePrivateEvents() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.scope != ScopeAdmin {
		return false
	}
	if c.authCtx.TokenStatus != auth.TokenStatusValid || c.authCtx.Claims == nil {
		return false
	}
	return auth.HasAnyPermission(c.authCtx.Claims, userbiz.PermissionRealtimeView, userbiz.PermissionAuctionControl, userbiz.PermissionLotViewAdmin)
}

func (c *connection) writePump() {
	pingTicker := time.NewTicker(pingInterval)
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer pingTicker.Stop()
	defer heartbeatTicker.Stop()
	defer func() {
		c.hub.leave(c)
		c.close()
	}()

	for {
		select {
		case critical := <-c.critical:
			if !c.writeCritical(critical) {
				return
			}
			continue
		default:
		}
		select {
		case critical := <-c.critical:
			if !c.writeCritical(critical) {
				return
			}
		case batch := <-c.latestState:
			if !c.writeBatch(batch) {
				return
			}
		case <-heartbeatTicker.C:
			if frame := c.hub.heartbeatFrame(c.roomID, clock.NowMs()); frame != nil && !c.writeFrame(frame) {
				return
			}
		case <-pingTicker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *connection) viewerIdentity() (userID, mainAccountID string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.authCtx.TokenStatus != auth.TokenStatusValid || c.authCtx.Claims == nil {
		return "", ""
	}
	return strings.TrimSpace(c.authCtx.Claims.UserID), strings.TrimSpace(auth.EffectiveMainAccountID(c.authCtx.Claims))
}

func canOpenAdminScope(claims *auth.Claims) bool {
	if claims == nil || !auth.HasPermission(claims, userbiz.PermissionRealtimeView) {
		return false
	}
	return auth.HasRoleCode(claims, userbiz.RoleMerchantOwner) || auth.HasRoleCode(claims, userbiz.RoleAnchor) || auth.HasRoleCode(claims, userbiz.RoleOperator)
}

func (c *connection) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}
