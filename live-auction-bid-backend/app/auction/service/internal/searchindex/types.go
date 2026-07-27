package searchindex

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	v1 "live-auction-bid/backend/api/auction/service/v1"
)

const (
	DefaultEmbeddingModel      = "text-embedding-v4"
	DefaultEmbeddingDimensions = 1024
	DefaultEmbeddingBatchSize  = 10
	DefaultSearchLimit         = 20
)

type LotDocument struct {
	LotID                 string
	RoomID                string
	MainAccountID         string
	Title                 string
	Description           string
	Category              string
	Tags                  []string
	ImageURL              string
	SearchText            string
	Status                string
	StartPrice            *v1.Money
	CurrentPrice          *v1.Money
	StartsAtUnixMs        int64
	EndsAtUnixMs          int64
	Href                  string
	PublicVisible         bool
	LotUpdatedAtUnixMs    int64
	LotVersion            int64
	LastEventID           string
	ContentHash           string
	EmbeddingProvider     string
	EmbeddingModel        string
	EmbeddingModelVersion string
	EmbeddingDimensions   int
	EmbeddingHash         string
}

type SearchQuery struct {
	Vector []float64
	RoomID string
	LotID  string
	Limit  int
}

type KeywordSearchQuery struct {
	Query    string
	RoomID   string
	LotID    string
	Statuses []string
	Limit    int
}

func (d LotDocument) StableEmbeddingHash(provider, model, modelVersion string, dimensions int) string {
	tags := append([]string(nil), d.Tags...)
	for index := range tags {
		tags[index] = strings.TrimSpace(tags[index])
	}
	sort.Strings(tags)
	parts := []string{
		strings.ToLower(strings.TrimSpace(provider)),
		strings.TrimSpace(model),
		strings.TrimSpace(modelVersion),
		strconv.Itoa(dimensions),
		strings.TrimSpace(d.LotID),
		strings.TrimSpace(d.MainAccountID),
		strings.TrimSpace(d.Title),
		strings.TrimSpace(d.Description),
		strings.TrimSpace(d.Category),
		strings.Join(tags, "\x1e"),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func LotDocumentFromDomainEvent(event *v1.LotStateDomainEventV1) LotDocument {
	if event == nil {
		return LotDocument{}
	}
	document := LotDocument{
		LotID: event.GetLotId(), RoomID: event.GetRoomId(), MainAccountID: event.GetMainAccountId(),
		Title: event.GetTitle(), Description: event.GetDescription(), Category: event.GetCategory(),
		Tags: append([]string(nil), event.GetTags()...), ImageURL: event.GetImageUrl(), Status: event.GetStatus().String(),
		StartPrice:     &v1.Money{Amount: event.GetStartPriceFen(), Currency: event.GetCurrency()},
		CurrentPrice:   &v1.Money{Amount: event.GetCurrentPriceFen(), Currency: event.GetCurrency()},
		StartsAtUnixMs: event.GetStartsAtUnixMs(), EndsAtUnixMs: event.GetEndsAtUnixMs(),
		Href: "/m/room/" + event.GetRoomId(), PublicVisible: publicSearchStatus(event.GetStatus()),
		LotVersion: event.GetLotVersion(), ContentHash: event.GetContentHash(),
	}
	if metadata := event.GetMetadata(); metadata != nil {
		document.LastEventID = metadata.GetCausationId()
	}
	document.SearchText = BuildStableSearchText(document)
	return document
}

func BuildStableSearchText(document LotDocument) string {
	parts := []string{strings.TrimSpace(document.Title), strings.TrimSpace(document.Description), strings.TrimSpace(document.Category)}
	parts = append(parts, document.Tags...)
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "\n")
}

func publicSearchStatus(status v1.LotStatus) bool {
	switch status {
	case v1.LotStatus_LOT_STATUS_QUEUED, v1.LotStatus_LOT_STATUS_LIVE, v1.LotStatus_LOT_STATUS_EXTENDED:
		return true
	default:
		return false
	}
}

func CloneMoney(money *v1.Money) *v1.Money {
	if money == nil {
		return nil
	}
	return &v1.Money{Amount: money.GetAmount(), Currency: money.GetCurrency()}
}

func VectorLiteral(vector []float64) string {
	if len(vector) == 0 {
		return ""
	}
	parts := make([]string, 0, len(vector))
	for _, value := range vector {
		parts = append(parts, strconv.FormatFloat(value, 'g', -1, 64))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func NormalizeDimensions(value int) int {
	if value <= 0 {
		return DefaultEmbeddingDimensions
	}
	return value
}

func NormalizeLimit(value int, fallback int) int {
	if value <= 0 {
		value = fallback
	}
	if value <= 0 {
		value = DefaultSearchLimit
	}
	if value > 100 {
		return 100
	}
	return value
}
