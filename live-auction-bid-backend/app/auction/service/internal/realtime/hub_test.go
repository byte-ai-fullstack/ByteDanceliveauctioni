package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/pkg/auth"
)

type testSnapshotProvider struct {
	snapshot *v1.RoomSnapshot
}

func (p testSnapshotProvider) Snapshot(context.Context, string) (*v1.RoomSnapshot, error) {
	if p.snapshot == nil {
		return &v1.RoomSnapshot{RoomId: "room1"}, nil
	}
	return proto.Clone(p.snapshot).(*v1.RoomSnapshot), nil
}

type testRoomAccess struct {
	mainByRoom map[string]string
}

type testRoomSubscriptions struct {
	retained []string
	released []string
	err      error
}

func (s *testRoomSubscriptions) RetainRoom(_ context.Context, roomID string) error {
	s.retained = append(s.retained, roomID)
	return s.err
}

func (s *testRoomSubscriptions) ReleaseRoom(roomID string) error {
	s.released = append(s.released, roomID)
	return nil
}

func (a testRoomAccess) ValidateRoomInMainAccount(_ context.Context, roomID, mainAccountID string) error {
	if a.mainByRoom[roomID] != mainAccountID {
		return errors.New("room access denied")
	}
	return nil
}

func TestHubRoomSubscriptionFollowsPresenceTransitions(t *testing.T) {
	hub := NewHub(nil)
	subs := &testRoomSubscriptions{}
	hub.BindRoomSubscriptionManager(subs)
	first := &connection{hub: hub, roomID: "room1", ctx: context.Background()}
	second := &connection{hub: hub, roomID: "room1", ctx: context.Background()}

	if err := hub.join(first); err != nil {
		t.Fatalf("join first: %v", err)
	}
	if err := hub.join(second); err != nil {
		t.Fatalf("join second: %v", err)
	}
	if len(subs.retained) != 1 || subs.retained[0] != "room1" {
		t.Fatalf("expected one retain on 0->1 transition, got %+v", subs.retained)
	}

	hub.leave(first)
	if len(subs.released) != 0 {
		t.Fatalf("must keep subscription while room has viewers, got %+v", subs.released)
	}
	hub.leave(second)
	hub.leave(second)
	if len(subs.released) != 1 || subs.released[0] != "room1" {
		t.Fatalf("expected one release on 1->0 transition, got %+v", subs.released)
	}
}

func TestHubRollsBackJoinWhenRoomSubscriptionFails(t *testing.T) {
	hub := NewHub(nil)
	subs := &testRoomSubscriptions{err: errors.New("nats unavailable")}
	hub.BindRoomSubscriptionManager(subs)
	conn := &connection{hub: hub, roomID: "room1", ctx: context.Background()}

	if err := hub.join(conn); err == nil {
		t.Fatal("join should fail when exact room subscription cannot be established")
	}
	if got := hub.RoomPresence("room1").GetTotalConnections(); got != 0 {
		t.Fatalf("failed join leaked room presence: %d", got)
	}
}

func TestHubDrainRejectsAdmissionAndStaggersReconnects(t *testing.T) {
	hub, _, server := newRealtimeTestServer(t)
	defer server.Close()

	first, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial first websocket: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial second websocket: %v", err)
	}
	defer func() { _ = second.Close() }()
	_ = readRealtimeEnvelope(t, first)
	_ = readRealtimeEnvelope(t, second)
	if got := hub.ConnectionCount(); got != 2 {
		t.Fatalf("expected two active connections, got %d", got)
	}

	hub.BeginDrain()
	if err := hub.Ping(context.Background()); !errors.Is(err, ErrHubDraining) {
		t.Fatalf("draining hub must fail readiness: %v", err)
	}
	_, response, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err == nil {
		t.Fatal("draining hub accepted a new websocket")
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while draining, got response=%v err=%v", response, err)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := hub.Drain(drainCtx, DrainConfig{
		BatchSize:     1,
		BatchInterval: time.Millisecond,
		RetryAfterMin: 1234 * time.Millisecond,
		RetryAfterMax: 1234 * time.Millisecond,
	}); err != nil {
		t.Fatalf("drain hub: %v", err)
	}
	for index, conn := range []*websocket.Conn{first, second} {
		envelope := readRealtimeEnvelope(t, conn)
		if reconnect := envelope.GetReconnect(); reconnect == nil || reconnect.GetRetryAfterMs() != 1234 || reconnect.GetReason() != "server draining" {
			t.Fatalf("connection %d did not receive reconnect control: %+v", index, envelope)
		}
	}
	if got := hub.ConnectionCount(); got != 0 {
		t.Fatalf("drain leaked %d connections", got)
	}
}

