package activities

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"

	"github.com/SaiNageswarS/go-api-boot/embed"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.uber.org/zap"
)

func (a *Activities) EmbedProfileCard(ctx context.Context, profileCardId string) error {
	profileCard, err := async.Await(
		odm.CollectionOf[db.ProfileCardModel](a.mongo, Tenant).FindOneByID(ctx, profileCardId))

	if err != nil {
		logger.Error("Failed to find profile card", zap.String("profileCardId", profileCardId), zap.Error(err))
		return err
	}

	// Get embedding of key, title and aliases in structured markdown format
	headerTextForEmbedding := fmt.Sprintf("# Title\n%s\n\n# Aliases\n%s",
		profileCard.Title,
		strings.Join(profileCard.Aliases, ", "))

	embeddingTasks := make([]<-chan async.Result[string], 0)
	// Embed and save header text
	headerTextEmbeddingTask := a.embedAndSaveText(ctx, headerTextForEmbedding, profileCard.UserId, profileCard.CardId)
	embeddingTasks = append(embeddingTasks, headerTextEmbeddingTask)

	// Get embedding of content in markdown format
	for _, para := range profileCard.ContentMd {
		contentEmbeddingTask := a.embedAndSaveText(ctx, para, profileCard.UserId, profileCard.CardId)
		embeddingTasks = append(embeddingTasks, contentEmbeddingTask)
	}

	// Await all tasks
	_, err = async.AwaitAll(embeddingTasks...)

	return err
}

func (a *Activities) embedAndSaveText(ctx context.Context, text, userId, profileCardId string) <-chan async.Result[string] {
	return async.Go(func() (string, error) {
		embedding, err := async.Await(
			a.embedder.GetEmbedding(ctx, text, embed.WithModel("jina-embeddings-v4"), embed.WithRetrievalPassageTask(), embed.WithLateChunking(true)))

		if err != nil {
			return "", err
		}

		// compute md5 hash of the text for id
		hash := md5.Sum([]byte(text))
		id := fmt.Sprintf("%x", hash)

		profileCardEmbedding := db.ProfileCardEmbeddingModel{
			ContentHash:   id,
			Embedding:     bson.NewVector(embedding),
			UserId:        userId,
			ProfileCardId: profileCardId,
		}

		_, err = async.Await(
			odm.CollectionOf[db.ProfileCardEmbeddingModel](a.mongo, Tenant).Save(ctx, profileCardEmbedding))

		if err != nil {
			return "", err
		}

		return id, nil
	})
}
