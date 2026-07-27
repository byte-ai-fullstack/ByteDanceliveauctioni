package projectionrepair

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

type syntheticBundleDocument struct {
	SchemaVersion     int                       `json:"schema_version"`
	Topic             string                    `json:"topic"`
	Partition         int32                     `json:"partition"`
	FromOffset        int64                     `json:"from_offset"`
	ToOffsetExclusive int64                     `json:"to_offset_exclusive"`
	PreparedBy        string                    `json:"prepared_by"`
	ChangeTicket      string                    `json:"change_ticket"`
	RepairReason      string                    `json:"repair_reason"`
	CreatedAtUnixMs   int64                     `json:"created_at_unix_ms"`
	Records           []syntheticRecordDocument `json:"records"`
}

type syntheticRecordDocument struct {
	SourceOffset  int64  `json:"source_offset"`
	RepairEventID string `json:"repair_event_id"`
	OwnerEpoch    int64  `json:"owner_epoch"`
	OutboxShard   int    `json:"outbox_shard"`
	RuntimeFact   string `json:"runtime_fact_base64"`
	PayloadSHA256 string `json:"payload_sha256"`
	EvidenceRef   string `json:"evidence_ref"`
}

func TestSyntheticBundleScansVerifiedRecordsMoreThanOnce(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 2)
	path, digest := writeSyntheticBundle(t, document)
	bundle, err := OpenSyntheticBundle(path, digest, now)
	if err != nil {
		t.Fatalf("OpenSyntheticBundle: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	for pass := 0; pass < 2; pass++ {
		var records []SyntheticRecord
		metadata, scanErr := bundle.Scan(func(record SyntheticRecord) error {
			records = append(records, record)
			return nil
		})
		if scanErr != nil {
			t.Fatalf("Scan pass %d: %v", pass, scanErr)
		}
		if metadata.BundleSHA256 != digest || metadata.RecordCount != 2 || metadata.FromOffset != 100 || metadata.ToOffsetExclusive != 102 {
			t.Fatalf("metadata=%+v", metadata)
		}
		if len(records) != 2 || records[0].SourceOffset != 100 || records[1].Fact.GetPrevLotVersion() != 7 {
			t.Fatalf("records=%+v", records)
		}
		decoded := records[0].Decoded(metadata)
		if decoded.Offset != 100 || decoded.Fact.GetEventId() != records[0].RepairEventID || decoded.PayloadHash != records[0].PayloadHash {
			t.Fatalf("decoded=%+v", decoded)
		}
	}
}

