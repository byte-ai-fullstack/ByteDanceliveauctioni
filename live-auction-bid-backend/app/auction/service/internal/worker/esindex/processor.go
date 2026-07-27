package esindex

import (
	"context"
	"errors"
	"fmt"

	"live-auction-bid/backend/app/auction/service/internal/searchindex"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

type documentIndex interface {
	ApplyDocument(ctx context.Context, document searchindex.LotDocument) (searchindex.ElasticsearchApplyResult, error)
}

type Processor struct{ index documentIndex }

func NewProcessor(index documentIndex) (*Processor, error) {
	if index == nil {
		return nil, errors.New("elasticsearch document index is required")
	}
	return &Processor{index: index}, nil
}

func (processor *Processor) Apply(ctx context.Context, record searchstate.Record) (searchindex.ElasticsearchApplyResult, error) {
	if processor == nil || processor.index == nil || record.Event == nil || record.Document.LotID == "" {
		return searchindex.ElasticsearchApplyResult{}, fmt.Errorf("%w: processor or record is invalid", searchstate.ErrInvalidRecord)
	}
	return processor.index.ApplyDocument(ctx, record.Document)
}
