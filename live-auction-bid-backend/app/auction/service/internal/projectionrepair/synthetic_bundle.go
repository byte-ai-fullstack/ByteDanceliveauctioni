package projectionrepair

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/data"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

const (
	SyntheticBundleSchemaVersion       = 1
	MaxSyntheticBundleBytes      int64 = 384 << 20
	maxSyntheticRecordJSONBytes        = 512 << 10
	syntheticCreatedFutureSkew         = 5 * time.Minute
)

var ErrInvalidSyntheticBundle = errors.New("invalid synthetic projection repair bundle")

type SyntheticBundleMetadata struct {
	SchemaVersion     int    `json:"schema_version"`
	Topic             string `json:"topic"`
	Partition         int32  `json:"partition"`
	FromOffset        int64  `json:"from_offset"`
	ToOffsetExclusive int64  `json:"to_offset_exclusive"`
	PreparedBy        string `json:"prepared_by"`
	ChangeTicket      string `json:"change_ticket"`
	RepairReason      string `json:"repair_reason"`
	CreatedAtUnixMs   int64  `json:"created_at_unix_ms"`
	RecordCount       int    `json:"record_count"`
	BundleSHA256      string `json:"bundle_sha256"`
}

type SyntheticRecord struct {
	SourceOffset  int64
	RepairEventID string
	OwnerEpoch    int64
	OutboxShard   int
	Payload       []byte
	PayloadHash   string
	EvidenceRef   string
	Fact          *v1.RuntimeFactV1
}

func (record SyntheticRecord) Decoded(metadata SyntheticBundleMetadata) projector.DecodedRecord {
	return projector.DecodedRecord{
		Topic: metadata.Topic, Partition: metadata.Partition, Offset: record.SourceOffset,
		Fact: record.Fact, Payload: append([]byte(nil), record.Payload...), PayloadHash: record.PayloadHash,
		OwnerEpoch: record.OwnerEpoch, OutboxShard: record.OutboxShard,
	}
}

type VerifiedSyntheticBundle struct {
	file           *os.File
	expectedDigest string
	now            time.Time
}

func OpenSyntheticBundle(path, expectedDigest string, now time.Time) (*VerifiedSyntheticBundle, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, '\x00') {
		return nil, fmt.Errorf("%w: bundle path is invalid", ErrInvalidSyntheticBundle)
	}
	if !validLowerHexDigest(expectedDigest) {
		return nil, fmt.Errorf("%w: expected SHA-256 must be 64 lowercase hexadecimal characters", ErrInvalidSyntheticBundle)
	}
	if now.IsZero() {
		return nil, fmt.Errorf("%w: current time is required", ErrInvalidSyntheticBundle)
	}
	file, err := openBundleNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open bundle: %v", ErrInvalidSyntheticBundle, err)
	}
	bundle := &VerifiedSyntheticBundle{file: file, expectedDigest: expectedDigest, now: now}
	if err := bundle.verifyFile(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return bundle, nil
}

func (bundle *VerifiedSyntheticBundle) Scan(consume func(SyntheticRecord) error) (SyntheticBundleMetadata, error) {
	if bundle == nil || bundle.file == nil {
		return SyntheticBundleMetadata{}, errors.New("synthetic bundle is closed")
	}
	if consume == nil {
		return SyntheticBundleMetadata{}, errors.New("synthetic bundle record consumer is required")
	}
	if err := bundle.verifyFile(); err != nil {
		return SyntheticBundleMetadata{}, err
	}
	metadata, err := readSyntheticBundleHeader(bundle.file, bundle.now)
	if err != nil {
		return SyntheticBundleMetadata{}, err
	}
	metadata.BundleSHA256 = bundle.expectedDigest
	if _, err := bundle.file.Seek(0, io.SeekStart); err != nil {
		return SyntheticBundleMetadata{}, fmt.Errorf("%w: rewind bundle records: %v", ErrInvalidSyntheticBundle, err)
	}
	if err := readSyntheticBundleRecords(bundle.file, metadata, consume); err != nil {
		return SyntheticBundleMetadata{}, err
	}
	return metadata, nil
}

func (bundle *VerifiedSyntheticBundle) Close() error {
	if bundle == nil || bundle.file == nil {
		return nil
	}
	err := bundle.file.Close()
	bundle.file = nil
	return err
}

