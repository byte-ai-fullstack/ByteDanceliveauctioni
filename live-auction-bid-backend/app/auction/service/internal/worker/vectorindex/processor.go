package vectorindex

import (
	"context"
	"errors"
	"fmt"

	"live-auction-bid/backend/app/auction/service/internal/observability"
	"live-auction-bid/backend/app/auction/service/internal/searchindex"
)

var ErrEmbeddingFailure = errors.New("pgvector embedding request failed")

type documentIndex interface {
	Provider() string
	Model() string
	ModelVersion() string
	Dimensions() int
	DocumentState(ctx context.Context, lotID string) (searchindex.VectorDocumentState, error)
	ApplyDocument(ctx context.Context, document searchindex.LotDocument, embedding []float64) (searchindex.VectorApplyResult, error)
}

type embeddingClient interface {
	Configured() bool
	Provider() string
	Model() string
	ModelVersion() string
	Dimensions() int
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}

type Processor struct {
	index    documentIndex
	embedder embeddingClient
}

func NewProcessor(index documentIndex, embedder embeddingClient) (*Processor, error) {
	if index == nil || embedder == nil || !embedder.Configured() {
		return nil, errors.New("pgvector document index and configured embedder are required")
	}
	if index.Provider() != embedder.Provider() || index.Model() != embedder.Model() ||
		index.ModelVersion() != embedder.ModelVersion() || index.Dimensions() != embedder.Dimensions() {
		return nil, errors.New("pgvector index and embedding identities differ")
	}
	return &Processor{index: index, embedder: embedder}, nil
}

func (processor *Processor) Apply(ctx context.Context, record Record) (searchindex.VectorApplyResult, error) {
	if processor == nil || processor.index == nil || processor.embedder == nil || record.Event == nil || record.Document.LotID == "" {
		return searchindex.VectorApplyResult{}, fmt.Errorf("%w: processor or record is invalid", ErrInvalidRecord)
	}
	document := record.Document
	document.EmbeddingProvider = processor.embedder.Provider()
	document.EmbeddingModel = processor.embedder.Model()
	document.EmbeddingModelVersion = processor.embedder.ModelVersion()
	document.EmbeddingDimensions = processor.embedder.Dimensions()
	document.EmbeddingHash = document.StableEmbeddingHash(
		document.EmbeddingProvider, document.EmbeddingModel, document.EmbeddingModelVersion, document.EmbeddingDimensions,
	)
	state, err := processor.index.DocumentState(ctx, document.LotID)
	if err != nil {
		return searchindex.VectorApplyResult{}, err
	}
	if state.Found {
		identityConflict := document.LotVersion == state.LotVersion &&
			(document.LastEventID != state.LastEventID || document.ContentHash != state.ContentHash)
		if document.LotVersion < state.LotVersion || identityConflict ||
			(state.HasEmbedding && state.EmbeddingHash == document.EmbeddingHash) {
			return processor.index.ApplyDocument(ctx, document, nil)
		}
	}
	if !document.PublicVisible || document.SearchText == "" {
		return processor.index.ApplyDocument(ctx, document, nil)
	}
	embeddings, err := processor.embedder.Embed(ctx, []string{document.SearchText})
	if err != nil {
		observability.RecordEmbeddingRequest(processor.embedder.Model(), "failed")
		return searchindex.VectorApplyResult{}, fmt.Errorf("%w: %v", ErrEmbeddingFailure, err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != processor.embedder.Dimensions() {
		observability.RecordEmbeddingRequest(processor.embedder.Model(), "invalid")
		return searchindex.VectorApplyResult{}, fmt.Errorf("%w: provider returned an invalid vector shape", ErrEmbeddingFailure)
	}
	observability.RecordEmbeddingRequest(processor.embedder.Model(), "success")
	return processor.index.ApplyDocument(ctx, document, embeddings[0])
}