func TestHubForceCloseConnectionsClosesEveryLocalClient(t *testing.T) {
	hub := NewHub(nil)
	first := &connection{hub: hub, roomID: "room-1", done: make(chan struct{})}
	second := &connection{hub: hub, roomID: "room-2", done: make(chan struct{})}
	hub.rooms[first.roomID] = map[*connection]struct{}{first: {}}
	hub.rooms[second.roomID] = map[*connection]struct{}{second: {}}

	hub.forceCloseConnections()
	for index, conn := range []*connection{first, second} {
		select {
		case <-conn.done:
		default:
			t.Fatalf("connection %d was not closed", index)
		}
	}
}

func TestHubRoomPresenceSeparatesViewersAndOperators(t *testing.T) {
	hub := NewHub(nil)
	viewer := &connection{hub: hub, roomID: "room-1", scope: ScopePublic}
	operator := &connection{
		hub: hub, roomID: "room-1", scope: ScopeAdmin,
		authCtx: auth.AuthContext{
			TokenStatus: auth.TokenStatusValid,
			Claims:      &auth.Claims{PermissionCodes: []string{userbiz.PermissionRealtimeView}},
		},
	}
	hub.rooms["room-1"] = map[*connection]struct{}{viewer: {}, operator: {}}

	presence := hub.RoomPresence("room-1")
	if presence.GetTotalConnections() != 2 || presence.GetViewerConnections() != 1 || presence.GetOperatorConnections() != 1 || presence.GetServerTimeUnixMs() <= 0 {
		t.Fatalf("unexpected room presence: %+v", presence)
	}
}

func TestDrainRetryAfterSpreadsClientsAcrossConfiguredWindow(t *testing.T) {
	cfg := DrainConfig{RetryAfterMin: time.Second, RetryAfterMax: 30 * time.Second}
	seen := make(map[time.Duration]struct{})
	for index := 0; index < 100; index++ {
		delay := drainRetryAfter(index, cfg)
		if delay < cfg.RetryAfterMin || delay > cfg.RetryAfterMax {
			t.Fatalf("retry-after outside configured range: %s", delay)
		}
		seen[delay] = struct{}{}
	}
	if len(seen) < 90 {
		t.Fatalf("retry-after values are not sufficiently spread: %d unique", len(seen))
	}
}

func TestHubRejectsNonWhitelistedOrigin(t *testing.T) {
	hub, _, server := newRealtimeTestServer(t)
	defer server.Close()

	_, response, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), http.Header{"Origin": []string{"https://evil.example"}})
	if err == nil {
		t.Fatal("expected websocket dial to fail for non-whitelisted origin")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got response=%v err=%v hub=%v", response, err, hub)
	}
}

