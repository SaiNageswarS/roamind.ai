package activities

import (
	"github.com/SaiNageswarS/go-api-boot/embed"
	"github.com/SaiNageswarS/go-api-boot/odm"
)

type Activities struct {
	mongo    odm.MongoClient
	embedder embed.Embedder
}

func ProvideActivities(mongo odm.MongoClient, embedder embed.Embedder) *Activities {
	return &Activities{
		mongo:    mongo,
		embedder: embedder,
	}
}

const Tenant = "roamind"
