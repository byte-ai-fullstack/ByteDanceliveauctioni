package auction

import (
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/protobuf/proto"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/pkg/apperr"
	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"
)

func NewLotFromRequest(id string, req *v1.CreateLotRequest) (*v1.Lot, error) {
	return NewLotDraftFromRequest(id, req, true)
}

func NewLotDraftFromRequest(id string, req *v1.CreateLotRequest, requireComplete bool) (*v1.Lot, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: create lot request is required", apperr.ErrInvalidArgument)
	}
	if requireComplete && req.GetRoomId() == "" {
		return nil, fmt.Errorf("%w: room id is required", apperr.ErrInvalidArgument)
	}
	if requireComplete {
		if err := ValidateLotReady(req.GetTitle(), req.GetImageUrl(), req.GetRule()); err != nil {
			return nil, err
		}
	}

	rule := cloneRule(req.GetRule())
	if rule == nil {
		rule = &v1.BidRule{}
	}
	currentPrice := rule.GetStartPrice()
	if currentPrice == nil {
		currentPrice = &v1.Money{}
	}

	trustCards := cloneTrustCards(req.GetTrustCards())
	stock, err := normalizeStock(req.GetStock())
	if err != nil {
		return nil, err
	}
	lot := &v1.Lot{
		Id:               id,
		RoomId:           req.GetRoomId(),
		Title:            req.GetTitle(),
		Description:      req.GetDescription(),
		ImageUrl:         req.GetImageUrl(),
		Status:           v1.LotStatus_LOT_STATUS_DRAFT,
		QueueStatus:      v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE,
		Rule:             rule,
		CurrentPrice:     currentPrice,
		FinalPrice:       &v1.Money{Currency: currentPrice.GetCurrency()},
		Version:          1,
		ConfigVersion:    1,
		Stats:            &v1.LotStats{},
		TrustCards:       trustCards,
		DuelState:        &v1.DuelState{},
		PlaybookStage:    v1.PlaybookStage_PLAYBOOK_STAGE_WARM_UP,
		GalleryImageUrls: cloneStringSlice(req.GetGalleryImageUrls()),
		Category:         req.GetCategory(),
		Tags:             cloneStringSlice(req.GetTags()),
		EstimatePrice:    cloneMoney(req.GetEstimatePrice()),
		Stock:            stock,
		AfterSaleNotes:   req.GetAfterSaleNotes(),
		DepositAmount:    cloneMoney(req.GetDepositAmount()),
	}
	if err := validateOptionalMoney("depositAmount", lot.GetDepositAmount()); err != nil {
		return nil, err
	}
	normalizeTrustCards(lot)
	if requireComplete {
		if err := validateLotMedia(lot); err != nil {
			return nil, err
		}
	}
	return lot, nil
}

func ValidateLotReady(title, imageURL string, rule *v1.BidRule) error {
	if title == "" {
		return fmt.Errorf("%w: lot title is required", apperr.ErrInvalidArgument)
	}
	if imageURL == "" {
		return fmt.Errorf("%w: lot image url is required", apperr.ErrInvalidArgument)
	}
	if err := validateHTTPImageURL("imageUrl", imageURL); err != nil {
		return err
	}
	if rule == nil || rule.GetStartPrice() == nil || rule.GetMinIncrement() == nil {
		return fmt.Errorf("%w: bid rule, start price and min increment are required", apperr.ErrInvalidArgument)
	}
	if rule.GetStartPrice().GetCurrency() == "" || rule.GetMinIncrement().GetCurrency() == "" {
		return fmt.Errorf("%w: start price and min increment currency are required", apperr.ErrInvalidArgument)
	}
	if rule.GetStartPrice().GetCurrency() != rule.GetMinIncrement().GetCurrency() {
		return fmt.Errorf("%w: start price and min increment currency must match", apperr.ErrInvalidArgument)
	}
	if rule.GetStartPrice().GetAmount() < 0 {
		return fmt.Errorf("%w: start price amount must be >= 0", apperr.ErrInvalidArgument)
	}
	if rule.GetMinIncrement().GetAmount() <= 0 {
		return fmt.Errorf("%w: min increment amount must be > 0", apperr.ErrInvalidArgument)
	}
	if rule.GetDurationSeconds() < 60 {
		return fmt.Errorf("%w: duration seconds must be >= 60", apperr.ErrInvalidArgument)
	}
	if rule.GetAntiSnipeWindowSeconds() <= 0 {
		return fmt.Errorf("%w: anti-snipe window seconds must be > 0", apperr.ErrInvalidArgument)
	}
	if rule.GetAntiSnipeExtendSeconds() < 10 || rule.GetAntiSnipeExtendSeconds() > 30 {
		return fmt.Errorf("%w: anti-snipe extend seconds must be between 10 and 30", apperr.ErrInvalidArgument)
	}
	if rule.GetMaxExtendCount() <= 0 {
		return fmt.Errorf("%w: max extend count must be > 0", apperr.ErrInvalidArgument)
	}
	if capPrice := rule.GetCapPrice(); capPrice != nil {
		if capPrice.GetCurrency() == "" {
			return fmt.Errorf("%w: cap price currency is required", apperr.ErrInvalidArgument)
		}
		if capPrice.GetCurrency() != rule.GetStartPrice().GetCurrency() || capPrice.GetCurrency() != rule.GetMinIncrement().GetCurrency() {
			return fmt.Errorf("%w: cap price currency must match start price and min increment currency", apperr.ErrInvalidArgument)
		}
		if capPrice.GetAmount() <= rule.GetStartPrice().GetAmount() {
			return fmt.Errorf("%w: cap price amount must be greater than start price amount", apperr.ErrInvalidArgument)
		}
	}
	return nil
}

