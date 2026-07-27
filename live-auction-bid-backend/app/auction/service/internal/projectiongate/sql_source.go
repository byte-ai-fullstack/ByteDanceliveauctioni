package projectiongate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

type SQLSource struct {
	db *sql.DB
}

func NewSQLSource(db *sql.DB) (*SQLSource, error) {
	if db == nil {
		return nil, errors.New("projection gate database is required")
	}
	return &SQLSource{db: db}, nil
}

func (source *SQLSource) Offsets(ctx context.Context) (map[int32]ProjectionOffset, error) {
	if source == nil || source.db == nil {
		return nil, errors.New("projection gate database is required")
	}
	rows, err := source.db.QueryContext(ctx, `
SELECT kafka_partition, next_offset, updated_at_ms
FROM auction_projection_partition_offsets
WHERE topic = ?
ORDER BY kafka_partition`, eventcontract.RuntimeProjectionTopicV1)
	if err != nil {
		return nil, fmt.Errorf("query projection gate offsets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int32]ProjectionOffset)
	for rows.Next() {
		var partition int32
		var offset ProjectionOffset
		if err := rows.Scan(&partition, &offset.NextOffset, &offset.UpdatedAtMs); err != nil {
			return nil, fmt.Errorf("scan projection gate offset: %w", err)
		}
		if _, duplicate := result[partition]; duplicate {
			return nil, fmt.Errorf("projection gate offset duplicated partition %d", partition)
		}
		result[partition] = offset
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projection gate offsets: %w", err)
	}
	return result, nil
}
