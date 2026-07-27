package enrichment

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/orderenrichment"
)

func TestSQLStoreApplyWritesReadySnapshotsOnce(t *testing.T) {
	record := decodedEnrichmentFixture(t)
	address := orderenrichment.AddressSnapshot{
		AddressID: "address-1", ReceiverName: "Buyer", Phone: "13800000000", Province: "浙江省", City: "杭州市", Detail: "1号", FullAddress: "浙江省杭州市1号",
	}
	addressJSON, err := json.Marshal(address)
	if err != nil {
		t.Fatal(err)
	}
	tx := &fakeEnrichmentTx{
		rows: []rowScanner{
			fakeRowError{sql.ErrNoRows},
			fakeRowError{sql.ErrNoRows},
			fakeRowValues{"buyer-1", "main-1"},
			fakeRowValues{1},
			fakeRowValues{"address-1", addressJSON},
			fakeRowValues{"Main Shop", "main_user"},
		},
		execResult: driver.RowsAffected(1),
	}
	store, err := newSQLStore(&fakeEnrichmentDB{tx: tx})
	if err != nil {
		t.Fatal(err)
	}
	store.nowMs = func() int64 { return 1_700_000_000_100 }
	result, err := store.Apply(context.Background(), record, 1)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Duplicate || result.Status != orderenrichment.StatusReady {
		t.Fatalf("Apply() result = %+v", result)
	}
	if tx.commits != 1 || tx.rollbacks != 0 || len(tx.rows) != 0 || len(tx.execArgs) != 1 {
		t.Fatalf("transaction state = %+v", tx)
	}
	args := tx.execArgs[0]
	if got := args[5]; got != string(orderenrichment.StatusReady) {
		t.Fatalf("persisted status = %v", got)
	}
	if got := args[6]; got != 1 {
		t.Fatalf("persisted attempts = %v", got)
	}
	if got := args[7]; got != "" {
		t.Fatalf("persisted last_error = %v", got)
	}
	var persistedAddress orderenrichment.AddressSnapshot
	if err := json.Unmarshal(args[3].([]byte), &persistedAddress); err != nil || persistedAddress.AddressID != "address-1" {
		t.Fatalf("persisted address = %+v, error = %v", persistedAddress, err)
	}
	var persistedShop orderenrichment.ShopSnapshot
	if err := json.Unmarshal(args[4].([]byte), &persistedShop); err != nil || persistedShop.ShopID != "main-1" || persistedShop.ShopName != "Main Shop" {
		t.Fatalf("persisted shop = %+v, error = %v", persistedShop, err)
	}
}

func TestSQLStoreApplyPersistsPartialInsteadOfBlockingOnMissingOptionalSources(t *testing.T) {
	record := decodedEnrichmentFixture(t)
	tx := &fakeEnrichmentTx{
		rows: []rowScanner{
			fakeRowError{sql.ErrNoRows},
			fakeRowError{sql.ErrNoRows},
			fakeRowValues{"buyer-1", "main-1"},
			fakeRowValues{1},
			fakeRowError{sql.ErrNoRows},
			fakeRowError{sql.ErrNoRows},
			fakeRowError{sql.ErrNoRows},
		},
		execResult: driver.RowsAffected(1),
	}
	store, _ := newSQLStore(&fakeEnrichmentDB{tx: tx})
	store.nowMs = func() int64 { return 1_700_000_000_100 }
	result, err := store.Apply(context.Background(), record, 3)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Status != orderenrichment.StatusPartial {
		t.Fatalf("Apply() result = %+v", result)
	}
	args := tx.execArgs[0]
	if args[3] != nil || args[4] != nil || args[6] != 3 || args[7] != "address_not_found;shop_not_found" {
		t.Fatalf("partial insert args = %#v", args)
	}
}

func TestSQLStoreApplyTreatsSameMessageAndPayloadAsDuplicate(t *testing.T) {
	record := decodedEnrichmentFixture(t)
	tx := &fakeEnrichmentTx{rows: []rowScanner{fakeRowValues{
		record.Event.GetOrderId(), record.MessageID, record.PayloadHash, orderenrichment.StatusReady,
	}}}
	store, _ := newSQLStore(&fakeEnrichmentDB{tx: tx})
	result, err := store.Apply(context.Background(), record, 1)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !result.Duplicate || result.Status != orderenrichment.StatusReady || tx.commits != 1 || len(tx.execArgs) != 0 {
		t.Fatalf("duplicate result = %+v, tx = %+v", result, tx)
	}
}

