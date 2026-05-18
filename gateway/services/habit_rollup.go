package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/roamind.ai/gateway/db"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/zap"
)

// rollupTickInterval is how often the rollup engine scans for newly
// completed weeks/months. Once a day is plenty — rollups are write-once
// and an initial pass also runs at startup.
const rollupTickInterval = 24 * time.Hour

// RollupEngine materializes per-week and per-month aggregates for closed
// periods into habit_weekly / habit_monthly. Idempotent: only writes a
// rollup doc if one does not exist for that (user, habit, period).
type RollupEngine struct {
	svc  *HabitService
	once sync.Once
}

// NewRollupEngine returns nil when HabitService is unavailable.
func NewRollupEngine(svc *HabitService) *RollupEngine {
	if svc == nil {
		return nil
	}
	return &RollupEngine{svc: svc}
}

// Start launches a background goroutine that runs the rollup pass once
// immediately and then on each tick. Idempotent.
func (e *RollupEngine) Start(ctx context.Context) {
	if e == nil {
		return
	}
	e.once.Do(func() { go e.loop(ctx) })
}

// RunOnce performs a single rollup pass across all known users. Exposed
// for tests and on-demand backfill.
func (e *RollupEngine) RunOnce(ctx context.Context) error {
	if e == nil {
		return nil
	}
	users, err := e.distinctUsers(ctx)
	if err != nil {
		return err
	}
	for _, userID := range users {
		if err := e.rollupForUser(ctx, userID); err != nil {
			logger.Error("rollup user failed",
				zap.Error(err), zap.String("user_id", userID))
		}
	}
	return nil
}

// --- Internals ----------------------------------------------------------

func (e *RollupEngine) loop(ctx context.Context) {
	logger.Info("habit rollup engine started")
	// Initial pass.
	if err := e.RunOnce(ctx); err != nil {
		logger.Error("initial rollup failed", zap.Error(err))
	}
	t := time.NewTicker(rollupTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.RunOnce(ctx); err != nil {
				logger.Error("rollup tick failed", zap.Error(err))
			}
		}
	}
}

func (e *RollupEngine) distinctUsers(ctx context.Context) ([]string, error) {
	res := e.svc.entriesColl().Distinct(ctx, "user_id", bson.D{})
	if err := res.Err(); err != nil {
		return nil, err
	}
	var users []string
	if err := res.Decode(&users); err != nil {
		return nil, err
	}
	return users, nil
}

func (e *RollupEngine) rollupForUser(ctx context.Context, userID string) error {
	loc, err := e.svc.loadLocation(ctx, userID)
	if err != nil {
		return err
	}
	now := time.Now().In(loc)

	if err := e.rollupWeeks(ctx, userID, loc, now); err != nil {
		return fmt.Errorf("weeks: %w", err)
	}
	if err := e.rollupMonths(ctx, userID, loc, now); err != nil {
		return fmt.Errorf("months: %w", err)
	}
	return nil
}

