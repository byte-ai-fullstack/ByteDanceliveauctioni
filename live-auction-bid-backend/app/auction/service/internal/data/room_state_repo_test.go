package data

import (
	"errors"
	"testing"

	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
)

func TestMapQueuePositionConflict(t *testing.T) {
	err := mapQueuePositionConflict(errors.New("Error 1062: Duplicate entry 'room#1' for key 'uidx_one_queued_position_per_room'"))
	if !apperr.IsQueuePositionConflict(err) {
		t.Fatalf("expected queue position conflict, got %v", err)
	}
}