func (bundle *VerifiedSyntheticBundle) verifyFile() error {
	info, err := bundle.file.Stat()
	if err != nil {
		return fmt.Errorf("%w: stat bundle: %v", ErrInvalidSyntheticBundle, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 || info.Size() <= 0 || info.Size() > MaxSyntheticBundleBytes {
		return fmt.Errorf("%w: bundle must be a non-empty read-only regular file no larger than %d bytes", ErrInvalidSyntheticBundle, MaxSyntheticBundleBytes)
	}
	if _, err := bundle.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: rewind bundle: %v", ErrInvalidSyntheticBundle, err)
	}
	digest, err := hashValidUTF8(bundle.file)
	if err != nil {
		return err
	}
	if digest != bundle.expectedDigest {
		return fmt.Errorf("%w: bundle SHA-256 mismatch", ErrInvalidSyntheticBundle)
	}
	if _, err := bundle.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: rewind verified bundle: %v", ErrInvalidSyntheticBundle, err)
	}
	return nil
}

func hashValidUTF8(reader io.Reader) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	carry := make([]byte, 0, utf8.UTFMax)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			if _, hashErr := hash.Write(chunk); hashErr != nil {
				return "", fmt.Errorf("%w: hash bundle: %v", ErrInvalidSyntheticBundle, hashErr)
			}
			data := make([]byte, 0, len(carry)+len(chunk))
			data = append(data, carry...)
			data = append(data, chunk...)
			carry = carry[:0]
			for index := 0; index < len(data); {
				if !utf8.FullRune(data[index:]) {
					carry = append(carry, data[index:]...)
					break
				}
				runeValue, size := utf8.DecodeRune(data[index:])
				if runeValue == utf8.RuneError && size == 1 {
					return "", fmt.Errorf("%w: bundle is not valid UTF-8", ErrInvalidSyntheticBundle)
				}
				index += size
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: read bundle: %v", ErrInvalidSyntheticBundle, err)
		}
	}
	if len(carry) != 0 {
		return "", fmt.Errorf("%w: bundle ends with incomplete UTF-8", ErrInvalidSyntheticBundle)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readSyntheticBundleHeader(reader io.Reader, now time.Time) (SyntheticBundleMetadata, error) {
	decoder := json.NewDecoder(bufio.NewReader(reader))
	metadata, recordCount, err := decodeSyntheticTopLevel(decoder, nil)
	if err != nil {
		return SyntheticBundleMetadata{}, err
	}
	metadata.RecordCount = recordCount
	if err := validateSyntheticMetadata(metadata, now); err != nil {
		return SyntheticBundleMetadata{}, err
	}
	return metadata, nil
}

func readSyntheticBundleRecords(reader io.Reader, expected SyntheticBundleMetadata, consume func(SyntheticRecord) error) error {
	decoder := json.NewDecoder(bufio.NewReader(reader))
	seenEventIDs := make(map[string]struct{}, expected.RecordCount)
	lastVersion := make(map[string]int64)
	index := 0
	handleRecord := func(record SyntheticRecord) error {
		wantOffset := expected.FromOffset + int64(index)
		if record.SourceOffset != wantOffset {
			return fmt.Errorf("%w: record %d source_offset=%d want=%d", ErrInvalidSyntheticBundle, index, record.SourceOffset, wantOffset)
		}
		if _, duplicate := seenEventIDs[record.RepairEventID]; duplicate {
			return fmt.Errorf("%w: duplicate repair_event_id", ErrInvalidSyntheticBundle)
		}
		seenEventIDs[record.RepairEventID] = struct{}{}
		if previous, exists := lastVersion[record.Fact.GetLotId()]; exists && record.Fact.GetPrevLotVersion() != previous {
			return fmt.Errorf("%w: lot %s internal version gap got prev=%d want=%d", ErrInvalidSyntheticBundle, record.Fact.GetLotId(), record.Fact.GetPrevLotVersion(), previous)
		}
		lastVersion[record.Fact.GetLotId()] = record.Fact.GetLotVersion()
		if err := consume(record); err != nil {
			return err
		}
		index++
		return nil
	}
	actual, recordCount, err := decodeSyntheticTopLevel(decoder, handleRecord)
	if err != nil {
		return err
	}
	actual.RecordCount = recordCount
	actual.BundleSHA256 = expected.BundleSHA256
	if actual != expected || recordCount != expected.RecordCount || index != expected.RecordCount {
		return fmt.Errorf("%w: bundle metadata changed between passes", ErrInvalidSyntheticBundle)
	}
	return nil
}

func decodeSyntheticTopLevel(decoder *json.Decoder, consume func(SyntheticRecord) error) (SyntheticBundleMetadata, int, error) {
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return SyntheticBundleMetadata{}, 0, err
	}
	var metadata SyntheticBundleMetadata
	seen := make(map[string]struct{}, 10)
	recordCount := 0
	for decoder.More() {
		name, err := readJSONFieldName(decoder, seen)
		if err != nil {
			return SyntheticBundleMetadata{}, 0, err
		}
		switch name {
		case "schema_version":
			err = decoder.Decode(&metadata.SchemaVersion)
		case "topic":
			err = decoder.Decode(&metadata.Topic)
		case "partition":
			err = decoder.Decode(&metadata.Partition)
		case "from_offset":
			err = decoder.Decode(&metadata.FromOffset)
		case "to_offset_exclusive":
			err = decoder.Decode(&metadata.ToOffsetExclusive)
		case "prepared_by":
			err = decoder.Decode(&metadata.PreparedBy)
		case "change_ticket":
			err = decoder.Decode(&metadata.ChangeTicket)
		case "repair_reason":
			err = decoder.Decode(&metadata.RepairReason)
		case "created_at_unix_ms":
			err = decoder.Decode(&metadata.CreatedAtUnixMs)
		case "records":
			recordCount, err = decodeSyntheticRecordArray(decoder, consume)
		default:
			return SyntheticBundleMetadata{}, 0, fmt.Errorf("%w: unknown top-level field %q", ErrInvalidSyntheticBundle, name)
		}
		if err != nil {
			return SyntheticBundleMetadata{}, 0, fmt.Errorf("%w: decode field %s: %v", ErrInvalidSyntheticBundle, name, err)
		}
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		return SyntheticBundleMetadata{}, 0, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return SyntheticBundleMetadata{}, 0, err
	}
	for _, required := range []string{
		"schema_version", "topic", "partition", "from_offset", "to_offset_exclusive",
		"prepared_by", "change_ticket", "repair_reason", "created_at_unix_ms", "records",
	} {
		if _, exists := seen[required]; !exists {
			return SyntheticBundleMetadata{}, 0, fmt.Errorf("%w: missing top-level field %s", ErrInvalidSyntheticBundle, required)
		}
	}
	return metadata, recordCount, nil
}

