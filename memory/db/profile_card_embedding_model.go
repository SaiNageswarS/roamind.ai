package db

import (
	"github.com/SaiNageswarS/go-api-boot/odm"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type ProfileCardEmbeddingModel struct {
	ContentHash   string      `bson:"_id"` // hash of the content for quick lookup
	UserId        string      `bson:"userId"`
	ProfileCardId string      `bson:"profileCardId"` // same as ProfileCardModel.CardId
	Embedding     bson.Vector `bson:"embedding"`
}

func (p ProfileCardEmbeddingModel) Id() string {
	return p.ContentHash
}

func (p ProfileCardEmbeddingModel) CollectionName() string {
	return "profile_card_embeddings"
}

// Indexes
func (p ProfileCardEmbeddingModel) VectorIndexSpecs() []odm.VectorIndexSpec {
	return []odm.VectorIndexSpec{
		{
			Name:          VectorIndexName,
			Path:          VectorPath,
			Type:          "vector",
			NumDimensions: EmbeddingDimensions,
			Similarity:    "cosine",
			Quantization:  "scalar",
		},
	}
}

const VectorIndexName = "profileCardEmbeddingIndex"
const VectorPath = "embedding"

const EmbeddingDimensions = 2048 // Embedding Gemma
