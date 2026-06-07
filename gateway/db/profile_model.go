package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DefaultTimezone is the fallback IANA TZ when the user has no profile or
// has not set one.
const DefaultTimezone = "Asia/Kolkata"

// ValidModes is the set of accepted scheduling modes.
var ValidModes = map[string]bool{
	"maintenance": true,
	"focus":       true,
	"vacation":    true,
	"recovery":    true,
	"deep_work":   true,
}

// ProfileRepo reads and writes the `user_profiles` collection.
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

// GetMode returns the user's mode ("maintenance", "focus", "vacation", etc)
// or "maintenance" as default.
func (r *ProfileRepo) GetMode(ctx context.Context, userID string) string {
	if r == nil || userID == "" {
		return "maintenance"
	}
	coll := r.mongo.Database(r.dbName).Collection("user_profiles")
	var doc struct {
		Mode string `bson:"mode"`
	}
	err := coll.FindOne(ctx, bson.M{"user_id": userID}).Decode(&doc)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			// Best-effort; fall through to default.
		}
		return "maintenance"
	}
	if doc.Mode == "" {
		return "maintenance"
	}
	return doc.Mode
}

// SetMode writes the mode field to user_profiles, upserting the document if
// it does not yet exist. Returns an error if the mode is not a ValidMode.
func (r *ProfileRepo) SetMode(ctx context.Context, userID, mode string) error {
	if r == nil {
		return errors.New("profile repo unavailable")
	}
	if !ValidModes[mode] {
		return fmt.Errorf("invalid mode %q", mode)
	}
	coll := r.mongo.Database(r.dbName).Collection("user_profiles")
	filter := bson.M{"user_id": userID}
	update := bson.M{"$set": bson.M{"mode": mode, "user_id": userID}}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := coll.UpdateOne(ctx, filter, update, opts)
	return err
}
