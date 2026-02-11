package handler

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/SaiNageswarS/go-api-boot/embed"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/go-collection-boot/linq"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.uber.org/zap"
)

const Tenant = "roamind"

type ProfileCardHandler struct {
	mongo    odm.MongoClient
	embedder embed.Embedder
}

func ProvideProfileCardHandler(mongo odm.MongoClient, embedder embed.Embedder) *ProfileCardHandler {
	return &ProfileCardHandler{
		mongo:    mongo,
		embedder: embedder,
	}
}

func (h *ProfileCardHandler) SaveProfileCard(
	ctx context.Context,
	userId string,
	key string,
	title string,
	aliases []string,
	contentMdFilePath string,
) (string, error) {
	logger.Info("Starting profile card save process",
		zap.String("userId", userId),
		zap.String("key", key),
		zap.String("title", title))

	// Step 1: Save the profile card
	profileCard, err := h.saveProfileCard(ctx, userId, key, title, aliases, contentMdFilePath)
	if err != nil {
		logger.Error("Failed to save profile card", zap.Error(err))
		return "", err
	}

	logger.Info("Profile card saved successfully", zap.String("profileCardId", profileCard.Id()))

	// Step 2: Segment the profile card
	segmentedProfileCard, err := h.segmentProfileCard(ctx, profileCard)
	if err != nil {
		logger.Error("Failed to segment profile card", zap.Error(err))
		return "", err
	}

	logger.Info("Profile card segmented successfully", zap.String("segmentedProfileCardId", segmentedProfileCard.Id()))

	// Step 3: Embed the profile card
	err = h.embedProfileCard(ctx, segmentedProfileCard)
	if err != nil {
		logger.Error("Failed to embed profile card", zap.Error(err))
		return "", err
	}

	logger.Info("Profile card embedded successfully", zap.String("segmentedProfileCardId", segmentedProfileCard.Id()))

	return profileCard.Id(), nil
}

func (h *ProfileCardHandler) saveProfileCard(ctx context.Context, userId, key, title string, aliases []string, contentMdFilePath string) (*db.ProfileCardModel, error) {
	// read content from file
	contentMd, err := readContentFromFile(contentMdFilePath)
	if err != nil {
		return nil, err
	}

	profileCard := db.ProfileCardModel{
		UserId:    userId,
		Key:       key,
		Title:     title,
		Aliases:   aliases,
		ContentMd: []string{contentMd},
	}

	// save to db
	_, err = async.Await(
		odm.CollectionOf[db.ProfileCardModel](h.mongo, Tenant).Save(ctx, profileCard))

	if err != nil {
		return nil, err
	}

	return &profileCard, nil
}

func (h *ProfileCardHandler) segmentProfileCard(ctx context.Context, profileCard *db.ProfileCardModel) (*db.ProfileCardModel, error) {
	if len(profileCard.ContentMd) == 0 {
		return nil, errors.New("profile card has no content to segment")
	}

	markdownContent := profileCard.ContentMd[0]
	chunks, err := h.parseMarkdownSections(ctx, markdownContent, 1000*4) // ~1000 tokens * 4 chars per token
	if err != nil {
		logger.Error("Failed to parse markdown sections", zap.String("profileCardId", profileCard.Id()), zap.Error(err))
		return nil, err
	}

	// Update ContentMd with the segmented chunks
	profileCard.ContentMd = chunks

	_, err = async.Await(
		odm.CollectionOf[db.ProfileCardModel](h.mongo, Tenant).Save(ctx, *profileCard))

	if err != nil {
		return nil, err
	}

	return profileCard, nil
}

func (h *ProfileCardHandler) embedProfileCard(ctx context.Context, profileCard *db.ProfileCardModel) error {
	// Get embedding of key, title and aliases in structured markdown format
	headerTextForEmbedding := fmt.Sprintf("# Title\n%s\n\n# Aliases\n%s",
		profileCard.Title,
		strings.Join(profileCard.Aliases, ", "))

	embeddingTasks := make([]<-chan async.Result[string], 0)
	// Embed and save header text
	headerTextEmbeddingTask := h.embedAndSaveText(ctx, headerTextForEmbedding, profileCard.UserId, profileCard.CardId)
	embeddingTasks = append(embeddingTasks, headerTextEmbeddingTask)

	// Get embedding of content in markdown format
	for _, para := range profileCard.ContentMd {
		contentEmbeddingTask := h.embedAndSaveText(ctx, para, profileCard.UserId, profileCard.CardId)
		embeddingTasks = append(embeddingTasks, contentEmbeddingTask)
	}

	// Await all tasks
	_, err := async.AwaitAll(embeddingTasks...)

	return err
}

func (h *ProfileCardHandler) parseMarkdownSections(ctx context.Context, content string, minBytes int) ([]string, error) {
	if content == "" {
		return nil, errors.New("empty content")
	}

	// Split by ## headers (level 2 headings)
	parts := strings.Split(content, "\n## ")
	if len(parts) == 1 {
		// No ## headers found, return entire content as single chunk
		return []string{content}, nil
	}

	// Build sections
	var sections []string

	currSectionString := ""

	_, err := linq.Pipe3(
		linq.FromSlice(ctx, parts),
		// Remove empty parts before processing
		linq.Where(func(part string) bool {
			return strings.TrimSpace(part) != ""
		}),
		// Add ## prefix back to all sections except the first one
		linq.Select(func(part string) string {
			if strings.HasPrefix(part, "# ") {
				// If the part starts with a level 1 header, keep it as is (handles the first section case)
				return part
			}
			// Otherwise, add ## prefix back
			return "## " + strings.TrimSpace(part)
		}),
		linq.ForEach(func(section string) {
			currSectionString += section + "\n\n"

			if len(currSectionString) >= minBytes {
				sections = append(sections, strings.TrimSpace(currSectionString))
				currSectionString = ""
			}
		}),
	)

	if err != nil {
		return nil, err
	}

	// Add any remaining content as a section
	if strings.TrimSpace(currSectionString) != "" {
		sections = append(sections, strings.TrimSpace(currSectionString))
	}

	return sections, nil
}

func (h *ProfileCardHandler) embedAndSaveText(ctx context.Context, text, userId, profileCardId string) <-chan async.Result[string] {
	return async.Go(func() (string, error) {
		embedding, err := async.Await(
			h.embedder.GetEmbedding(ctx, text, embed.WithModel("jina-embeddings-v4"), embed.WithRetrievalPassageTask(), embed.WithLateChunking(true)))

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
			odm.CollectionOf[db.ProfileCardEmbeddingModel](h.mongo, Tenant).Save(ctx, profileCardEmbedding))

		if err != nil {
			return "", err
		}

		return id, nil
	})
}

func readContentFromFile(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