func ApplyDraftPatch(lot *v1.Lot, req *v1.PatchLotDraftRequest) error {
	if lot == nil {
		return fmt.Errorf("%w: lot is required", apperr.ErrInvalidArgument)
	}
	if req == nil {
		return fmt.Errorf("%w: patch lot draft request is required", apperr.ErrInvalidArgument)
	}
	if !IsPreStartCancellableStatus(lot.GetStatus()) {
		return fmt.Errorf("%w: only not-started lot can be edited, current status: %s", apperr.ErrInvalidArgument, lot.Status)
	}
	if lot.QueueStatus != v1.LotQueueStatus_LOT_QUEUE_STATUS_UNSPECIFIED && lot.QueueStatus != v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE {
		return fmt.Errorf("%w: queued lot must be removed from the queue before editing", apperr.ErrInvalidArgument)
	}
	candidate := proto.Clone(lot).(*v1.Lot)
	if req.GetRoomId() != "" {
		candidate.RoomId = req.GetRoomId()
	}
	if req.GetTitle() != "" {
		candidate.Title = req.GetTitle()
	}
	if req.GetDescription() != "" {
		candidate.Description = req.GetDescription()
	}
	if req.GetImageUrl() != "" {
		if err := validateHTTPImageURL("imageUrl", req.GetImageUrl()); err != nil {
			return err
		}
		candidate.ImageUrl = req.GetImageUrl()
	}
	if len(req.GetGalleryImageUrls()) > 0 {
		candidate.GalleryImageUrls = cloneStringSlice(req.GetGalleryImageUrls())
	}
	if req.GetCategory() != "" {
		candidate.Category = req.GetCategory()
	}
	if len(req.GetTags()) > 0 {
		candidate.Tags = cloneStringSlice(req.GetTags())
	}
	if req.GetEstimatePrice() != nil {
		candidate.EstimatePrice = cloneMoney(req.GetEstimatePrice())
	}
	if req.GetStock() != 0 {
		stock, err := normalizeStock(req.GetStock())
		if err != nil {
			return err
		}
		candidate.Stock = stock
	}
	if req.GetAfterSaleNotes() != "" {
		candidate.AfterSaleNotes = req.GetAfterSaleNotes()
	}
	if req.GetDepositAmount() != nil {
		if err := validateOptionalMoney("depositAmount", req.GetDepositAmount()); err != nil {
			return err
		}
		candidate.DepositAmount = cloneMoney(req.GetDepositAmount())
	}
	if req.GetRule() != nil {
		candidate.Rule = cloneRule(req.GetRule())
		if candidate.Rule.GetStartPrice() != nil {
			candidate.CurrentPrice = candidate.Rule.GetStartPrice()
			candidate.FinalPrice = &v1.Money{Currency: candidate.Rule.GetStartPrice().GetCurrency()}
		}
	}
	if len(req.GetTrustCards()) > 0 {
		candidate.TrustCards = cloneTrustCards(req.GetTrustCards())
		normalizeTrustCards(candidate)
	}
	if err := validateLotMedia(candidate); err != nil {
		return err
	}
	if candidate.QueueStatus == v1.LotQueueStatus_LOT_QUEUE_STATUS_UNSPECIFIED {
		candidate.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_NONE
	}
	if candidate.ConfigVersion <= 0 {
		candidate.ConfigVersion = 1
	}
	if candidate.ConfigVersion >= maxRedisExactInteger {
		return fmt.Errorf("%w: lot config version is exhausted", apperr.ErrInvalidArgument)
	}
	candidate.ConfigVersion++
	candidate.Version++
	proto.Reset(lot)
	proto.Merge(lot, candidate)
	return nil
}