// rollupWeeks finds completed ISO weeks (strictly before the current
// week) with entries not yet rolled up, computes aggregates, and upserts.
func (e *RollupEngine) rollupWeeks(ctx context.Context, userID string, loc *time.Location, now time.Time) error {
	curStart := startOfISOWeek(now)
	curStartStr := curStart.Format("2006-01-02")

	// Pipeline groups completed weeks by (habit_id, year-week).
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"user_id": userID,
			"date":    bson.M{"$lt": curStartStr},
		}}},
		{{Key: "$addFields", Value: bson.M{
			"date_dt": bson.M{"$dateFromString": bson.M{
				"dateString": "$date",
				"format":     "%Y-%m-%d",
				"timezone":   loc.String(),
			}},
		}}},
		{{Key: "$addFields", Value: bson.M{
			"iso_year": bson.M{"$isoWeekYear": bson.M{"date": "$date_dt", "timezone": loc.String()}},
			"iso_week": bson.M{"$isoWeek": bson.M{"date": "$date_dt", "timezone": loc.String()}},
		}}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{
				"habit_id": "$habit_id",
				"iso_year": "$iso_year",
				"iso_week": "$iso_week",
			},
			"positive":   bson.M{"$sum": "$positive"},
			"negative":   bson.M{"$sum": "$negative"},
			"first_date": bson.M{"$min": "$date"},
		}}},
	}

	cur, err := e.svc.entriesColl().Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	var rows []struct {
		ID struct {
			HabitID string `bson:"habit_id"`
			ISOYear int    `bson:"iso_year"`
			ISOWeek int    `bson:"iso_week"`
		} `bson:"_id"`
		Positive  int    `bson:"positive"`
		Negative  int    `bson:"negative"`
		FirstDate string `bson:"first_date"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return err
	}

	weeklyColl := e.svc.mongo.Database(e.svc.dbName).Collection(db.CollectionHabitWeekly)
	for _, r := range rows {
		weekStart := isoWeekStartDate(r.ID.ISOYear, r.ID.ISOWeek, loc)
		filter := bson.M{
			"user_id":  userID,
			"habit_id": r.ID.HabitID,
			"iso_year": r.ID.ISOYear,
			"iso_week": r.ID.ISOWeek,
		}
		update := bson.M{
			"$set": bson.M{
				"positive":        r.Positive,
				"negative":        r.Negative,
				"week_start_date": weekStart.Format("2006-01-02"),
				"computed_at":     time.Now().Unix(),
			},
			"$setOnInsert": bson.M{"_id": uuid.NewString()},
		}
		if _, err := weeklyColl.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
			return err
		}
	}
	return nil
}

// rollupMonths is the month-equivalent of rollupWeeks for completed
// calendar months (in the user's timezone).
func (e *RollupEngine) rollupMonths(ctx context.Context, userID string, loc *time.Location, now time.Time) error {
	curMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	curMonthStartStr := curMonthStart.Format("2006-01-02")

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"user_id": userID,
			"date":    bson.M{"$lt": curMonthStartStr},
		}}},
		{{Key: "$addFields", Value: bson.M{
			"date_dt": bson.M{"$dateFromString": bson.M{
				"dateString": "$date",
				"format":     "%Y-%m-%d",
				"timezone":   loc.String(),
			}},
		}}},
		{{Key: "$addFields", Value: bson.M{
			"year":  bson.M{"$year": bson.M{"date": "$date_dt", "timezone": loc.String()}},
			"month": bson.M{"$month": bson.M{"date": "$date_dt", "timezone": loc.String()}},
		}}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{
				"habit_id": "$habit_id",
				"year":     "$year",
				"month":    "$month",
			},
			"positive": bson.M{"$sum": "$positive"},
			"negative": bson.M{"$sum": "$negative"},
		}}},
	}

	cur, err := e.svc.entriesColl().Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	var rows []struct {
		ID struct {
			HabitID string `bson:"habit_id"`
			Year    int    `bson:"year"`
			Month   int    `bson:"month"`
		} `bson:"_id"`
		Positive int `bson:"positive"`
		Negative int `bson:"negative"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return err
	}

	monthlyColl := e.svc.mongo.Database(e.svc.dbName).Collection(db.CollectionHabitMonthly)
	for _, r := range rows {
		filter := bson.M{
			"user_id":  userID,
			"habit_id": r.ID.HabitID,
			"year":     r.ID.Year,
			"month":    r.ID.Month,
		}
		update := bson.M{
			"$set": bson.M{
				"positive":    r.Positive,
				"negative":    r.Negative,
				"computed_at": time.Now().Unix(),
			},
			"$setOnInsert": bson.M{"_id": uuid.NewString()},
		}
		if _, err := monthlyColl.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
			return err
		}
	}
	return nil
}

// isoWeekStartDate returns the Monday of the given ISO week, in loc.
func isoWeekStartDate(isoYear, isoWeek int, loc *time.Location) time.Time {
	// Jan 4th is always in ISO week 1; from there walk to that week's Monday,
	// then add (isoWeek-1) weeks.
	jan4 := time.Date(isoYear, time.January, 4, 0, 0, 0, 0, loc)
	wd := int(jan4.Weekday())
	if wd == 0 {
		wd = 7
	}
	week1Mon := jan4.AddDate(0, 0, -(wd - 1))
	return week1Mon.AddDate(0, 0, (isoWeek-1)*7)
}
