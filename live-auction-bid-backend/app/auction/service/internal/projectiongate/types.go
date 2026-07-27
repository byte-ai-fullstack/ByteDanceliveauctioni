package projectiongate

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	ReasonHealthy                = "healthy"
	ReasonUninitialized          = "uninitialized"
	ReasonRecovering             = "recovering"
	ReasonKafkaUnavailable       = "kafka_unavailable"
	ReasonMySQLUnavailable       = "mysql_unavailable"
	ReasonPartitionMismatch      = "partition_mismatch"
	ReasonOffsetMissing          = "offset_missing"
	ReasonRetentionCliff         = "retention_cliff"
	ReasonOffsetAhead            = "offset_ahead"
	ReasonRecordMissing          = "record_missing"
	ReasonRecordTimestampInvalid = "record_timestamp_invalid"
	ReasonLagLimit               = "lag_limit"
	ReasonOldestAgeLimit         = "oldest_age_limit"
	ReasonRetentionHeadroom      = "retention_headroom"
	ReasonSnapshotStale          = "snapshot_stale"
)

var ErrClosed = errors.New("end-to-end projection gate is closed")

type Config struct {
	RefreshInterval       time.Duration
	RefreshTimeout        time.Duration
	MaxStaleness          time.Duration
	MaxLagRecords         int64
	MaxOldestAge          time.Duration
	RuntimeTopicRetention time.Duration
	MinRetentionHeadroom  time.Duration
	HealthyPollsToOpen    int
}

func DefaultConfig() Config {
	return Config{
		RefreshInterval:       2 * time.Second,
		RefreshTimeout:        1500 * time.Millisecond,
		MaxStaleness:          6 * time.Second,
		MaxLagRecords:         1000,
		MaxOldestAge:          30 * time.Second,
		RuntimeTopicRetention: 90 * 24 * time.Hour,
		MinRetentionHeadroom:  7 * 24 * time.Hour,
		HealthyPollsToOpen:    3,
	}
}

func (config Config) Validate() error {
	if config.RefreshInterval <= 0 || config.RefreshTimeout <= 0 ||
		config.RefreshTimeout >= config.RefreshInterval {
		return errors.New("projection gate refresh interval and timeout are invalid")
	}
	if config.MaxStaleness < config.RefreshInterval ||
		config.MaxStaleness-config.RefreshInterval < config.RefreshInterval {
		return errors.New("projection gate max staleness must cover at least two refresh intervals")
	}
	if config.MaxLagRecords <= 0 || config.MaxOldestAge <= 0 {
		return errors.New("projection gate lag and oldest-age limits must be positive")
	}
	if config.RuntimeTopicRetention <= 0 || config.MinRetentionHeadroom <= 0 ||
		config.MinRetentionHeadroom >= config.RuntimeTopicRetention {
		return errors.New("projection gate retention and headroom are invalid")
	}
	if config.HealthyPollsToOpen <= 0 || config.HealthyPollsToOpen > 100 {
		return errors.New("projection gate healthy-poll count must be within [1,100]")
	}
	return nil
}

type PartitionBounds struct {
	Earliest int64
	Latest   int64
}

type ProjectionOffset struct {
	NextOffset  int64
	UpdatedAtMs int64
}

type OldestRecord struct {
	Offset    int64
	Timestamp time.Time
}

type PartitionSnapshot struct {
	Partition       int32 `json:"partition"`
	Earliest        int64 `json:"earliest"`
	DatabaseNext    int64 `json:"database_next_offset"`
	Latest          int64 `json:"latest"`
	LagRecords      int64 `json:"lag_records"`
	OldestAgeMs     int64 `json:"oldest_age_ms"`
	OldestTimestamp int64 `json:"oldest_timestamp_ms,omitempty"`
}

type Snapshot struct {
	Ready                   bool                `json:"ready"`
	Reason                  string              `json:"reason"`
	CheckedAtMs             int64               `json:"checked_at_ms"`
	LastSuccessfulCheckAtMs int64               `json:"last_successful_check_at_ms"`
	PartitionCount          int                 `json:"partition_count"`
	TotalLagRecords         int64               `json:"total_lag_records"`
	MaxPartitionLagRecords  int64               `json:"max_partition_lag_records"`
	OldestAgeMs             int64               `json:"oldest_age_ms"`
	RetentionHeadroomMs     int64               `json:"retention_headroom_ms"`
	SnapshotAgeMs           int64               `json:"snapshot_age_ms"`
	ConsecutiveHealthyPolls int                 `json:"consecutive_healthy_polls"`
	Partitions              []PartitionSnapshot `json:"partitions"`
}

type RuntimeSource interface {
	Bounds(context.Context) (map[int32]PartitionBounds, error)
	OldestRecords(context.Context, map[int32]int64) (map[int32]OldestRecord, error)
}

type OffsetSource interface {
	Offsets(context.Context) (map[int32]ProjectionOffset, error)
}

func closedError(reason string) error {
	return fmt.Errorf("%w: %s", ErrClosed, reason)
}