func QueueLot(lot *v1.Lot, queuePosition int32) error {
	if lot == nil {
		return fmt.Errorf("%w: lot is required", apperr.ErrInvalidArgument)
	}
	if !IsPreStartCancellableStatus(lot.GetStatus()) {
		return fmt.Errorf("%w: only not-started lot can be queued, current status: %s", apperr.ErrInvalidArgument, lot.Status)
	}
	if err := ValidateLotReady(lot.GetTitle(), lot.GetImageUrl(), lot.GetRule()); err != nil {
		return err
	}
	if err := validateLotMedia(lot); err != nil {
		return err
	}
	if lot.QueueStatus == v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED && lot.QueuePosition > 0 {
		return nil
	}
	if queuePosition <= 0 {
		return fmt.Errorf("%w: queue position is required", apperr.ErrInvalidArgument)
	}
	lot.Status = v1.LotStatus_LOT_STATUS_QUEUED
	lot.QueueStatus = v1.LotQueueStatus_LOT_QUEUE_STATUS_QUEUED
	lot.QueuePosition = queuePosition
	lot.CurrentPrice = lot.GetRule().GetStartPrice()
	lot.FinalPrice = &v1.Money{Currency: lot.GetRule().GetStartPrice().GetCurrency()}
	lot.Version++
	return nil
}

func cloneRule(rule *v1.BidRule) *v1.BidRule {
	if rule == nil {
		return nil
	}
	return proto.Clone(rule).(*v1.BidRule)
}

func validateOptionalMoney(field string, money *v1.Money) error {
	if money == nil {
		return nil
	}
	if money.GetAmount() < 0 {
		return fmt.Errorf("%w: %s amount must be >= 0", apperr.ErrInvalidArgument, field)
	}
	if money.GetAmount() > 0 && strings.TrimSpace(money.GetCurrency()) == "" {
		return fmt.Errorf("%w: %s currency is required", apperr.ErrInvalidArgument, field)
	}
	return nil
}

func cloneMoney(money *v1.Money) *v1.Money {
	if money == nil {
		return nil
	}
	return proto.Clone(money).(*v1.Money)
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeStock(stock int32) (int32, error) {
	if stock < 0 {
		return 0, fmt.Errorf("%w: stock must be >= 1", apperr.ErrInvalidArgument)
	}
	if stock == 0 {
		return 1, nil
	}
	return stock, nil
}

func validateLotMedia(lot *v1.Lot) error {
	if lot == nil {
		return nil
	}
	if len(lot.GetGalleryImageUrls()) > 6 {
		return fmt.Errorf("%w: gallery image urls must be <= 6", apperr.ErrInvalidArgument)
	}
	for _, imageURL := range lot.GetGalleryImageUrls() {
		if err := validateHTTPImageURL("galleryImageUrls", imageURL); err != nil {
			return err
		}
	}
	for _, card := range lot.GetTrustCards() {
		if card != nil && card.GetImageUrl() != "" {
			if err := validateHTTPImageURL("trustCards.imageUrl", card.GetImageUrl()); err != nil {
				return err
			}
		}
	}
	if lot.GetStock() < 1 {
		return fmt.Errorf("%w: stock must be >= 1", apperr.ErrInvalidArgument)
	}
	return nil
}

func validateHTTPImageURL(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if len(value) > 1024 {
		return fmt.Errorf("%w: %s must be <= 1024 chars", apperr.ErrInvalidArgument, field)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%w: %s must be a valid http or https URL", apperr.ErrInvalidArgument, field)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s must be a valid http or https URL", apperr.ErrInvalidArgument, field)
	}
	return nil
}