func TestSQLStoreApplyRejectsMessageIdentityAndSourceCorruption(t *testing.T) {
	record := decodedEnrichmentFixture(t)
	conflictTx := &fakeEnrichmentTx{rows: []rowScanner{fakeRowValues{
		record.Event.GetOrderId(), record.MessageID, "different-hash", orderenrichment.StatusReady,
	}}}
	store, _ := newSQLStore(&fakeEnrichmentDB{tx: conflictTx})
	if _, err := store.Apply(context.Background(), record, 1); !errors.Is(err, ErrMessageIdentityConflict) {
		t.Fatalf("identity conflict error = %v", err)
	}
	if conflictTx.rollbacks != 1 {
		t.Fatalf("identity conflict rollbacks = %d", conflictTx.rollbacks)
	}

	badSnapshot, _ := json.Marshal(orderenrichment.AddressSnapshot{AddressID: "other", FullAddress: "somewhere"})
	corruptTx := &fakeEnrichmentTx{rows: []rowScanner{
		fakeRowError{sql.ErrNoRows}, fakeRowError{sql.ErrNoRows}, fakeRowValues{"buyer-1", "main-1"}, fakeRowValues{1}, fakeRowValues{"address-1", badSnapshot},
	}}
	store, _ = newSQLStore(&fakeEnrichmentDB{tx: corruptTx})
	if _, err := store.Apply(context.Background(), record, 1); !errors.Is(err, ErrEnrichmentSourceCorrupt) {
		t.Fatalf("source corruption error = %v", err)
	}
}

func TestSQLStoreApplyRejectsMutatedDecodedRecord(t *testing.T) {
	record := decodedEnrichmentFixture(t)
	record.PayloadHash = "wrong"
	store, _ := newSQLStore(&fakeEnrichmentDB{tx: &fakeEnrichmentTx{}})
	if _, err := store.Apply(context.Background(), record, 1); !errors.Is(err, ErrInvalidApplyRecord) {
		t.Fatalf("invalid record error = %v", err)
	}
	if _, err := NewSQLStore(nil); !errors.Is(err, ErrInvalidStoreArgument) {
		t.Fatalf("NewSQLStore(nil) error = %v", err)
	}
}

func decodedEnrichmentFixture(t *testing.T) Record {
	t.Helper()
	decoded, err := DecodeRecord(validKafkaRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

type fakeEnrichmentDB struct {
	tx       *fakeEnrichmentTx
	beginErr error
}

func (db *fakeEnrichmentDB) BeginTx(context.Context, *sql.TxOptions) (enrichmentTransaction, error) {
	if db.beginErr != nil {
		return nil, db.beginErr
	}
	return db.tx, nil
}

type fakeEnrichmentTx struct {
	rows       []rowScanner
	execResult sql.Result
	execErr    error
	execArgs   [][]any
	commits    int
	rollbacks  int
	committed  bool
}

func (tx *fakeEnrichmentTx) QueryRowContext(context.Context, string, ...any) rowScanner {
	if len(tx.rows) == 0 {
		return fakeRowError{errors.New("unexpected query")}
	}
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}

func (tx *fakeEnrichmentTx) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	tx.execArgs = append(tx.execArgs, append([]any(nil), args...))
	return tx.execResult, tx.execErr
}

func (tx *fakeEnrichmentTx) Commit() error {
	tx.commits++
	tx.committed = true
	return nil
}

func (tx *fakeEnrichmentTx) Rollback() error {
	if !tx.committed {
		tx.rollbacks++
	}
	return nil
}

type fakeRowError struct{ err error }

func (row fakeRowError) Scan(...any) error { return row.err }

type fakeRowValues []any

func (row fakeRowValues) Scan(dest ...any) error {
	if len(dest) != len(row) {
		return errors.New("scan destination count mismatch")
	}
	for index := range dest {
		target := reflect.ValueOf(dest[index])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("scan destination must be a non-nil pointer")
		}
		value := reflect.ValueOf(row[index])
		if !value.IsValid() {
			target.Elem().SetZero()
			continue
		}
		if value.Type().AssignableTo(target.Elem().Type()) {
			target.Elem().Set(value)
			continue
		}
		if value.Type().ConvertibleTo(target.Elem().Type()) {
			target.Elem().Set(value.Convert(target.Elem().Type()))
			continue
		}
		return errors.New("scan value type mismatch")
	}
	return nil
}