func TestHubOriginPolicyDevLocalhostAndProdMissingOrigin(t *testing.T) {
	devConfig := DefaultConfig()
	devConfig.TicketSecret = "secret"
	devHub := NewHub(nil, devConfig)
	if !devHub.checkOrigin(httptest.NewRequest(http.MethodGet, "/ws/rooms/room1", nil)) {
		t.Fatal("dev should allow missing Origin for non-browser clients")
	}
	devReq := httptest.NewRequest(http.MethodGet, "/ws/rooms/room1", nil)
	devReq.Header.Set("Origin", "http://localhost:5174")
	if !devHub.checkOrigin(devReq) {
		t.Fatal("dev should allow localhost Origin")
	}

	prodHub := NewHub(nil, Config{Environment: "prod", AllowedOrigins: []string{"https://admin.example.test"}, TicketSecret: "secret"})
	if prodHub.checkOrigin(httptest.NewRequest(http.MethodGet, "/ws/rooms/room1", nil)) {
		t.Fatal("prod should reject missing Origin by default")
	}
}

func TestHubRejectsAdminWithoutTicketBeforeUpgrade(t *testing.T) {
	_, _, server := newRealtimeTestServer(t)
	defer server.Close()

	_, response, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=admin"), allowedOriginHeader())
	if err == nil {
		t.Fatal("expected admin websocket dial without ticket to fail")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got response=%v err=%v", response, err)
	}
}