func decodeSyntheticRecordArray(decoder *json.Decoder, consume func(SyntheticRecord) error) (int, error) {
	if err := expectJSONDelimiter(decoder, '['); err != nil {
		return 0, err
	}
	count := 0
	for decoder.More() {
		if count >= int(MaxReplayRecords) {
			return 0, fmt.Errorf("%w: record count exceeds %d", ErrInvalidSyntheticBundle, MaxReplayRecords)
		}
		if consume == nil {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return 0, err
			}
			if len(raw) == 0 || len(raw) > maxSyntheticRecordJSONBytes {
				return 0, fmt.Errorf("%w: record JSON size is invalid", ErrInvalidSyntheticBundle)
			}
		} else {
			record, err := decodeSyntheticRecord(decoder)
			if err != nil {
				return 0, err
			}
			if err := consume(record); err != nil {
				return 0, err
			}
		}
		count++
	}
	if err := expectJSONDelimiter(decoder, ']'); err != nil {
		return 0, err
	}
	return count, nil
}

func decodeSyntheticRecord(decoder *json.Decoder) (SyntheticRecord, error) {
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return SyntheticRecord{}, err
	}
	seen := make(map[string]struct{}, 7)
	var (
		record        SyntheticRecord
		payloadBase64 string
	)
	for decoder.More() {
		name, err := readJSONFieldName(decoder, seen)
		if err != nil {
			return SyntheticRecord{}, err
		}
		switch name {
		case "source_offset":
			err = decoder.Decode(&record.SourceOffset)
		case "repair_event_id":
			err = decoder.Decode(&record.RepairEventID)
		case "owner_epoch":
			err = decoder.Decode(&record.OwnerEpoch)
		case "outbox_shard":
			err = decoder.Decode(&record.OutboxShard)
		case "runtime_fact_base64":
			err = decoder.Decode(&payloadBase64)
		case "payload_sha256":
			err = decoder.Decode(&record.PayloadHash)
		case "evidence_ref":
			err = decoder.Decode(&record.EvidenceRef)
		default:
			return SyntheticRecord{}, fmt.Errorf("%w: unknown record field %q", ErrInvalidSyntheticBundle, name)
		}
		if err != nil {
			return SyntheticRecord{}, err
		}
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		return SyntheticRecord{}, err
	}
	for _, required := range []string{
		"source_offset", "repair_event_id", "owner_epoch", "outbox_shard",
		"runtime_fact_base64", "payload_sha256", "evidence_ref",
	} {
		if _, exists := seen[required]; !exists {
			return SyntheticRecord{}, fmt.Errorf("%w: missing record field %s", ErrInvalidSyntheticBundle, required)
		}
	}
	if err := validateSyntheticRecord(&record, payloadBase64); err != nil {
		return SyntheticRecord{}, err
	}
	return record, nil
}

