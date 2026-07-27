package service

import (
	"context"
	"errors"
	"log/slog"
	"math"
	mathrand "math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/aiassistant"
	"live-auction-bid/backend/app/auction/service/internal/biz/auction"
	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

var buyerBudgetPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:元|块)?`)

type buyerQueryIntent struct {
	Query        string
	Budget       int64
	StatusIntent string
	RoomName     string
	Terms        []string
	Gift         bool
	LowPrice     bool
}

func (s *AuctionService) SetAIAssistant(assistant *aiassistant.Assistant) *AuctionService {
	if s == nil {
		return nil
	}
	s.ai = assistant
	return s
}

func (s *AuctionService) SetBuyerSearch(keywords buyerKeywordSearch, vectors buyerVectorSearch, embedder buyerEmbeddingClient) *AuctionService {
	if s == nil {
		return nil
	}
	s.buyerKeywords = keywords
	s.buyerVectors = vectors
	s.buyerEmbedder = embedder
	return s
}

func (s *AuctionService) SetBuyerSearchTimeout(timeout time.Duration) *AuctionService {
	if s == nil {
		return nil
	}
	if timeout > 0 {
		s.buyerSearchTimeout = timeout
	}
	return s
}

func (s *AuctionService) searchTimeout() time.Duration {
	if s != nil && s.buyerSearchTimeout > 0 {
		return s.buyerSearchTimeout
	}
	return 5 * time.Second
}

func (s *AuctionService) ConsultBuyer(ctx context.Context, req *v1.BuyerConsultRequest) (*v1.BuyerConsultReply, error) {
	aiReq := aiassistant.BuyerConsultRequest{
		Query:          req.GetQuery(),
		RoomID:         req.GetRoomId(),
		LotID:          req.GetLotId(),
		Budget:         req.GetBudget(),
		RiskPreference: req.GetRiskPreference(),
	}
	intent := parseBuyerQuery(aiReq.Query, aiReq.Budget)
	if aiReq.Budget <= 0 {
		aiReq.Budget = intent.Budget
	}
	assistant := s.aiAssistant()
	aiCtx := aiassistant.BuyerConsultContext{Candidates: s.buyerCandidates(ctx, aiReq, intent)}
	if strings.TrimSpace(aiReq.RoomID) != "" {
		if snapshot, err := s.auction.Snapshot(ctx, strings.TrimSpace(aiReq.RoomID)); err == nil {
			aiCtx.Snapshot = auction.SnapshotForViewer(snapshot, lotResultViewerFromContext(ctx))
		}
	}
	reply, err := assistant.ConsultBuyer(ctx, aiReq, aiCtx)
	if err != nil {
		return &v1.BuyerConsultReply{Result: ErrorResult(ctx, err)}, nil
	}
	return buyerConsultReplyToProto(ctx, reply), nil
}

func (s *AuctionService) BuyerSuggestions(ctx context.Context, limit int) (aiassistant.BuyerSuggestionReply, error) {
	limit = normalizeBuyerSuggestionLimit(limit)
	assistant := s.aiAssistant()
	candidates := s.buyerSuggestionCandidates(ctx, limit*4)
	return assistant.SuggestBuyerPrompts(ctx, aiassistant.BuyerSuggestionRequest{Limit: limit}, aiassistant.BuyerSuggestionContext{Candidates: candidates})
}

func (s *AuctionService) aiAssistant() *aiassistant.Assistant {
	if s != nil && s.ai != nil {
		return s.ai
	}
	return aiassistant.New(aiassistant.Config{Provider: "mock"})
}

func (s *AuctionService) buyerCandidates(ctx context.Context, req aiassistant.BuyerConsultRequest, intent buyerQueryIntent) []aiassistant.LotCandidate {
	if s == nil || s.auction == nil {
		return nil
	}
	keywordConfigured := s.buyerKeywords != nil
	vectorConfigured := s.buyerVectors != nil && s.buyerEmbedder != nil && s.buyerEmbedder.Configured() && strings.TrimSpace(req.Query) != ""
	if !keywordConfigured && !vectorConfigured {
		observability.RecordSearchFallback("mysql_only")
		return s.keywordBuyerCandidates(ctx, req, intent)
	}

	var keywordDocuments, vectorDocuments []searchindex.LotDocument
	var keywordErr, vectorErr error
	var wait sync.WaitGroup
	if keywordConfigured {
		wait.Add(1)
		go func() {
			defer wait.Done()
			started := time.Now()
			searchCtx, cancel := context.WithTimeout(ctx, s.searchTimeout())
			defer cancel()
			keywordDocuments, keywordErr = s.buyerKeywords.SearchKeywords(searchCtx, searchindex.KeywordSearchQuery{
				Query: strings.TrimSpace(req.Query), RoomID: strings.TrimSpace(req.RoomID), LotID: strings.TrimSpace(req.LotID),
				Statuses: buyerSearchStatuses(intent), Limit: 20,
			})
			observability.RecordSearchRetrieval("elasticsearch", searchResultLabel(keywordErr), time.Since(started))
		}()
	}
	if vectorConfigured {
		wait.Add(1)
		go func() {
			defer wait.Done()
			started := time.Now()
			searchCtx, cancel := context.WithTimeout(ctx, s.searchTimeout())
			defer cancel()
			embeddings, err := s.buyerEmbedder.Embed(searchCtx, []string{strings.TrimSpace(req.Query)})
			if err != nil {
				vectorErr = err
			} else if len(embeddings) == 0 {
				vectorErr = errors.New("embedding provider returned no query vector")
			} else {
				vectorDocuments, vectorErr = s.buyerVectors.Search(searchCtx, searchindex.SearchQuery{
					Vector: embeddings[0], RoomID: strings.TrimSpace(req.RoomID), LotID: strings.TrimSpace(req.LotID), Limit: 20,
				})
			}
			observability.RecordSearchRetrieval("pgvector", searchResultLabel(vectorErr), time.Since(started))
		}()
	}
	wait.Wait()
	if keywordErr != nil {
		slog.Warn("buyer Elasticsearch retrieval failed", "error", keywordErr)
	}
	if vectorErr != nil {
		slog.Warn("buyer pgvector retrieval failed", "error", vectorErr)
	}
	if len(keywordDocuments) == 0 && len(vectorDocuments) == 0 {
		observability.RecordSearchFallback("mysql_after_empty_or_error")
		return s.keywordBuyerCandidates(ctx, req, intent)
	}
	ranked := reciprocalRankFusion([][]searchindex.LotDocument{keywordDocuments, vectorDocuments}, 60, 40)
	candidates, err := s.hydrateBuyerCandidates(ctx, ranked, req, intent, 8)
	if err != nil {
		slog.Warn("buyer search authoritative hydration failed", "error", err)
		observability.RecordSearchFallback("mysql_after_hydration_error")
		return s.keywordBuyerCandidates(ctx, req, intent)
	}
	if len(candidates) == 0 {
		observability.RecordSearchFallback("mysql_after_hydration_empty")
		return s.keywordBuyerCandidates(ctx, req, intent)
	}
	mode := "hybrid"
	if len(keywordDocuments) == 0 {
		mode = "vector_only"
	} else if len(vectorDocuments) == 0 {
		mode = "keyword_only"
	}
	observability.RecordSearchFusion(mode, len(ranked), len(candidates))
	return candidates
}

func (s *AuctionService) buyerSuggestionCandidates(ctx context.Context, limit int) []aiassistant.LotCandidate {
	if limit <= 0 {
		limit = 24
	}
	vector := s.randomVectorBuyerCandidates(ctx, limit)
	keyword := s.randomKeywordBuyerCandidates(ctx, limit)
	return mergeRandomBuyerCandidates(vector, keyword, limit)
}

func (s *AuctionService) randomVectorBuyerCandidates(ctx context.Context, limit int) []aiassistant.LotCandidate {
	if s == nil || s.buyerVectors == nil {
		return nil
	}
	docs, err := s.buyerVectors.RandomPublicDocuments(ctx, limit*2)
	if err != nil {
		slog.Warn("buyer suggestion vector pool failed", "error", err)
		return nil
	}
	lotIDs := make([]string, 0, len(docs))
	for _, doc := range docs {
		if strings.TrimSpace(doc.LotID) != "" {
			lotIDs = append(lotIDs, doc.LotID)
		}
	}
	lotsByID, roomNames, err := s.loadAuthoritativeLots(ctx, lotIDs)
	if err != nil {
		slog.Warn("buyer suggestion authoritative hydration failed", "error", err)
		return nil
	}
	out := make([]aiassistant.LotCandidate, 0, len(docs))
	seen := map[string]bool{}
	for _, doc := range docs {
		if doc.LotID == "" || seen[doc.LotID] {
			continue
		}
		lot := lotsByID[doc.LotID]
		if lot == nil || !auction.IsPublicVisibleLotStatus(lot.GetStatus()) {
			continue
		}
		roomName, ok := roomNames[lot.GetRoomId()]
		if !ok {
			continue
		}
		seen[doc.LotID] = true
		_, reason := scoreLot(buyerQueryIntent{}, lot, roomName)
		out = append(out, aiassistant.LotCandidate{
			Type:         "lot",
			Title:        lot.GetTitle(),
			RoomID:       lot.GetRoomId(),
			LotID:        lot.GetId(),
			Status:       lot.GetStatus().String(),
			CurrentPrice: searchPriceMoney(lot),
			Href:         "/m/room/" + lot.GetRoomId(),
			Reason:       reason,
			ImageURL:     lot.GetImageUrl(),
			Score:        100 - len(out),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (s *AuctionService) randomKeywordBuyerCandidates(ctx context.Context, limit int) []aiassistant.LotCandidate {
	rooms, err := s.auction.ListRooms(ctx, auction.RoomQuery{PublicOnly: true, PublicVisibleOnly: true})
	if err != nil {
		return nil
	}
	out := make([]aiassistant.LotCandidate, 0, limit)
	seen := map[string]bool{}
	for _, room := range rooms {
		for _, status := range buyerCandidateStatuses(buyerQueryIntent{}) {
			lots, err := s.auction.ListLots(ctx, room.ID, status)
			if err != nil {
				continue
			}
			for _, lot := range lots {
				if lot == nil || seen[lot.GetId()] || !auction.IsPublicVisibleLotStatus(lot.GetStatus()) {
					continue
				}
				seen[lot.GetId()] = true
				score, reason := scoreLot(buyerQueryIntent{}, lot, room.Name)
				out = append(out, aiassistant.LotCandidate{
					Type:         "lot",
					Title:        lot.GetTitle(),
					RoomID:       lot.GetRoomId(),
					LotID:        lot.GetId(),
					Status:       lot.GetStatus().String(),
					CurrentPrice: searchPriceMoney(lot),
					Href:         "/m/room/" + lot.GetRoomId(),
					Reason:       reason,
					ImageURL:     lot.GetImageUrl(),
					Score:        score,
				})
			}
		}
	}
	shuffleBuyerCandidates(out)
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func (s *AuctionService) keywordBuyerCandidates(ctx context.Context, req aiassistant.BuyerConsultRequest, intent buyerQueryIntent) []aiassistant.LotCandidate {
	rooms, err := s.auction.ListRooms(ctx, auction.RoomQuery{PublicOnly: true, PublicVisibleOnly: true})
	if err != nil {
		return nil
	}
	roomNames := make(map[string]string, len(rooms))
	for _, room := range rooms {
		roomNames[room.ID] = room.Name
	}
	candidates := make([]aiassistant.LotCandidate, 0)
	seen := map[string]bool{}
	for _, room := range rooms {
		if req.RoomID != "" && room.ID != req.RoomID {
			continue
		}
		if intent.RoomName != "" && !strings.Contains(strings.ToLower(room.Name), strings.ToLower(intent.RoomName)) {
			continue
		}
		for _, status := range buyerCandidateStatuses(intent) {
			lots, err := s.auction.ListLots(ctx, room.ID, status)
			if err != nil {
				continue
			}
			for _, lot := range lots {
				if lot == nil || seen[lot.GetId()] || !auction.IsPublicVisibleLotStatus(lot.GetStatus()) {
					continue
				}
				if req.LotID != "" && lot.GetId() != req.LotID {
					continue
				}
				seen[lot.GetId()] = true
				score, reason := scoreLot(intent, lot, roomNames[room.ID])
				if strings.TrimSpace(req.Query) != "" && score <= 0 {
					continue
				}
				candidates = append(candidates, aiassistant.LotCandidate{
					Type:         "lot",
					Title:        lot.GetTitle(),
					RoomID:       lot.GetRoomId(),
					LotID:        lot.GetId(),
					Status:       lot.GetStatus().String(),
					CurrentPrice: searchPriceMoney(lot),
					Href:         "/m/room/" + lot.GetRoomId(),
					Reason:       reason,
					ImageURL:     lot.GetImageUrl(),
					Score:        score,
				})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].LotID < candidates[j].LotID
	})
	if len(candidates) > 8 {
		return candidates[:8]
	}
	return candidates
}

type fusedLotRank struct {
	LotID   string
	Score   float64
	Sources int
}

func reciprocalRankFusion(rankings [][]searchindex.LotDocument, rankConstant, limit int) []fusedLotRank {
	if rankConstant <= 0 {
		rankConstant = 60
	}
	if limit <= 0 {
		limit = 40
	}
	byLotID := make(map[string]*fusedLotRank)
	for _, ranking := range rankings {
		seen := make(map[string]struct{}, len(ranking))
		for rank, document := range ranking {
			lotID := strings.TrimSpace(document.LotID)
			if lotID == "" {
				continue
			}
			if _, duplicate := seen[lotID]; duplicate {
				continue
			}
			seen[lotID] = struct{}{}
			entry := byLotID[lotID]
			if entry == nil {
				entry = &fusedLotRank{LotID: lotID}
				byLotID[lotID] = entry
			}
			entry.Score += 1 / float64(rankConstant+rank+1)
			entry.Sources++
		}
	}
	out := make([]fusedLotRank, 0, len(byLotID))
	for _, entry := range byLotID {
		out = append(out, *entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].LotID < out[j].LotID
	})
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func (s *AuctionService) hydrateBuyerCandidates(
	ctx context.Context,
	ranked []fusedLotRank,
	req aiassistant.BuyerConsultRequest,
	intent buyerQueryIntent,
	limit int,
) ([]aiassistant.LotCandidate, error) {
	lotIDs := make([]string, 0, len(ranked))
	for _, candidate := range ranked {
		lotIDs = append(lotIDs, candidate.LotID)
	}
	lotsByID, roomNames, err := s.loadAuthoritativeLots(ctx, lotIDs)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 8
	}
	out := make([]aiassistant.LotCandidate, 0, limit)
	for _, rank := range ranked {
		lot := lotsByID[rank.LotID]
		if lot == nil || !auction.IsPublicVisibleLotStatus(lot.GetStatus()) {
			continue
		}
		roomName, publicRoom := roomNames[lot.GetRoomId()]
		if !publicRoom || (req.RoomID != "" && lot.GetRoomId() != req.RoomID) || (req.LotID != "" && lot.GetId() != req.LotID) ||
			!buyerStatusMatchesIntent(lot.GetStatus(), intent.StatusIntent) {
			continue
		}
		if intent.RoomName != "" && !strings.Contains(strings.ToLower(roomName), strings.ToLower(intent.RoomName)) {
			continue
		}
		matchScore, reason := scoreLot(intent, lot, roomName)
		if rank.Sources == 1 && reason == "公开可见 · 可进直播间查看" {
			reason = "检索匹配你的描述 · " + reason
		}
		out = append(out, aiassistant.LotCandidate{
			Type: "lot", Title: lot.GetTitle(), RoomID: lot.GetRoomId(), LotID: lot.GetId(), Status: lot.GetStatus().String(),
			CurrentPrice: searchPriceMoney(lot), Href: "/m/room/" + lot.GetRoomId(), Reason: reason,
			ImageURL: lot.GetImageUrl(), Score: int(math.Round(rank.Score*1_000_000)) + matchScore,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].LotID < out[j].LotID
	})
	if len(out) > limit {
		return out[:limit], nil
	}
	return out, nil
}

func (s *AuctionService) loadAuthoritativeLots(ctx context.Context, lotIDs []string) (map[string]*v1.Lot, map[string]string, error) {
	lots, err := s.auction.FindLotsByIDs(ctx, lotIDs)
	if err != nil {
		return nil, nil, err
	}
	roomNames, err := s.loadPublicVisibleRoomNames(ctx)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*v1.Lot, len(lots))
	for _, lot := range lots {
		if lot == nil || strings.TrimSpace(lot.GetId()) == "" {
			continue
		}
		byID[lot.GetId()] = lot
	}
	return byID, roomNames, nil
}

func buyerSearchStatuses(intent buyerQueryIntent) []string {
	statuses := buyerCandidateStatuses(intent)
	out := make([]string, 0, len(statuses))
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		value := status.String()
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func searchResultLabel(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func (s *AuctionService) loadPublicVisibleRoomNames(ctx context.Context) (map[string]string, error) {
	rooms, err := s.auction.ListRooms(ctx, auction.RoomQuery{PublicOnly: true, PublicVisibleOnly: true})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rooms))
	for _, room := range rooms {
		out[room.ID] = room.Name
	}
	return out, nil
}

func mergeRandomBuyerCandidates(primary, secondary []aiassistant.LotCandidate, limit int) []aiassistant.LotCandidate {
	if limit <= 0 {
		limit = 24
	}
	seen := map[string]bool{}
	out := make([]aiassistant.LotCandidate, 0, len(primary)+len(secondary))
	add := func(candidate aiassistant.LotCandidate) {
		if strings.TrimSpace(candidate.LotID) == "" || seen[candidate.LotID] {
			return
		}
		seen[candidate.LotID] = true
		out = append(out, candidate)
	}
	for _, candidate := range primary {
		add(candidate)
	}
	for _, candidate := range secondary {
		add(candidate)
	}
	shuffleBuyerCandidates(out)
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func shuffleBuyerCandidates(candidates []aiassistant.LotCandidate) {
	if len(candidates) <= 1 {
		return
	}
	rng := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
}

func normalizeBuyerSuggestionLimit(limit int) int {
	if limit <= 0 {
		return 6
	}
	if limit > 8 {
		return 8
	}
	return limit
}

func scoreLot(intent buyerQueryIntent, lot *v1.Lot, roomName string) (int, string) {
	title := strings.ToLower(lot.GetTitle())
	description := strings.ToLower(lot.GetDescription())
	category := strings.ToLower(lot.GetCategory())
	tagsText := strings.ToLower(strings.Join(lot.GetTags(), " "))
	roomText := strings.ToLower(roomName)
	score := 0
	reasons := make([]string, 0)
	addReason := func(reason string) {
		for _, item := range reasons {
			if item == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}
	for _, token := range intent.Terms {
		if token == "" {
			continue
		}
		switch {
		case strings.Contains(title, token):
			score += 12
			addReason("标题命中“" + token + "”")
		case strings.Contains(tagsText, token):
			score += 10
			addReason("标签命中“" + token + "”")
		case strings.Contains(category, token):
			score += 8
			addReason("品类命中“" + token + "”")
		case strings.Contains(roomText, token):
			score += 7
			addReason("直播间命中“" + token + "”")
		case strings.Contains(description, token):
			score += 3
			addReason("描述命中“" + token + "”")
		}
	}
	switch lot.GetStatus() {
	case v1.LotStatus_LOT_STATUS_LIVE:
		score += 12
		addReason("正在竞拍")
	case v1.LotStatus_LOT_STATUS_EXTENDED:
		score += 12
		addReason("加时中")
	case v1.LotStatus_LOT_STATUS_QUEUED:
		score += 6
		addReason("即将开拍")
	}
	if intent.StatusIntent != "" && buyerStatusMatchesIntent(lot.GetStatus(), intent.StatusIntent) {
		score += 12
	}
	current := currentSearchPrice(lot)
	if intent.Budget > 0 && current > 0 {
		switch {
		case current <= intent.Budget:
			score += 18
			addReason("预算内")
		case current <= intent.Budget+intent.Budget/4:
			score += 6
			addReason("接近预算")
		default:
			score -= 8
		}
	}
	if intent.LowPrice && current > 0 && current <= 50000 {
		score += 8
		addReason("低价开拍")
	}
	if intent.Gift && textContainsAny(title+" "+description+" "+category+" "+tagsText, []string{"送礼", "礼物", "礼盒", "证书", "首饰", "手镯", "吊坠", "纪念", "寓意", "收藏"}) {
		score += 8
		addReason("适合送礼")
	}
	if intent.RoomName != "" && strings.Contains(roomText, strings.ToLower(intent.RoomName)) {
		score += 16
		addReason("直播间命中")
	}
	if len(reasons) == 0 {
		return score, "公开可见 · 可进直播间查看"
	}
	return score, strings.Join(reasons, " · ")
}

func currentSearchPrice(lot *v1.Lot) int64 {
	if lot == nil {
		return 0
	}
	if price := lot.GetCurrentPrice(); price != nil && price.GetAmount() > 0 {
		return price.GetAmount()
	}
	if rule := lot.GetRule(); rule != nil && rule.GetStartPrice() != nil {
		return rule.GetStartPrice().GetAmount()
	}
	return 0
}

func searchPriceMoney(lot *v1.Lot) *v1.Money {
	if lot == nil {
		return nil
	}
	if price := lot.GetCurrentPrice(); price != nil && price.GetAmount() > 0 {
		return &v1.Money{Amount: price.GetAmount(), Currency: price.GetCurrency()}
	}
	if rule := lot.GetRule(); rule != nil && rule.GetStartPrice() != nil {
		price := rule.GetStartPrice()
		return &v1.Money{Amount: price.GetAmount(), Currency: price.GetCurrency()}
	}
	return nil
}

func buyerConsultReplyToProto(ctx context.Context, reply aiassistant.BuyerConsultReply) *v1.BuyerConsultReply {
	out := &v1.BuyerConsultReply{
		Answer:       reply.Answer,
		Intent:       reply.Intent,
		FallbackUsed: reply.FallbackUsed,
		Result:       okResult(ctx),
	}
	for _, result := range reply.Results {
		out.Results = append(out.Results, &v1.BuyerConsultResult{
			Type:         result.Type,
			Title:        result.Title,
			RoomId:       result.RoomID,
			LotId:        result.LotID,
			Status:       result.Status,
			CurrentPrice: result.CurrentPrice,
			Href:         result.Href,
			Reason:       result.Reason,
			ImageUrl:     result.ImageURL,
		})
	}
	for _, source := range reply.Sources {
		out.Sources = append(out.Sources, &v1.BuyerConsultSource{
			Type:   source.Type,
			Title:  source.Title,
			RoomId: source.RoomID,
			LotId:  source.LotID,
		})
	}
	return out
}

func queryTokens(query string) []string {
	query = strings.NewReplacer(
		"，", " ", "。", " ", ",", " ", ".", " ",
		"的", " ", "想", " ", "看", " ", "找", " ", "拍品", " ",
		"预算", " ", "以内", " ", "以下", " ", "不超过", " ", "正在", " ",
		"竞拍", " ", "直播中", " ", "即将", " ", "开拍", " ", "直播间", " ",
	).Replace(query)
	fields := strings.Fields(query)
	out := make([]string, 0, len(fields)+4)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || len([]rune(field)) <= 1 {
			continue
		}
		if _, err := strconv.ParseFloat(field, 64); err == nil {
			continue
		}
		out = append(out, field)
	}
	for _, keyword := range []string{"翡翠", "手镯", "吊坠", "珠宝", "玉", "玉石", "和田玉", "奢侈品", "收藏", "送礼", "礼物"} {
		if strings.Contains(query, strings.ToLower(keyword)) {
			out = append(out, strings.ToLower(keyword))
		}
	}
	return out
}

func parseBuyerQuery(query string, explicitBudget int64) buyerQueryIntent {
	normalized := strings.ToLower(strings.TrimSpace(query))
	intent := buyerQueryIntent{
		Query:    query,
		Budget:   explicitBudget,
		RoomName: parseRoomName(query),
		Terms:    queryTokens(normalized),
		Gift:     textContainsAny(normalized, []string{"送礼", "礼物", "生日", "纪念", "收藏品"}),
		LowPrice: textContainsAny(normalized, []string{"低价", "捡漏", "小预算", "便宜", "新人", "入门"}),
	}
	if intent.Budget <= 0 {
		intent.Budget = parseBudgetFromQuery(normalized)
	}
	if textContainsAny(normalized, []string{"正在竞拍", "竞拍中", "直播中", "正在拍", "加时"}) {
		intent.StatusIntent = "live"
	} else if textContainsAny(normalized, []string{"即将开拍", "待开拍", "马上开拍", "未开拍"}) {
		intent.StatusIntent = "queued"
	}
	return intent
}

func parseBudgetFromQuery(query string) int64 {
	if !textContainsAny(query, []string{"预算", "以内", "以下", "不超过", "低于", "小于", "元", "块", "¥", "￥"}) {
		return 0
	}
	match := buyerBudgetPattern.FindStringSubmatch(query)
	if len(match) < 2 {
		return 0
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(math.Round(value * 100))
}

func parseRoomName(query string) string {
	index := strings.Index(query, "直播间")
	if index <= 0 {
		return ""
	}
	prefix := strings.TrimSpace(query[:index])
	prefix = strings.NewReplacer("想看", " ", "看看", " ", "找", " ", "在", " ", "的", " ").Replace(prefix)
	fields := strings.Fields(prefix)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], "：:，,。.!！?？")
}

func buyerCandidateStatuses(intent buyerQueryIntent) []v1.LotStatus {
	switch intent.StatusIntent {
	case "live":
		return []v1.LotStatus{v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED}
	case "queued":
		return []v1.LotStatus{v1.LotStatus_LOT_STATUS_QUEUED}
	default:
		return []v1.LotStatus{
			v1.LotStatus_LOT_STATUS_LIVE,
			v1.LotStatus_LOT_STATUS_EXTENDED,
			v1.LotStatus_LOT_STATUS_QUEUED,
		}
	}
}

func buyerStatusMatchesIntent(status v1.LotStatus, intent string) bool {
	switch intent {
	case "":
		return true
	case "live":
		return status == v1.LotStatus_LOT_STATUS_LIVE || status == v1.LotStatus_LOT_STATUS_EXTENDED
	case "queued":
		return status == v1.LotStatus_LOT_STATUS_QUEUED
	default:
		return true
	}
}

func textContainsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