func TestServeTicketRejectsInvalidRequestsBeforeIssuingCredentials(t *testing.T) {
	hub := NewHub(nil)
	testsWithoutAuth := []struct {
		method string
		body   string
		status int
	}{
		{method: http.MethodGet, body: `{}`, status: http.StatusMethodNotAllowed},
		{method: http.MethodPost, body: `{}`, status: http.StatusServiceUnavailable},
	}
	for _, test := range testsWithoutAuth {
		recorder := httptest.NewRecorder()
		hub.ServeTicket(recorder, httptest.NewRequest(test.method, "/api/realtime/ws-ticket", strings.NewReader(test.body)))
		if recorder.Code != test.status {
			t.Fatalf("method=%s status=%d want=%d body=%s", test.method, recorder.Code, test.status, recorder.Body.String())
		}
	}

	manager, err := auth.NewManager(auth.Config{Secret: "ticket-test-secret", Issuer: "ticket-test"})
	if err != nil {
		t.Fatal(err)
	}
	hub.BindAuthManager(manager)
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "malformed JSON", body: `{`, status: http.StatusBadRequest},
		{name: "missing room", body: `{}`, status: http.StatusBadRequest},
		{name: "invalid scope", body: `{"roomId":"room-1","scope":"private"}`, status: http.StatusBadRequest},
		{name: "missing bearer token", body: `{"roomId":"room-1","scope":"public"}`, status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/realtime/ws-ticket", strings.NewReader(test.body))
			hub.ServeTicket(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("content type=%q", got)
			}
		})
	}

	ownerToken := issueAccessToken(t, manager, &v1.User{
		Id:              "main-1",
		Username:        "owner",
		RoleCodes:       []string{userbiz.RoleMerchantOwner},
		PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleMerchantOwner),
		MainAccountId:   "main-1",
		Status:          v1.UserStatus_USER_STATUS_ACTIVE,
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/realtime/ws-ticket", strings.NewReader(`{"roomId":"room-1","scope":"admin"}`))
	request.Header.Set("Authorization", "Bearer "+ownerToken)
	hub.ServeTicket(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing room validator status=%d want=%d body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestHubRejectsAdminInvalidTicketBeforeUpgrade(t *testing.T) {
	_, _, server := newRealtimeTestServer(t)
	defer server.Close()

	_, response, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=admin&ticket=invalid"), allowedOriginHeader())
	if err == nil {
		t.Fatal("expected admin websocket dial with invalid ticket to fail")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got response=%v err=%v", response, err)
	}
}

func TestWSTicketExpires(t *testing.T) {
	codec := newWSTicketCodec(Config{TicketSecret: "secret", TicketTTL: time.Minute})
	now := time.Unix(1000, 0)
	codec.now = func() time.Time { return now }
	ticket, _, err := codec.issue(wsTicketClaims{RoomID: "room1", Scope: ScopeAdmin, UserID: "main1"})
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	codec.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := codec.parse(ticket, "room1", ScopeAdmin); !errors.Is(err, errTicketExpired) {
		t.Fatalf("expected expired ticket, got %v", err)
	}
}

func TestHubPublicAnonymousReceivesRedactedSnapshot(t *testing.T) {
	_, _, server := newRealtimeTestServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial public websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	envelope := readRealtimeEnvelope(t, conn)
	public := envelope.GetPublicSnapshot()
	if public == nil || public.GetLotId() != "lot1" || public.GetLotVersion() != 7 {
		t.Fatalf("expected public v1 snapshot, got %+v", envelope)
	}
	if got := public.GetTopRanking()[0]; got.GetMaskedNickname() != "张***" || got.GetAmountFen() != 12000 {
		t.Fatalf("public ranking should contain only masked identity: %+v", got)
	}
}

func TestHubAdminTicketReceivesFullSnapshotAndCrossMainEventRedacted(t *testing.T) {
	hub, manager, server := newRealtimeTestServer(t)
	defer server.Close()

	token := issueAccessToken(t, manager, &v1.User{
		Id:              "main1",
		Username:        "owner",
		Nickname:        "主账号",
		RoleCodes:       []string{userbiz.RoleMerchantOwner},
		PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleMerchantOwner),
		MainAccountId:   "main1",
		Status:          v1.UserStatus_USER_STATUS_ACTIVE,
	})
	ticket := requestWSTicket(t, server.URL, token, "room1", ScopeAdmin, http.StatusOK)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=admin&ticket="+ticket), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial admin websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	snapshot := readRealtimeEnvelope(t, conn).GetAdminSnapshot()
	if snapshot.GetMainAccountId() != "main1" || snapshot.GetTopRanking()[0].GetUserId() != "buyer1" {
		t.Fatalf("same-main admin should receive full snapshot: %+v", snapshot)
	}

	if err := hub.Publish(context.Background(), &v1.AuctionEvent{
		Id:       "evt_snapshot_without_main",
		Type:     v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT,
		RoomId:   "room1",
		Snapshot: testSnapshot(),
	}); err != nil {
		t.Fatalf("publish snapshot event without main account id: %v", err)
	}
	publishedSnapshot := readRealtimeEnvelope(t, conn).GetAdminSnapshot()
	if publishedSnapshot.GetMainAccountId() != "main1" || publishedSnapshot.GetTopRanking()[0].GetUserId() != "buyer1" {
		t.Fatalf("admin should receive full snapshot even when event mainAccountId is omitted: %+v", publishedSnapshot)
	}

	if err := hub.Publish(context.Background(), &v1.AuctionEvent{
		Id:            "evt_order",
		Type:          v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED,
		RoomId:        "room1",
		MainAccountId: "main1",
		Reason:        "order_id=order_private",
		Lot: &v1.Lot{
			Id:             "lot1",
			RoomId:         "room1",
			MainAccountId:  "main1",
			Status:         v1.LotStatus_LOT_STATUS_SETTLED,
			Version:        8,
			WinnerUserId:   "buyer1",
			WinnerNickname: "张三",
			CurrentPrice:   &v1.Money{Amount: 12000, Currency: "CNY"},
			FinalPrice:     &v1.Money{Amount: 12000, Currency: "CNY"},
		},
		Ranking: testSnapshot().GetRanking(),
	}); err != nil {
		t.Fatalf("publish same-main order event: %v", err)
	}
	orderSnapshot := readRealtimeEnvelope(t, conn).GetAdminSnapshot()
	if orderSnapshot.GetMainAccountId() != "main1" || orderSnapshot.GetLotVersion() != 8 || orderSnapshot.GetTopRanking()[0].GetUserId() != "buyer1" {
		t.Fatalf("same-main admin should receive versioned admin state: %+v", orderSnapshot)
	}

	if err := hub.Publish(context.Background(), &v1.AuctionEvent{
		Id:            "evt_cross",
		Type:          v1.AuctionEventType_AUCTION_EVENT_TYPE_BID_ACCEPTED,
		RoomId:        "room1",
		MainAccountId: "main2",
		Lot: &v1.Lot{
			Id:            "lot1",
			RoomId:        "room1",
			MainAccountId: "main2",
			Status:        v1.LotStatus_LOT_STATUS_LIVE,
			Version:       9,
			CurrentPrice:  &v1.Money{Amount: 20000, Currency: "CNY"},
		},
		Bid:     &v1.Bid{UserId: "buyer2", Nickname: "李四", Amount: &v1.Money{Amount: 20000, Currency: "CNY"}},
		Ranking: []*v1.RankingItem{{Rank: 1, UserId: "buyer2", Nickname: "李四", Amount: &v1.Money{Amount: 20000, Currency: "CNY"}}},
	}); err != nil {
		t.Fatalf("publish cross-main event: %v", err)
	}
	cross := readRealtimeEnvelope(t, conn)
	if cross.GetAdminSnapshot() != nil || cross.GetPublicSnapshot().GetTopRanking()[0].GetMaskedNickname() != "李***" {
		t.Fatalf("cross-main event must fall back to a fully public payload: %+v", cross)
	}
}

func TestHubRejectsCrossMainAdminTicket(t *testing.T) {
	_, manager, server := newRealtimeTestServer(t)
	defer server.Close()

	token := issueAccessToken(t, manager, &v1.User{
		Id:              "main2",
		Username:        "other",
		Nickname:        "其他主账号",
		RoleCodes:       []string{userbiz.RoleMerchantOwner},
		PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleMerchantOwner),
		MainAccountId:   "main2",
		Status:          v1.UserStatus_USER_STATUS_ACTIVE,
	})
	_ = requestWSTicket(t, server.URL, token, "room1", ScopeAdmin, http.StatusForbidden)
}

