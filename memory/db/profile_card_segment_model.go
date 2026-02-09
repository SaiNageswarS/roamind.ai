package db

import "github.com/SaiNageswarS/go-api-boot/odm"

type ProfileCardSegmentModel struct {
	SegmentId       string `bson:"_id"`
	UserId          string `bson:"userId"`
	ProfileCardId   string `bson:"profileCardId"`
	Content         string `bson:"content"`
	ChunkIndex      int    `bson:"chunkIndex"`   // Index of the chunk within the profile card
	SegmentIndex    int    `bson:"segmentIndex"` // Index of the segment within the chunk
	TokenCount      int    `bson:"tokenCount"`
	OriginalSection string `bson:"originalSection"` // The markdown section this segment came from
}

func (p ProfileCardSegmentModel) Id() string {
	return p.SegmentId
}

func (p ProfileCardSegmentModel) CollectionName() string {
	return "profile_card_segments"
}

// Indexes
func (p ProfileCardSegmentModel) TermSearchIndexSpecs() []odm.TermSearchIndexSpec {
	return []odm.TermSearchIndexSpec{
		{
			Name:  SegmentTextSearchIndexName,
			Paths: SegmentSearchPaths,
		},
	}
}

const SegmentTextSearchIndexName = "profileCardSegmentTextIndex"

var SegmentSearchPaths = []string{"content", "originalSection"}
