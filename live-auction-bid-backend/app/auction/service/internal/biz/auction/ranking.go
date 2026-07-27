package auction

import (
	"os"
	"sort"
	"strconv"
	"strings"

	v1 "live-auction-bid/backend/api/auction/service/v1"
	userbiz "live-auction-bid/backend/app/auction/service/internal/biz/user"
)

const DefaultRealtimeRankingLimit int64 = 50

func RealtimeRankingLimit() int64 {
	raw := strings.TrimSpace(os.Getenv("AUCTION_REALTIME_RANKING_LIMIT"))
	if raw == "" {
		return DefaultRealtimeRankingLimit
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return DefaultRealtimeRankingLimit
	}
	return limit
}

func LimitRanking(ranking []*v1.RankingItem, limit int64) []*v1.RankingItem {
	if limit <= 0 || int64(len(ranking)) <= limit {
		return ranking
	}
	return ranking[:limit]
}

func BuildRealtimeRanking(bids []*v1.Bid) []*v1.RankingItem {
	return LimitRanking(BuildRanking(bids), RealtimeRankingLimit())
}

func BuildRanking(bids []*v1.Bid) []*v1.RankingItem {
	bestByUser := make(map[string]*v1.RankingItem)
	for _, bid := range bids {
		if bid == nil {
			continue
		}
		current, ok := bestByUser[bid.UserId]
		if !ok || bid.GetAmount().GetAmount() > current.GetAmount().GetAmount() ||
			bid.GetAmount().GetAmount() == current.GetAmount().GetAmount() && bid.CreatedAtUnixMs < current.BidAtUnixMs {
			bestByUser[bid.UserId] = &v1.RankingItem{
				UserId:      bid.UserId,
				Nickname:    bid.Nickname,
				AvatarUrl:   avatarURLForBid(bid),
				Amount:      bid.GetAmount(),
				BidAtUnixMs: bid.CreatedAtUnixMs,
			}
		}
	}

	items := make([]*v1.RankingItem, 0, len(bestByUser))
	for _, item := range bestByUser {
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].GetAmount().GetAmount() == items[j].GetAmount().GetAmount() {
			return items[i].BidAtUnixMs < items[j].BidAtUnixMs
		}
		return items[i].GetAmount().GetAmount() > items[j].GetAmount().GetAmount()
	})

	for i := range items {
		items[i].Rank = int32(i + 1)
	}
	return items
}

func avatarURLForBid(bid *v1.Bid) string {
	if bid == nil {
		return ""
	}
	if trimmed := strings.TrimSpace(bid.GetAvatarUrl()); trimmed != "" {
		return trimmed
	}
	return userbiz.AvatarURLForUserID(bid.GetUserId())
}
