package projectionrepair

import (
	"errors"

	"live-auction-bid/backend/app/auction/service/internal/worker/projector"
)

const MaxReplayRecords int64 = 1000

var (
	ErrUnsafeReplay       = errors.New("projector replay safety precondition failed")
	ErrUnsafeSynthetic    = errors.New("projector synthetic repair safety precondition failed")
	ErrVerificationFailed = errors.New("projector replay verification failed")
)

type PartitionOffset struct {
	Found       bool  `json:"found"`
	NextOffset  int64 `json:"next_offset"`
	UpdatedAtMs int64 `json:"updated_at_ms"`
}

type InboxEntry struct {
	EventID     string `json:"event_id"`
	Offset      int64  `json:"offset"`
	LotID       string `json:"lot_id"`
	LotVersion  int64  `json:"lot_version"`
	PayloadHash string `json:"payload_hash"`
	AppliedAtMs int64  `json:"applied_at_ms"`
}

type LotState struct {
	LotID                string `json:"lot_id"`
	ProjectionStateFound bool   `json:"projection_state_found"`
	LastEventID          string `json:"last_event_id"`
	LastLotVersion       int64  `json:"last_lot_version"`
	CanonicalHash        string `json:"canonical_hash"`
	Frozen               bool   `json:"frozen"`
	LastAppliedAtMs      int64  `json:"last_applied_at_ms"`
	LotVersion           int64  `json:"auction_lot_version"`
}

type RecordReport struct {
	Offset         int64  `json:"offset"`
	DecodeError    string `json:"decode_error,omitempty"`
	EventID        string `json:"event_id,omitempty"`
	LotID          string `json:"lot_id,omitempty"`
	PrevLotVersion int64  `json:"prev_lot_version,omitempty"`
	LotVersion     int64  `json:"lot_version,omitempty"`
	Command        string `json:"command,omitempty"`
	PayloadHash    string `json:"payload_hash,omitempty"`
	InboxStatus    string `json:"inbox_status"`
}

type DiagnoseRequest struct {
	Partition int32
	Before    int64
	After     int64
}

type DiagnoseReport struct {
	Topic            string                    `json:"topic"`
	Partition        int32                     `json:"partition"`
	DatabaseOffset   PartitionOffset           `json:"database_offset"`
	KafkaBounds      projector.PartitionBounds `json:"kafka_bounds"`
	RetentionCliff   bool                      `json:"retention_cliff"`
	ReplayPossible   bool                      `json:"replay_possible"`
	WindowStart      int64                     `json:"window_start"`
	WindowEnd        int64                     `json:"window_end_exclusive"`
	Records          []RecordReport            `json:"records"`
	Inbox            []InboxEntry              `json:"inbox"`
	ProjectionStates []LotState                `json:"projection_states"`
}

type ReplayRequest struct {
	Partition          int32
	ExpectedNextOffset int64
	ThroughOffset      int64
	Execute            bool
	Operator           string
	Reason             string
}

type AffectedLot struct {
	LotID                 string `json:"lot_id"`
	InitialVersion        int64  `json:"initial_version"`
	ExpectedEventID       string `json:"expected_last_event_id"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedCanonicalHash string `json:"expected_canonical_hash"`
	ActualEventID         string `json:"actual_last_event_id,omitempty"`
	ActualVersion         int64  `json:"actual_version,omitempty"`
	ActualCanonicalHash   string `json:"actual_canonical_hash,omitempty"`
	ActualLotVersion      int64  `json:"actual_auction_lot_version,omitempty"`
	Verified              bool   `json:"verified"`
}

type ReplayReport struct {
	Topic             string         `json:"topic"`
	Partition         int32          `json:"partition"`
	FromOffset        int64          `json:"from_offset"`
	ToOffsetExclusive int64          `json:"to_offset_exclusive"`
	Executed          bool           `json:"executed"`
	AuditID           string         `json:"audit_id,omitempty"`
	AppliedRecords    int            `json:"applied_records"`
	Records           []RecordReport `json:"records"`
	AffectedLots      []AffectedLot  `json:"affected_lots"`
	Verified          bool           `json:"verified"`
	ResumeSafe        bool           `json:"resume_safe"`
	RestartRequired   bool           `json:"restart_required"`
}

type SyntheticRequest struct {
	BundlePath     string
	ExpectedSHA256 string
	Execute        bool
	ExecutedBy     string
	Confirm        string
}

type SyntheticAuditHistory struct {
	Started   int `json:"started"`
	Failed    int `json:"failed"`
	Succeeded int `json:"succeeded"`
}

type SyntheticReport struct {
	Topic             string                    `json:"topic"`
	Partition         int32                     `json:"partition"`
	FromOffset        int64                     `json:"from_offset"`
	ToOffsetExclusive int64                     `json:"to_offset_exclusive"`
	BundleSHA256      string                    `json:"bundle_sha256"`
	PreparedBy        string                    `json:"prepared_by"`
	ChangeTicket      string                    `json:"change_ticket"`
	RepairReason      string                    `json:"repair_reason"`
	Executed          bool                      `json:"executed"`
	ExecutedBy        string                    `json:"executed_by,omitempty"`
	AuditID           string                    `json:"audit_id,omitempty"`
	DatabaseOffset    PartitionOffset           `json:"database_offset"`
	KafkaBounds       projector.PartitionBounds `json:"kafka_bounds"`
	PriorAudits       SyntheticAuditHistory     `json:"prior_audits"`
	InterruptedAudits int                       `json:"interrupted_audits"`
	CompletionOnly    bool                      `json:"completion_only"`
	PrefixRecords     int                       `json:"prefix_records"`
	SuffixRecords     int                       `json:"suffix_records"`
	AppliedRecords    int                       `json:"applied_records"`
	Records           []RecordReport            `json:"records"`
	AffectedLots      []AffectedLot             `json:"affected_lots"`
	Verified          bool                      `json:"verified"`
	ResumeSafe        bool                      `json:"resume_safe"`
	RestartRequired   bool                      `json:"restart_required"`
}
