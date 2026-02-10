package handler

import (
	"context"

	"github.com/SaiNageswarS/go-api-boot/embed"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.uber.org/zap"
)

type ProfileCardSearchHandler struct {
	profileCardTextRepo      odm.OdmCollectionInterface[db.ProfileCardModel]
	profileCardEmbeddingRepo odm.OdmCollectionInterface[db.ProfileCardEmbeddingModel]
	embedder                 embed.Embedder
}

func ProvideProfileCardSearchHandler(mongo odm.MongoClient, embedder embed.Embedder) *ProfileCardSearchHandler {
	profileCardTextRepo := odm.CollectionOf[db.ProfileCardModel](mongo, "roamind")
	profileCardEmbeddingRepo := odm.CollectionOf[db.ProfileCardEmbeddingModel](mongo, "roamind")

	return &ProfileCardSearchHandler{
		profileCardTextRepo:      profileCardTextRepo,
		profileCardEmbeddingRepo: profileCardEmbeddingRepo,
		embedder:                 embedder,
	}
}

func (h *ProfileCardSearchHandler) SearchProfileCards(ctx context.Context, userId, query string, topK int) ([]db.ProfileCardModel, error) {
	// Update configuration for this search
	textMatchesTask := h.fetchTextMatches(ctx, userId, query, topK)
	vectorMatchesTask := h.fetchVectorMatches(ctx, userId, query, topK)

	textMatchesResult, textMatchErr := async.Await(textMatchesTask)
	vectorMatchesResult, vectorMatchErr := async.Await(vectorMatchesTask)

	if textMatchErr != nil && vectorMatchErr != nil {
		logger.Error("Failed to fetch both text and vector matches", zap.Error(textMatchErr), zap.Error(vectorMatchErr))
		return nil, textMatchErr // return one of the errors
	}

	if textMatchErr != nil {
		logger.Error("Failed to fetch text matches", zap.Error(textMatchErr))
	}

	if vectorMatchErr != nil {
		logger.Error("Failed to fetch vector matches", zap.Error(vectorMatchErr))
	}

	combinedResultsMap := h.combineResults(ctx, textMatchesResult, vectorMatchesResult)

	// Convert map to slice
	combinedResults := make([]db.ProfileCardModel, 0, len(combinedResultsMap))
	for _, textHit := range textMatchesResult {
		combinedResults = append(combinedResults, combinedResultsMap[textHit.Doc.Id()])
		delete(combinedResultsMap, textHit.Doc.Id())
	}

	// Append remaining vector-only matches
	for _, vectorHit := range vectorMatchesResult {
		if card, exists := combinedResultsMap[vectorHit.Doc.ProfileCardId]; exists {
			combinedResults = append(combinedResults, card)
		}
	}

	return combinedResults, nil
}

func (h *ProfileCardSearchHandler) fetchTextMatches(ctx context.Context, userId, query string, topK int) <-chan async.Result[[]odm.SearchHit[db.ProfileCardModel]] {
	return h.profileCardTextRepo.TermSearch(
		ctx,
		query,
		odm.TermSearchParams{
			IndexName: db.TextSearchIndexName,
			Path:      db.TextSearchPaths,
			Limit:     topK,
			Filter:    bson.M{"user_id": userId},
		})
}

func (h *ProfileCardSearchHandler) fetchVectorMatches(ctx context.Context, userId, query string, topK int) <-chan async.Result[[]odm.SearchHit[db.ProfileCardEmbeddingModel]] {
	return async.Go(func() ([]odm.SearchHit[db.ProfileCardEmbeddingModel], error) {
		embedding, err := async.Await(
			h.embedder.GetEmbedding(ctx, query, embed.WithModel("jina-embeddings-v4"), embed.WithRetrievalQueryTask()))

		if err != nil {
			return nil, err
		}

		result, err := async.Await(h.profileCardEmbeddingRepo.VectorSearch(
			ctx,
			embedding,
			odm.VectorSearchParams{
				IndexName:     db.VectorIndexName,
				Path:          db.VectorPath,
				K:             topK,
				NumCandidates: 100,
				Filter:        bson.M{"userId": userId},
			}))

		return result, err
	})
}

func (h *ProfileCardSearchHandler) combineResults(
	ctx context.Context,
	textHits []odm.SearchHit[db.ProfileCardModel],
	vectorHits []odm.SearchHit[db.ProfileCardEmbeddingModel]) map[string]db.ProfileCardModel {
	// Combine results, giving preference to text matches
	combinedResultsMap := make(map[string]db.ProfileCardModel)

	for _, hit := range textHits {
		combinedResultsMap[hit.Doc.Id()] = hit.Doc
	}

	missingCardIds := make([]string, 0)
	for _, hit := range vectorHits {
		if _, exists := combinedResultsMap[hit.Doc.ProfileCardId]; !exists {
			missingCardIds = append(missingCardIds, hit.Doc.ProfileCardId)
		}
	}

	// Bulk fetch missing profile cards from text repo
	if len(missingCardIds) > 0 {
		missingCards, err := async.Await(h.profileCardTextRepo.Find(ctx, bson.M{"_id": bson.M{"$in": missingCardIds}}, nil, 0, 0))

		if err != nil {
			logger.Error("Failed to fetch missing profile cards for vector matches", zap.Strings("missingCardIds", missingCardIds), zap.Error(err))
		} else {
			for _, card := range missingCards {
				combinedResultsMap[card.Id()] = card
			}
		}
	}

	return combinedResultsMap
}