func TestHubPublicAuthCannotEscalateToAdminPrivateData(t *testing.T) {
	_, manager, server := newRealtimeTestServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial public websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = readRealtimeEnvelope(t, conn)

	token := issueAccessToken(t, manager, &v1.User{
		Id:              "main1",
		Username:        "owner",
		Nickname:        "主账号",
		RoleCodes:       []string{userbiz.RoleMerchantOwner},
		PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleMerchantOwner),
		MainAccountId:   "main1",
		Status:          v1.UserStatus_USER_STATUS_ACTIVE,
	})
	if err := conn.WriteJSON(map[string]string{"type": "AUTH", "accessToken": token}); err != nil {
		t.Fatalf("send public AUTH: %v", err)
	}
	event := readRealtimeEnvelope(t, conn)
	if event.GetAdminSnapshot() != nil || event.GetPublicSnapshot().GetTopRanking()[0].GetMaskedNickname() != "张***" {
		t.Fatalf("public AUTH with admin token must remain on the public wire model: %+v", event)
	}
	delta := readRealtimeEnvelope(t, conn).GetPersonalDelta()
	if delta.GetUserId() != "main1" || !delta.GetTombstone() {
		t.Fatalf("public scope may receive only its own empty overlay: %+v", delta)
	}
}

