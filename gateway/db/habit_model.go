package db

import (
	"context"
	"fmt"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Habit collection / polarity constants.
const (
	CollectionHabits       = "habits"
	CollectionHabitEntries = "habit_entries"
	CollectionHabitWeekly  = "habit_weekly"
	CollectionHabitMonthly = "habit_monthly"

	PolarityPositive = "positive"
	PolarityNegative = "negative"
	PolarityBoth     = "both"
)

// Habit is a user-defined tracker. `Slug` is the lookup key (lower-cased,
// hyphen-joined name) and is unique per user. `Description` is a
// free-form explanation of the activity / objective — surfaced to the
// LLM so it can reason about the habit when giving advice.
type Habit struct {
	ID          string `bson:"_id"`
	UserID      string `bson:"user_id"`
	Name        string `bson:"name"`
	Slug        string `bson:"slug"`
	Polarity    string `bson:"polarity"`
	Description string `bson:"description,omitempty"`
	CreatedAt   int64  `bson:"created_at"`
	ArchivedAt  int64  `bson:"archived_at,omitempty"`
}

// HabitEntry holds one user's counts for one habit on one local-TZ day.
// `Date` is `YYYY-MM-DD` in the user's timezone.
type HabitEntry struct {
	ID        string `bson:"_id,omitempty"`
	UserID    string `bson:"user_id"`
	HabitID   string `bson:"habit_id"`
	Date      string `bson:"date"`
	Positive  int    `bson:"positive"`
	Negative  int    `bson:"negative"`
	UpdatedAt int64  `bson:"updated_at"`
}

// HabitWeekly is a rolled-up week summary for a completed ISO week.
type HabitWeekly struct {
	ID            string `bson:"_id,omitempty"`
	UserID        string `bson:"user_id"`
	HabitID       string `bson:"habit_id"`
	ISOYear       int    `bson:"iso_year"`
	ISOWeek       int    `bson:"iso_week"`
	WeekStartDate string `bson:"week_start_date"`
	Positive      int    `bson:"positive"`
	Negative      int    `bson:"negative"`
	ComputedAt    int64  `bson:"computed_at"`
}

// HabitMonthly is a rolled-up month summary for a completed calendar month.
type HabitMonthly struct {
	ID         string `bson:"_id,omitempty"`
	UserID     string `bson:"user_id"`
	HabitID    string `bson:"habit_id"`
	Year       int    `bson:"year"`
	Month      int    `bson:"month"`
	Positive   int    `bson:"positive"`
	Negative   int    `bson:"negative"`
	ComputedAt int64  `bson:"computed_at"`
}

// EnsureHabitIndexes asserts the indexes the habit subsystem relies on.
// Safe to call repeatedly.
func EnsureHabitIndexes(ctx context.Context, mc odm.MongoClient, dbName string) error {
	db := mc.Database(dbName)

	specs := []struct {
		coll  string
		model mongo.IndexModel
	}{
		{
			CollectionHabits,
			mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "slug", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_user_slug"),
			},
		},
		{
			CollectionHabitEntries,
			mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "habit_id", Value: 1}, {Key: "date", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_user_habit_date"),
			},
		},
		{
			CollectionHabitEntries,
			mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "date", Value: 1}},
				Options: options.Index().SetName("user_date"),
			},
		},
		{
			CollectionHabitWeekly,
			mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "habit_id", Value: 1}, {Key: "iso_year", Value: 1}, {Key: "iso_week", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_user_habit_week"),
			},
		},
		{
			CollectionHabitMonthly,
			mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "habit_id", Value: 1}, {Key: "year", Value: 1}, {Key: "month", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_user_habit_month"),
			},
		},
	}

	for _, s := range specs {
		if _, err := db.Collection(s.coll).Indexes().CreateOne(ctx, s.model); err != nil {
			return fmt.Errorf("ensure index on %s: %w", s.coll, err)
		}
	}
	return nil
}
