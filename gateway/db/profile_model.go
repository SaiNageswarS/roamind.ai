package db

import (
	"context"
	"errors"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// DefaultTimezone is the fallback IANA TZ when the user has no profile or
// has not set one.
const DefaultTimezone = "Asia/Kolkata"

// ProfileRepo reads the agent-owned `user_profiles` collection. Writes
// are the agent's responsibility — the gateway only reads from here.
type ProfileRepo struct {
	mongo  odm.MongoClient
	dbName string
}

// NewProfileRepo returns nil when no mongo client is available.
func NewProfileRepo(mc odm.MongoClient, dbName string) *ProfileRepo {
	if mc == nil {
		return nil
	}
	return &ProfileRepo{mongo: mc, dbName: dbName}
}

// GetTimezone returns the user's IANA timezone, or DefaultTimezone if the
// user has no profile, no timezone, or the repo is unavailable.
func (r *ProfileRepo) GetTimezone(ctx context.Context, userID string) string {
	if r == nil || userID == "" {
		return DefaultTimezone
	}
	coll := r.mongo.Database(r.dbName).Collection("user_profiles")
	var doc struct {
		Timezone string `bson:"timezone"`
	}
	err := coll.FindOne(ctx, bson.M{"user_id": userID}).Decode(&doc)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			// Best-effort; fall through to default.
		}
		return DefaultTimezone
	}
	if doc.Timezone == "" {
		return DefaultTimezone
	}
	return doc.Timezone
}