func TestHubPublicBuyerAuthSeesOnlyOwnIdentity(t *testing.T) {
	hub, manager, server := newRealtimeTestServer(t)
	defer server.Close()
	hub.BindSnapshotProvider(testSnapshotProvider{snapshot: testBuyerScopedSnapshot()})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial public websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = readRealtimeEnvelope(t, conn)

	token := issueAccessToken(t, manager, &v1.User{
		Id:              "buyer1",
		Username:        "buyer",
		Nickname:        "张三",
		RoleCodes:       []string{userbiz.RoleBuyer},
		PermissionCodes: userbiz.PermissionsForRole(userbiz.RoleBuyer),
		Status:          v1.UserStatus_USER_STATUS_ACTIVE,
	})
	if err := conn.WriteJSON(map[string]string{"type": "AUTH", "accessToken": token}); err != nil {
		t.Fatalf("send buyer AUTH: %v", err)
	}
	public := readRealtimeEnvelope(t, conn).GetPublicSnapshot()
	if public.GetTopRanking()[0].GetMaskedNickname() != "张***" || public.GetTopRanking()[1].GetMaskedNickname() != "李***" {
		t.Fatalf("public snapshot must keep every identity masked: %+v", public)
	}
	delta := readRealtimeEnvelope(t, conn).GetPersonalDelta()
	if delta.GetUserId() != "buyer1" || delta.GetYourRank() != 1 || delta.GetYourAmountFen() != 12000 || !delta.GetYouAreLeading() || delta.GetTombstone() {
		t.Fatalf("buyer should receive only its own same-version overlay: %+v", delta)
	}
}

func TestRealtimeConfigProdRequiresAllowedOrigins(t *testing.T) {
	if _, err := NormalizeConfig(Config{Environment: "prod", TicketSecret: "secret"}); err == nil {
		t.Fatal("prod websocket config should require allowed origins")
	}
}

