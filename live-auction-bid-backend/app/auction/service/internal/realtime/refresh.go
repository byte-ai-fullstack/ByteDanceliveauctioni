package realtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/pkg/clock"
	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"
)

// RunSnapshotRefresh periodically compares every locally viewed room with the
// authoritative Redis-backed snapshot. It is owned and cancelled by main.
func (h *Hub) RunSnapshotRefresh(ctx context.Context, cfg SnapshotRefreshConfig) error {
	normalized, err := NormalizeSnapshotRefreshConfig(cfg)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(normalized.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if h.IsDraining() {
				return nil
			}
			if err := h.RefreshSnapshots(ctx, normalized); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("refresh realtime room snapshots failed", "error", err)
			}
		}
	}
}

// RefreshSnapshots executes one bounded refresh pass. Individual room failures
// do not block other rooms; the first error is returned for logging and tests.
func (h *Hub) RefreshSnapshots(ctx context.Context, cfg SnapshotRefreshConfig) error {
	normalized, err := NormalizeSnapshotRefreshConfig(cfg)
	if err != nil {
		return err
	}
	if h.snapshot == nil || h.IsDraining() {
		return nil
	}
	roomIDs := h.activeRoomIDs()
	if len(roomIDs) == 0 {
		return nil
	}
	workerCount := normalized.Concurrency
	if workerCount > len(roomIDs) {
		workerCount = len(roomIDs)
	}

	jobs := make(chan string)
	var workers sync.WaitGroup
	var errorMu sync.Mutex
	var firstErr error
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for roomID := range jobs {
				requestCtx, cancel := context.WithTimeout(ctx, normalized.RequestTimeout)
				refreshed, err := h.refreshRoomSnapshot(requestCtx, roomID)
				cancel()
				result := "unchanged"
				if refreshed {
					result = "refreshed"
				}
				if err != nil {
					result = "error"
					errorMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errorMu.Unlock()
				}
				observability.RecordWSSnapshotRefresh(result)
			}
		}()
	}
	for _, roomID := range roomIDs {
		select {
		case jobs <- roomID:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return firstErr
}

func (h *Hub) refreshRoomSnapshot(ctx context.Context, roomID string) (bool, error) {
	snapshot, err := h.snapshot.Snapshot(ctx, roomID)
	if err != nil {
		return false, err
	}
	if snapshot == nil || strings.TrimSpace(snapshot.GetRoomId()) != roomID {
		return false, errors.New("authoritative realtime snapshot room identity mismatch")
	}
	needsRefresh, err := h.snapshotNeedsRefresh(snapshot)
	if err != nil || !needsRefresh {
		return false, err
	}
	prepared, err := h.prepareRoomFrames(snapshot, v1.AuctionEventType_AUCTION_EVENT_TYPE_ROOM_SNAPSHOT, idgen.New("refresh"), clock.NowMs())
	if errors.Is(err, ErrSnapshotVersionRegressed) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	h.enqueuePreparedRoomFrames(h.roomConnections(roomID), prepared)
	return true, nil
}

func (h *Hub) snapshotNeedsRefresh(snapshot *v1.RoomSnapshot) (bool, error) {
	lot := snapshot.GetCurrentLot()
	lotID := ""
	lotVersion := int64(0)
	status := v1.LotStatus_LOT_STATUS_UNSPECIFIED
	if lot != nil {
		lotID = lot.GetId()
		lotVersion = lot.GetVersion()
		status = lot.GetStatus()
	}
	h.wireMu.Lock()
	defer h.wireMu.Unlock()
	cache := h.wireRooms[snapshot.GetRoomId()]
	if cache == nil {
		return true, nil
	}
	if cache.lotID != lotID {
		return true, nil
	}
	if lotVersion < cache.lotVersion {
		return false, nil
	}
	if lotVersion == cache.lotVersion {
		if status != cache.status {
			return false, errors.New("same realtime lot version has conflicting status")
		}
		return false, nil
	}
	return true, nil
}

func (h *Hub) activeRoomIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	roomIDs := make([]string, 0, len(h.rooms))
	for roomID, connections := range h.rooms {
		if len(connections) > 0 {
			roomIDs = append(roomIDs, roomID)
		}
	}
	return roomIDs
}