func TestSyntheticBundleRejectsUnsafeFilesAndBytes(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	document := syntheticBundleFixture(t, now, 1)
	path, digest := writeSyntheticBundle(t, document)

	t.Run("digest mismatch", func(t *testing.T) {
		_, err := OpenSyntheticBundle(path, strings.Repeat("0", 64), now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("uppercase digest", func(t *testing.T) {
		_, err := OpenSyntheticBundle(path, strings.ToUpper(digest), now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "bundle-link.json")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		_, err := OpenSyntheticBundle(link, digest, now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("invalid UTF-8", func(t *testing.T) {
		invalid := append(marshalSyntheticBundle(t, document), 0xff)
		invalidPath, invalidDigest := writeSyntheticBytes(t, invalid)
		_, err := OpenSyntheticBundle(invalidPath, invalidDigest, now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("not regular", func(t *testing.T) {
		directory := t.TempDir()
		_, err := OpenSyntheticBundle(directory, digest, now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("writable", func(t *testing.T) {
		writablePath, writableDigest := writeSyntheticBundle(t, document)
		if err := os.Chmod(writablePath, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := OpenSyntheticBundle(writablePath, writableDigest, now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("file size limit", func(t *testing.T) {
		oversizedPath := filepath.Join(t.TempDir(), "oversized.json")
		file, err := os.OpenFile(oversizedPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o400)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(MaxSyntheticBundleBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = OpenSyntheticBundle(oversizedPath, strings.Repeat("0", 64), now)
		if !errors.Is(err, ErrInvalidSyntheticBundle) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestSyntheticBundleRejectsStrictJSONAndContractViolations(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	base := syntheticBundleFixture(t, now, 2)
	tests := []struct {
		name   string
		mutate func(syntheticBundleDocument) []byte
	}{
		{name: "unknown top field", mutate: func(value syntheticBundleDocument) []byte {
			raw := marshalSyntheticBundle(t, value)
			return append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
		}},
		{name: "duplicate top field", mutate: func(value syntheticBundleDocument) []byte {
			raw := marshalSyntheticBundle(t, value)
			return []byte(strings.Replace(string(raw), `"topic":`, `"topic":"duplicate","topic":`, 1))
		}},
		{name: "unknown record field", mutate: func(value syntheticBundleDocument) []byte {
			raw := marshalSyntheticBundle(t, value)
			return []byte(strings.Replace(string(raw), `"source_offset":`, `"unexpected":true,"source_offset":`, 1))
		}},
		{name: "duplicate record field", mutate: func(value syntheticBundleDocument) []byte {
			raw := marshalSyntheticBundle(t, value)
			return []byte(strings.Replace(string(raw), `"source_offset":100`, `"source_offset":100,"source_offset":100`, 1))
		}},
		{name: "trailing JSON", mutate: func(value syntheticBundleDocument) []byte {
			return append(marshalSyntheticBundle(t, value), []byte(` {}`)...)
		}},
		{name: "control character", mutate: func(value syntheticBundleDocument) []byte {
			value.RepairReason = "bad\treason"
			return marshalSyntheticBundle(t, value)
		}},
		{name: "out of order offset", mutate: func(value syntheticBundleDocument) []byte {
			value.Records[1].SourceOffset++
			return marshalSyntheticBundle(t, value)
		}},
		{name: "duplicate event ID", mutate: func(value syntheticBundleDocument) []byte {
			value.Records[1].RepairEventID = value.Records[0].RepairEventID
			fact := decodeSyntheticFact(t, value.Records[1])
			fact.EventId = value.Records[0].RepairEventID
			setSyntheticPayload(t, &value.Records[1], fact)
			return marshalSyntheticBundle(t, value)
		}},
		{name: "lot version gap", mutate: func(value syntheticBundleDocument) []byte {
			fact := decodeSyntheticFact(t, value.Records[1])
			fact.PrevLotVersion++
			fact.LotVersion++
			fact.StateAfter.BidCount++
			setSyntheticPayload(t, &value.Records[1], fact)
			return marshalSyntheticBundle(t, value)
		}},
		{name: "malformed base64", mutate: func(value syntheticBundleDocument) []byte {
			value.Records[0].RuntimeFact = "***"
			return marshalSyntheticBundle(t, value)
		}},
		{name: "malformed protobuf", mutate: func(value syntheticBundleDocument) []byte {
			payload := []byte{0xff}
			hash := sha256.Sum256(payload)
			value.Records[0].RuntimeFact = base64.StdEncoding.EncodeToString(payload)
			value.Records[0].PayloadSHA256 = hex.EncodeToString(hash[:])
			return marshalSyntheticBundle(t, value)
		}},
		{name: "record size limit", mutate: func(value syntheticBundleDocument) []byte {
			raw := marshalSyntheticBundle(t, value)
			padding := `"padding":"` + strings.Repeat("a", maxSyntheticRecordJSONBytes) + `",`
			return []byte(strings.Replace(string(raw), `{"source_offset":`, `{`+padding+`"source_offset":`, 1))
		}},
		{name: "payload hash mismatch", mutate: func(value syntheticBundleDocument) []byte {
			value.Records[0].PayloadSHA256 = strings.Repeat("0", 64)
			return marshalSyntheticBundle(t, value)
		}},
		{name: "event ID mismatch", mutate: func(value syntheticBundleDocument) []byte {
			value.Records[0].RepairEventID = value.Records[1].RepairEventID
			return marshalSyntheticBundle(t, value)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := test.mutate(base)
			path, digest := writeSyntheticBytes(t, raw)
			bundle, err := OpenSyntheticBundle(path, digest, now)
			if err == nil {
				t.Cleanup(func() { _ = bundle.Close() })
				_, err = bundle.Scan(func(SyntheticRecord) error { return nil })
			}
			if !errors.Is(err, ErrInvalidSyntheticBundle) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func syntheticBundleFixture(t *testing.T, now time.Time, count int) syntheticBundleDocument {
	t.Helper()
	document := syntheticBundleDocument{
		SchemaVersion: SyntheticBundleSchemaVersion, Topic: eventcontract.RuntimeProjectionTopicV1,
		Partition: 2, FromOffset: 100, ToOffsetExclusive: 100 + int64(count),
		PreparedBy: "engineer-a", ChangeTicket: "INC-2026-0042",
		RepairReason: "restore retained-out runtime facts", CreatedAtUnixMs: now.Add(-time.Minute).UnixMilli(),
	}
	for index := 0; index < count; index++ {
		record := repairRecordFixture(t, document.Partition, document.FromOffset+int64(index), "lot-1", 6+int64(index), 7+int64(index))
		fact := new(v1.RuntimeFactV1)
		if err := proto.Unmarshal(record.Value, fact); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(record.Value)
		document.Records = append(document.Records, syntheticRecordDocument{
			SourceOffset: record.Offset, RepairEventID: fact.GetEventId(), OwnerEpoch: 1, OutboxShard: 0,
			RuntimeFact: base64.StdEncoding.EncodeToString(record.Value), PayloadSHA256: hex.EncodeToString(hash[:]),
			EvidenceRef: "INC-2026-0042/runtime-log",
		})
	}
	return document
}

func setSyntheticPayload(t *testing.T, record *syntheticRecordDocument, fact *v1.RuntimeFactV1) {
	t.Helper()
	payload, err := eventcontract.MarshalRuntimeFactBinary(fact)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	record.RepairEventID = fact.GetEventId()
	record.RuntimeFact = base64.StdEncoding.EncodeToString(payload)
	record.PayloadSHA256 = hex.EncodeToString(hash[:])
}

func decodeSyntheticFact(t *testing.T, record syntheticRecordDocument) *v1.RuntimeFactV1 {
	t.Helper()
	payload, err := base64.StdEncoding.DecodeString(record.RuntimeFact)
	if err != nil {
		t.Fatal(err)
	}
	fact := new(v1.RuntimeFactV1)
	if err := proto.Unmarshal(payload, fact); err != nil {
		t.Fatal(err)
	}
	return fact
}

func writeSyntheticBundle(t *testing.T, document syntheticBundleDocument) (string, string) {
	t.Helper()
	return writeSyntheticBytes(t, marshalSyntheticBundle(t, document))
}

func marshalSyntheticBundle(t *testing.T, document syntheticBundleDocument) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeSyntheticBytes(t *testing.T, raw []byte) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-repair.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	return path, hex.EncodeToString(hash[:])
}