func TestRemoteBusDispatchPublishesToWebSocketClient(t *testing.T) {
	hub, _, server := newRealtimeTestServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/rooms/room1?scope=public"), allowedOriginHeader())
	if err != nil {
		t.Fatalf("dial public websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = readRealtimeEnvelope(t, conn)

	payload, err := encodeNATSEventEnvelope("node-a", &v1.AuctionEvent{
		Id:            "evt_remote_settled",
		Type:          v1.AuctionEventType_AUCTION_EVENT_TYPE_LOT_SETTLED,
		RoomId:        "room1",
		MainAccountId: "main1",
		Lot: &v1.Lot{
			Id:             "lot1",
			RoomId:         "room1",
			MainAccountId:  "main1",
			Status:         v1.LotStatus_LOT_STATUS_SETTLED,
			Version:        8,
			WinnerUserId:   "buyer1",
			WinnerNickname: "张三",
			CurrentPrice:   &v1.Money{Amount: 12000, Currency: "CNY"},
			FinalPrice:     &v1.Money{Amount: 12000, Currency: "CNY"},
		},
		Ranking: testSnapshot().GetRanking(),
	})
	if err != nil {
		t.Fatalf("encode remote event: %v", err)
	}

	bus := &NATSBus{origin: "node-b"}
	delivered, err := bus.dispatchPayload(context.Background(), hub, payload)
	if err != nil {
		t.Fatalf("dispatch remote payload: %v", err)
	}
	if !delivered {
		t.Fatal("remote payload should be delivered to this hub")
	}
	event := readRealtimeEnvelope(t, conn)
	if event.GetMessageId() != "evt_remote_settled:public" || event.GetPublicSnapshot().GetStatus() != v1.LotStatus_LOT_STATUS_SETTLED || event.GetPublicSnapshot().GetSettlement() == nil {
		t.Fatalf("websocket client did not receive the remote terminal snapshot: %+v", event)
	}
}

func TestHubOrderReadySignalTargetsWinnerAndSurvivesSnapshotRefresh(t *testing.T) {
	snapshot := testBuyerScopedSnapshot()
	lot := snapshot.GetCurrentLot()
	lot.Status = v1.LotStatus_LOT_STATUS_SETTLED
	lot.Version = 8
	lot.WinnerUserId = "buyer1"
	lot.WinnerNickname = "张三"
	lot.FinalPrice = &v1.Money{Amount: 12_000, Currency: "CNY"}
	lot.SettledAtUnixMs = 1_700_000_000_000
	hub := NewHub(testSnapshotProvider{snapshot: snapshot})
	buyer := &connection{
		hub: hub, roomID: "room1", scope: ScopePublic, ctx: context.Background(),
		authCtx:     auth.AuthContext{TokenStatus: auth.TokenStatusValid, Claims: &auth.Claims{UserID: "buyer1"}},
		latestState: make(chan *wireBatch, 1), critical: make(chan *criticalFrame, 1), done: make(chan struct{}),
	}
	otherBuyer := &connection{
		hub: hub, roomID: "room1", scope: ScopePublic, ctx: context.Background(),
		authCtx:     auth.AuthContext{TokenStatus: auth.TokenStatusValid, Claims: &auth.Claims{UserID: "buyer2"}},
		latestState: make(chan *wireBatch, 1), critical: make(chan *criticalFrame, 1), done: make(chan struct{}),
	}
	hub.rooms["room1"] = map[*connection]struct{}{buyer: {}, otherBuyer: {}}
	orderID, err := eventcontract.RuntimeOrderID(lot.GetId())
	if err != nil {
		t.Fatal(err)
	}
	ready := &v1.AuctionEvent{
		Id: "order-created-message", Type: v1.AuctionEventType_AUCTION_EVENT_TYPE_ORDER_CREATED,
		RoomId: "room1", LotId: lot.GetId(), OccurredAtUnixMs: 1_700_000_001_000,
		BuyerUserId: "buyer1", OrderId: orderID, OrderVisibility: v1.OrderVisibility_ORDER_VISIBILITY_READY,
		LotVersion: lot.GetVersion(),
	}
	if err := hub.Publish(context.Background(), ready); err != nil {
		t.Fatalf("publish READY: %v", err)
	}
	batch := <-buyer.latestState
	if len(batch.frames) != 1 {
		t.Fatalf("winner frames=%d", len(batch.frames))
	}
	envelope := new(v1.RealtimeEnvelopeV1)
	if err := protojson.Unmarshal(batch.frames[0].data, envelope); err != nil {
		t.Fatal(err)
	}
	delta := envelope.GetPersonalDelta()
	if delta.GetUserId() != "buyer1" || delta.GetYourRank() != 1 || delta.GetYourAmountFen() != 12_000 ||
		!delta.GetYouAreLeading() || delta.GetYourOrderId() != orderID || delta.GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_READY {
		t.Fatalf("winner READY delta=%+v", delta)
	}
	select {
	case leaked := <-otherBuyer.latestState:
		t.Fatalf("READY leaked to another buyer: %+v", leaked)
	default:
	}

	prepared, err := hub.prepareRoomFrames(snapshot, v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT, "refresh", 1_700_000_002_000)
	if err != nil {
		t.Fatalf("prepare refresh: %v", err)
	}
	refreshed := new(v1.RealtimeEnvelopeV1)
	if err := protojson.Unmarshal(prepared.personal["buyer1"].data, refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.GetPersonalDelta().GetOrderVisibility() != v1.OrderVisibility_ORDER_VISIBILITY_READY || refreshed.GetPersonalDelta().GetYourOrderId() != orderID {
		t.Fatalf("snapshot refresh regressed READY: %+v", refreshed.GetPersonalDelta())
	}
}

func newRealtimeTestServer(t *testing.T) (*Hub, *auth.Manager, *httptest.Server) {
	t.Helper()
	manager, err := auth.NewManager(auth.Config{Secret: "unit-test-secret", Issuer: "test", AccessTTL: time.Minute})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	hub := NewHub(testSnapshotProvider{snapshot: testSnapshot()}, Config{
		Environment:        "prod",
		AllowedOrigins:     []string{"https://admin.example.test"},
		AllowMissingOrigin: false,
		TicketTTL:          time.Minute,
		TicketSecret:       "unit-test-secret",
	})
	hub.BindAuthManager(manager)
	hub.BindRoomAccessValidator(testRoomAccess{mainByRoom: map[string]string{"room1": "main1"}})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/realtime/ws-ticket", hub.ServeTicket)
	mux.HandleFunc("/ws/rooms/", func(w http.ResponseWriter, r *http.Request) {
		roomID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/ws/rooms/"), "/")
		hub.ServeRoom(w, r, roomID)
	})
	return hub, manager, httptest.NewServer(mux)
}

func testSnapshot() *v1.RoomSnapshot {
	return &v1.RoomSnapshot{
		RoomId: "room1",
		CurrentLot: &v1.Lot{
			Id:              "lot1",
			RoomId:          "room1",
			MainAccountId:   "main1",
			Status:          v1.LotStatus_LOT_STATUS_LIVE,
			Version:         7,
			LeadingUserId:   "buyer1",
			LeadingNickname: "张三",
			CurrentPrice:    &v1.Money{Amount: 12000, Currency: "CNY"},
			Stats:           &v1.LotStats{BidCount: 3},
		},
		Ranking: []*v1.RankingItem{{
			Rank:     1,
			UserId:   "buyer1",
			Nickname: "张三",
			Amount:   &v1.Money{Amount: 12000, Currency: "CNY"},
		}},
		RecentBids: []*v1.Bid{{
			UserId:   "buyer1",
			Nickname: "张三",
			Amount:   &v1.Money{Amount: 12000, Currency: "CNY"},
		}},
	}
}

func testBuyerScopedSnapshot() *v1.RoomSnapshot {
	return &v1.RoomSnapshot{
		RoomId: "room1",
		CurrentLot: &v1.Lot{
			Id:              "lot1",
			RoomId:          "room1",
			MainAccountId:   "main1",
			Status:          v1.LotStatus_LOT_STATUS_LIVE,
			Version:         7,
			LeadingUserId:   "buyer1",
			LeadingNickname: "张三",
			CurrentPrice:    &v1.Money{Amount: 12000, Currency: "CNY"},
			Stats:           &v1.LotStats{BidCount: 3},
		},
		Ranking: []*v1.RankingItem{
			{
				Rank:     1,
				UserId:   "buyer1",
				Nickname: "张三",
				Amount:   &v1.Money{Amount: 12000, Currency: "CNY"},
			},
			{
				Rank:     2,
				UserId:   "buyer2",
				Nickname: "李四",
				Amount:   &v1.Money{Amount: 11000, Currency: "CNY"},
			},
		},
		RecentBids: []*v1.Bid{
			{
				UserId:   "buyer1",
				Nickname: "张三",
				Amount:   &v1.Money{Amount: 12000, Currency: "CNY"},
			},
			{
				UserId:   "buyer2",
				Nickname: "李四",
				Amount:   &v1.Money{Amount: 11000, Currency: "CNY"},
			},
		},
	}
}

func issueAccessToken(t *testing.T, manager *auth.Manager, user *v1.User) string {
	t.Helper()
	pair, err := manager.IssueTokenPair(user)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return pair.AccessToken
}

func requestWSTicket(t *testing.T, baseURL, token, roomID, scope string, expectedStatus int) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"roomId": roomID, "scope": scope})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/realtime/ws-ticket", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new ticket request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request ticket: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var reply wsTicketReply
	_ = json.NewDecoder(response.Body).Decode(&reply)
	if response.StatusCode != expectedStatus {
		t.Fatalf("expected ticket status %d, got %d reply=%+v", expectedStatus, response.StatusCode, reply)
	}
	return reply.Ticket
}

func readRealtimeEnvelope(t *testing.T, conn *websocket.Conn) *v1.RealtimeEnvelopeV1 {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket message: %v", err)
	}
	envelope := &v1.RealtimeEnvelopeV1{}
	if err := protojson.Unmarshal(payload, envelope); err != nil {
		t.Fatalf("decode websocket envelope: %v payload=%s", err, string(payload))
	}
	return envelope
}

func allowedOriginHeader() http.Header {
	return http.Header{"Origin": []string{"https://admin.example.test"}}
}

func wsURL(baseURL, path string) string {
	return "ws" + strings.TrimPrefix(baseURL, "http") + path
}
