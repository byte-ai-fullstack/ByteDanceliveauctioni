package data

import (
	"errors"
	"testing"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

func TestClassifyRuntimeReconciliationCoversFailoverMatrix(t *testing.T) {
	runtime := reconcileRuntimeIdentity(t, 7, "019d3d40-6b8f-7abc-8def-0123456789ab")
	projection := runtimeProjectionIdentity{
		Found: true, LotID: runtime.LotID, RoomID: runtime.RoomID, ProjectionRoomID: runtime.RoomID,
		LastEventID: runtime.LastEventID, LotVersion: runtime.LotVersion, LotRowVersion: runtime.LotVersion,
		CanonicalHash: runtime.CanonicalHash, LotCore: runtimeCoreFromState(runtime.State),
	}
	tests := []struct {
		name        string
		mutate      func(*runtimeRedisIdentity, *runtimeProjectionIdentity)
		recoverable bool
		want        runtimeReconcileClass
		wantErr     error
	}{
		{name: "exact", want: runtimeReconcileExact},
		{name: "redis ahead recoverable", mutate: func(redis *runtimeRedisIdentity, _ *runtimeProjectionIdentity) {
			redis.LotVersion++
		}, recoverable: true, want: runtimeReconcileRecoverable},
		{name: "redis ahead unlocated", mutate: func(redis *runtimeRedisIdentity, _ *runtimeProjectionIdentity) {
			redis.LotVersion++
		}, want: runtimeReconcileUnlocated, wantErr: ErrRuntimeReconcilePending},
		{name: "mysql ahead", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.LotVersion++
			mysql.LotRowVersion++
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "same version event fork", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.LastEventID = "019d3d40-6b8f-7abc-8def-1123456789ab"
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "same version hash fork", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.CanonicalHash = "00000000000000000000000000000000"
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "projected row corruption", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.LotCore.CurrentPriceFen++
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "missing projection identity", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.Found = false
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "projection frozen", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.Frozen = true
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "projection and lot row versions differ", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.LotRowVersion--
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
		{name: "room fork", mutate: func(_ *runtimeRedisIdentity, mysql *runtimeProjectionIdentity) {
			mysql.ProjectionRoomID = "other-room"
		}, want: runtimeReconcileDiverged, wantErr: ErrRuntimeStateDiverged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redisIdentity := runtime
			mysqlIdentity := projection
			if test.mutate != nil {
				test.mutate(&redisIdentity, &mysqlIdentity)
			}
			got, err := classifyRuntimeReconciliation(redisIdentity, mysqlIdentity, test.recoverable)
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("class=%s error=%v want_class=%s want_error=%v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestRuntimeCoreFromStateUsesOnlyProjectedDiagnosticFields(t *testing.T) {
	state := &v1.LotRuntimeStateV1{
		Status: v1.LotStatus_LOT_STATUS_SETTLED, CurrentPriceFen: 12_300,
		WinnerUserId: "buyer-1", EndsAtUnixMs: 1_700_000_000_000,
	}
	if got := runtimeCoreFromState(state); got != (runtimeCoreState{
		Status: int32(v1.LotStatus_LOT_STATUS_SETTLED), CurrentPriceFen: 12_300,
		WinnerUserID: "buyer-1", EndsAtUnixMs: 1_700_000_000_000,
	}) {
		t.Fatalf("core=%+v", got)
	}
}

func reconcileRuntimeIdentity(t *testing.T, version int64, eventID string) runtimeRedisIdentity {
	t.Helper()
	state := &v1.LotRuntimeStateV1{
		LotId: "lot-reconcile", RoomId: "room-reconcile", Status: v1.LotStatus_LOT_STATUS_LIVE,
		Currency: "CNY", CurrentPriceFen: 10_500, EndsAtUnixMs: 1_700_000_000_000,
	}
	hash, err := eventcontract.CanonicalStateHash(state)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeRedisIdentity{
		LotID: state.GetLotId(), RoomID: state.GetRoomId(), LastEventID: eventID,
		LotVersion: version, CanonicalHash: hash, State: state,
	}
}