func validateSyntheticMetadata(metadata SyntheticBundleMetadata, now time.Time) error {
	if metadata.SchemaVersion != SyntheticBundleSchemaVersion || metadata.Topic != eventcontract.RuntimeProjectionTopicV1 || metadata.Partition < 0 {
		return fmt.Errorf("%w: schema, topic, or partition is invalid", ErrInvalidSyntheticBundle)
	}
	width := metadata.ToOffsetExclusive - metadata.FromOffset
	if metadata.FromOffset < 0 || width <= 0 || width > MaxReplayRecords || int64(metadata.RecordCount) != width {
		return fmt.Errorf("%w: offset range and record count must match within the replay limit", ErrInvalidSyntheticBundle)
	}
	if !validAuditText(metadata.PreparedBy, 128, false) || !validAuditText(metadata.ChangeTicket, 128, false) ||
		!validAuditText(metadata.RepairReason, 512, false) {
		return fmt.Errorf("%w: preparer, ticket, or reason is invalid", ErrInvalidSyntheticBundle)
	}
	if metadata.CreatedAtUnixMs <= 0 || metadata.CreatedAtUnixMs > now.Add(syntheticCreatedFutureSkew).UnixMilli() {
		return fmt.Errorf("%w: created_at_unix_ms is invalid", ErrInvalidSyntheticBundle)
	}
	return nil
}

func validateSyntheticRecord(record *SyntheticRecord, payloadBase64 string) error {
	if record.SourceOffset < 0 || record.OwnerEpoch <= 0 || record.OutboxShard < 0 || record.OutboxShard >= data.RuntimeOutboxShardCount {
		return fmt.Errorf("%w: record source metadata is invalid", ErrInvalidSyntheticBundle)
	}
	if !validAuditText(record.EvidenceRef, 512, false) || !validLowerHexDigest(record.PayloadHash) {
		return fmt.Errorf("%w: record evidence reference or payload hash is invalid", ErrInvalidSyntheticBundle)
	}
	if len(payloadBase64) == 0 || len(payloadBase64) > base64.StdEncoding.EncodedLen(eventcontract.MaxRuntimeFactBytes) {
		return fmt.Errorf("%w: encoded Runtime Fact size is invalid", ErrInvalidSyntheticBundle)
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(payloadBase64)
	if err != nil || len(payload) == 0 || len(payload) > eventcontract.MaxRuntimeFactBytes {
		return fmt.Errorf("%w: decode Runtime Fact payload", ErrInvalidSyntheticBundle)
	}
	hash := sha256.Sum256(payload)
	if hex.EncodeToString(hash[:]) != record.PayloadHash {
		return fmt.Errorf("%w: Runtime Fact payload hash mismatch", ErrInvalidSyntheticBundle)
	}
	fact := new(v1.RuntimeFactV1)
	if err := proto.Unmarshal(payload, fact); err != nil {
		return fmt.Errorf("%w: decode Runtime Fact protobuf: %v", ErrInvalidSyntheticBundle, err)
	}
	if err := eventcontract.ValidateRuntimeFact(fact); err != nil {
		return fmt.Errorf("%w: Runtime Fact contract: %v", ErrInvalidSyntheticBundle, err)
	}
	canonical, err := eventcontract.MarshalRuntimeFactBinary(fact)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("%w: Runtime Fact payload is not deterministic canonical protobuf", ErrInvalidSyntheticBundle)
	}
	if record.RepairEventID != fact.GetEventId() {
		return fmt.Errorf("%w: repair_event_id does not match Runtime Fact", ErrInvalidSyntheticBundle)
	}
	record.Payload = payload
	record.Fact = fact
	return nil
}

func validAuditText(value string, limit int, allowNewline bool) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > limit || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, runeValue := range value {
		if unicode.IsControl(runeValue) && (!allowNewline || (runeValue != '\r' && runeValue != '\n')) {
			return false
		}
	}
	return true
}

func validLowerHexDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func readJSONFieldName(decoder *json.Decoder, seen map[string]struct{}) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	name, ok := token.(string)
	if !ok {
		return "", fmt.Errorf("%w: JSON object field name is invalid", ErrInvalidSyntheticBundle)
	}
	if _, duplicate := seen[name]; duplicate {
		return "", fmt.Errorf("%w: duplicate JSON field %q", ErrInvalidSyntheticBundle, name)
	}
	seen[name] = struct{}{}
	return name, nil
}

func expectJSONDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: read JSON delimiter: %v", ErrInvalidSyntheticBundle, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != expected {
		return fmt.Errorf("%w: expected JSON delimiter %q", ErrInvalidSyntheticBundle, expected)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON content", ErrInvalidSyntheticBundle)
		}
		return fmt.Errorf("%w: trailing JSON: %v", ErrInvalidSyntheticBundle, err)
	}
	return nil
}
