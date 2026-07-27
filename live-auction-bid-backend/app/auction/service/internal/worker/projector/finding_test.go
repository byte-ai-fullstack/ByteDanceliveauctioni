package projector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRecordProjectionFindingFreezesAndWritesStructuredEvidence(t *testing.T) {
	record := decodedRuntimeFixture(t)
	tx := &scriptedProjectionTx{t: t, execs: []scriptedExec{
		{contains: "INSERT INTO auction_lot_projection_state", rows: 1},
		{contains: "INSERT INTO auction_reconcile_findings", rows: 1, check: func(args []any) {
			if args[0] != FindingEventIdentityConflict || args[1] != record.Fact.GetLotId() || args[2] != FindingSeverityP0 {
				t.Fatalf("finding args=%v", args)
			}
			var detail map[string]any
			if err := json.Unmarshal(args[3].([]byte), &detail); err != nil {
				t.Fatalf("decode finding detail: %v", err)
			}
			if detail["event_id"] != record.Fact.GetEventId() || strings.Contains(detail["error"].(string), "\n") {
				t.Fatalf("finding detail=%v", detail)
			}
		}},
	}}
	if err := recordProjectionFinding(context.Background(), tx, record, FindingEventIdentityConflict, FindingSeverityP0, true, errors.New("identity\nconflict"), 123); err != nil {
		t.Fatalf("recordProjectionFinding: %v", err)
	}
	tx.assertDrained()
}

func TestSQLStoreRecordFindingUsesIndependentTransaction(t *testing.T) {
	record := decodedRuntimeFixture(t)
	state := &scriptedDBState{execRows: []int64{1, 1}}
	db := sql.OpenDB(scriptedConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.nowMs = func() int64 { return 123 }
	if err := store.RecordFinding(context.Background(), record, FindingProjectionConflict, FindingSeverityP0, true, ErrEventIdentityConflict); err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction counts begin=%d commit=%d rollback=%d", state.begins, state.commits, state.rollbacks)
	}
	state.assertDrained(t)
}

func TestFindingValidationRejectsInvalidInputs(t *testing.T) {
	record := decodedRuntimeFixture(t)
	if err := recordProjectionFinding(context.Background(), &scriptedProjectionTx{t: t}, record, "", FindingSeverityP1, false, errors.New("gap"), 1); err == nil {
		t.Fatal("empty finding kind was accepted")
	}
	if validFinding(FindingRuntimeVersionGap, "P2") {
		t.Fatal("unknown severity was accepted")
	}
	if got := truncateFindingText(strings.Repeat("x", 600), 512); len(got) != 512 {
		t.Fatalf("truncated length=%d", len(got))
	}
	var nilStore *SQLStore
	if err := nilStore.RecordFinding(context.Background(), record, FindingRuntimeVersionGap, FindingSeverityP1, false, errors.New("gap")); err == nil {
		t.Fatal("nil store finding was accepted")
	}
	state := &scriptedDBState{}
	db := sql.OpenDB(scriptedConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewSQLStore(db)
	if err := store.RecordFinding(context.Background(), record, "", FindingSeverityP1, false, errors.New("gap")); err == nil {
		t.Fatal("invalid public finding was accepted")
	}
}