func cloneTrustCards(cards []*v1.TrustRevealCard) []*v1.TrustRevealCard {
	out := make([]*v1.TrustRevealCard, 0, len(cards))
	for _, card := range cards {
		if card == nil {
			continue
		}
		out = append(out, proto.Clone(card).(*v1.TrustRevealCard))
	}
	return out
}

func normalizeTrustCards(lot *v1.Lot) {
	for _, card := range lot.TrustCards {
		if card.Id == "" {
			card.Id = idgen.New("card")
		}
		card.LotId = lot.Id
		card.Revealed = false
		card.RevealedAtUnixMs = 0
	}
}

func RevealTrustCard(lot *v1.Lot, cardID string, nowMs int64) (*v1.TrustRevealCard, error) {
	if lot == nil {
		return nil, fmt.Errorf("%w: lot is required", apperr.ErrInvalidArgument)
	}
	if cardID == "" {
		return nil, fmt.Errorf("%w: trust card id is required", apperr.ErrInvalidArgument)
	}
	if !IsAuctionOpenStatus(lot.GetStatus()) {
		return nil, fmt.Errorf("%w: trust card can only be revealed for a live lot", apperr.ErrInvalidArgument)
	}
	for _, card := range lot.TrustCards {
		if card.Id == cardID {
			if card.Revealed {
				return card, nil
			}
			card.Revealed = true
			card.RevealedAtUnixMs = nowMs
			lot.PlaybookStage = v1.PlaybookStage_PLAYBOOK_STAGE_TRUST_BLOCKED
			lot.PresentationVersion++
			return card, nil
		}
	}
	return nil, fmt.Errorf("%w: trust card not found", apperr.ErrInvalidArgument)
}

func StartDuel(lot *v1.Lot, ranking []*v1.RankingItem, nowMs int64, userAID, userBID string) error {
	if lot == nil {
		return fmt.Errorf("%w: lot is required", apperr.ErrInvalidArgument)
	}
	if !IsAuctionOpenStatus(lot.Status) {
		return fmt.Errorf("%w: only live lot can enter duel mode", apperr.ErrInvalidArgument)
	}
	if len(ranking) < 2 {
		return fmt.Errorf("%w: at least two bidders are required to enter duel mode", apperr.ErrInvalidArgument)
	}

	var userA, userB *v1.RankingItem
	for _, item := range ranking {
		if userAID != "" && item.UserId == userAID {
			userA = item
		}
		if userBID != "" && item.UserId == userBID {
			userB = item
		}
	}
	if userAID != "" && userA == nil {
		return fmt.Errorf("%w: duel user A is not in ranking", apperr.ErrInvalidArgument)
	}
	if userBID != "" && userB == nil {
		return fmt.Errorf("%w: duel user B is not in ranking", apperr.ErrInvalidArgument)
	}
	if userA != nil && userB != nil && userA.UserId == userB.UserId {
		return fmt.Errorf("%w: duel users must be different", apperr.ErrInvalidArgument)
	}
	for _, item := range ranking {
		if userA == nil && (userB == nil || item.UserId != userB.UserId) {
			userA = item
			continue
		}
		if userB == nil && item.UserId != userA.UserId {
			userB = item
			break
		}
	}
	if userA == nil || userB == nil || userA.UserId == userB.UserId {
		return fmt.Errorf("%w: at least two distinct bidders are required to enter duel mode", apperr.ErrInvalidArgument)
	}

	extendCount := int32(0)
	if lot.DuelState != nil {
		extendCount = lot.DuelState.ExtendCount
	}
	lot.DuelState = &v1.DuelState{
		Active:          true,
		LotId:           lot.Id,
		UserAId:         userA.UserId,
		UserANickname:   userA.Nickname,
		UserBId:         userB.UserId,
		UserBNickname:   userB.Nickname,
		StartedAtUnixMs: nowMs,
		EndsAtUnixMs:    lot.EndsAtUnixMs,
		ExtendCount:     extendCount,
		MaxExtendCount:  lot.GetRule().GetMaxExtendCount(),
	}
	lot.PlaybookStage = v1.PlaybookStage_PLAYBOOK_STAGE_DUEL_MODE
	lot.PresentationVersion++
	return nil
}

func IsPreStartCancellableStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_DRAFT, v1.LotStatus_LOT_STATUS_READY, v1.LotStatus_LOT_STATUS_QUEUED:
		return true
	default:
		return false
	}
}
