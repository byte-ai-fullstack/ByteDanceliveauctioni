package auction

import (
	"errors"

	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

// OverlayLotPresentation merges independently persisted presentation fields
// onto a lot without changing Redis-authoritative adjudication fields.
func OverlayLotPresentation(lot, presentation *v1.Lot) error {
	if lot == nil || presentation == nil {
		return errors.New("lot and presentation are required")
	}
	if presentation.GetId() != lot.GetId() || presentation.GetMainAccountId() != lot.GetMainAccountId() {
		return errors.New("lot presentation identity mismatch")
	}
	if presentation.GetPresentationVersion() <= 0 {
		return errors.New("lot presentation version must be positive")
	}

	runtimeDuel := lot.GetDuelState()
	lot.TrustCards = cloneTrustCards(presentation.GetTrustCards())
	lot.PresentationVersion = presentation.GetPresentationVersion()
	if presentation.GetDuelState() != nil {
		lot.DuelState = proto.Clone(presentation.GetDuelState()).(*v1.DuelState)
	} else {
		lot.DuelState = &v1.DuelState{LotId: lot.GetId()}
	}
	if runtimeDuel != nil {
		lot.DuelState.EndsAtUnixMs = runtimeDuel.GetEndsAtUnixMs()
		lot.DuelState.ExtendCount = runtimeDuel.GetExtendCount()
		lot.DuelState.MaxExtendCount = runtimeDuel.GetMaxExtendCount()
	}
	lot.DuelState.LotId = lot.GetId()
	if IsAuctionOpenStatus(lot.GetStatus()) {
		if presentation.GetPlaybookStage() != v1.PlaybookStage_PLAYBOOK_STAGE_UNSPECIFIED {
			lot.PlaybookStage = presentation.GetPlaybookStage()
		}
	} else {
		lot.DuelState.Active = false
	}
	return nil
}

// MergeRuntimeDuelState overlays only Redis-owned timing and extension fields,
// preserving presentation-owned participant selection and start state.
func MergeRuntimeDuelState(current *v1.DuelState, lotID string, endsAtUnixMs int64, extendCount, maxExtendCount int32, terminal bool) *v1.DuelState {
	duel := &v1.DuelState{}
	if current != nil {
		duel = proto.Clone(current).(*v1.DuelState)
	}
	duel.LotId = lotID
	duel.EndsAtUnixMs = endsAtUnixMs
	duel.ExtendCount = extendCount
	duel.MaxExtendCount = maxExtendCount
	if terminal {
		duel.Active = false
	}
	return duel
}
