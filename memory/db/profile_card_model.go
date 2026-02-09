package db

import "github.com/SaiNageswarS/go-api-boot/odm"

type ProfileCardModel struct {
	CardId string `bson:"_id"`
	UserId string `bson:"user_id"`
	Key    string `bson:"key"`

	Title   string   `bson:"title"`
	Aliases []string `bson:"aliases"`

	ContentMd []string `bson:"content_md"` // content in markdown format
}

func (p ProfileCardModel) Id() string {
	if p.CardId == "" {
		p.CardId = p.UserId + "_" + p.Key
	}

	return p.CardId
}

func (p ProfileCardModel) CollectionName() string {
	return "profile_cards"
}

// Indexes
func (p ProfileCardModel) TermSearchIndexSpecs() []odm.TermSearchIndexSpec {
	return []odm.TermSearchIndexSpec{
		{
			Name:  TextSearchIndexName,
			Paths: TextSearchPaths,
		},
	}
}

const TextSearchIndexName = "profileCardTextIndex"

var TextSearchPaths = []string{"content_md", "aliases", "title"}
