package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/roamind.ai/gateway/db"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// HabitService owns the gateway-side habit CRUD path. Reads & writes the
// habit_* collections directly via the underlying mongo driver because we
// need atomic `$inc` upserts and aggregation pipelines that the typed odm
// wrapper does not expose.
type HabitService struct {
	mongo   odm.MongoClient
	dbName  string
	profile *db.ProfileRepo
}

// NewHabitService returns nil when no mongo client is wired up. Habit
// commands are then effectively disabled (ParseAndExecute reports
// handled=false on any input).
func NewHabitService(mc odm.MongoClient, dbName string, profile *db.ProfileRepo) *HabitService {
	if mc == nil {
		return nil
	}
	return &HabitService{mongo: mc, dbName: dbName, profile: profile}
}

// --- Public API: command entrypoint -------------------------------------

// ParseAndExecute routes a raw inbound text line. Returns:
//   - handled=false when text does not begin with a recognized /habit_*
//     command (caller should fall through to the LLM path).
//   - handled=true with reply text otherwise. err is set only on internal
//     failures the caller may want to log; user-facing errors are folded
//     into reply.
func (s *HabitService) ParseAndExecute(ctx context.Context, userID, raw string) (reply string, handled bool, err error) {
	if s == nil {
		return "", false, nil
	}
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "/habit_") {
		return "", false, nil
	}
	if userID == "" {
		return "habit: missing user", true, nil
	}

	cmd, rest := splitCmd(text)
	switch cmd {
	case "/habit_add":
		return s.cmdAdd(ctx, userID, rest)
	case "/habit_desc":
		return s.cmdDesc(ctx, userID, rest)
	case "/habit_inc":
		return s.cmdAdjust(ctx, userID, rest, +1, 0)
	case "/habit_dec":
		return s.cmdAdjust(ctx, userID, rest, 0, +1)
	case "/habit_list":
		return s.cmdList(ctx, userID)
	case "/habit_today":
		return s.cmdToday(ctx, userID)
	case "/habit_week":
		return s.cmdWeek(ctx, userID)
	case "/habit_help", "/habit":
		return habitHelp(), true, nil
	default:
		return "habit: unknown command. " + habitHelp(), true, nil
	}
}

// --- Public API: CRUD primitives (exported for rollup / tests) ----------

// AddHabit inserts a habit if its slug is unused for the user. Returns
// the created or existing habit. `description` is optional free text.
func (s *HabitService) AddHabit(ctx context.Context, userID, name, polarity, description string) (*db.Habit, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, errors.New("name required")
	}
	polarity = normalizePolarity(polarity)
	description = strings.TrimSpace(description)
	slug := slugify(name)
	if slug == "" {
		return nil, false, errors.New("name produces empty slug")
	}

	coll := s.habitsColl()
	// Try insert; on duplicate, return existing.
	now := time.Now().Unix()
	habit := db.Habit{
		ID:          uuid.NewString(),
		UserID:      userID,
		Name:        name,
		Slug:        slug,
		Polarity:    polarity,
		Description: description,
		CreatedAt:   now,
	}
	_, err := coll.InsertOne(ctx, habit)
	if err == nil {
		return &habit, true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return nil, false, fmt.Errorf("insert habit: %w", err)
	}
	existing, ferr := s.findHabitBySlug(ctx, userID, slug)
	if ferr != nil {
		return nil, false, ferr
	}
	return existing, false, nil
}

// SetDescription updates the free-form description on an existing habit.
func (s *HabitService) SetDescription(ctx context.Context, userID, token, description string) (*db.Habit, error) {
	habit, err := s.ResolveHabit(ctx, userID, token)
	if err != nil {
		return nil, err
	}
	description = strings.TrimSpace(description)
	update := bson.M{"$set": bson.M{"description": description}}
	if _, err := s.habitsColl().UpdateOne(ctx, bson.M{"_id": habit.ID}, update); err != nil {
		return nil, fmt.Errorf("update description: %w", err)
	}
	habit.Description = description
	return habit, nil
}

// AdjustHabit increments positive/negative counters atomically for today
// in the user's timezone. Either pInc or nInc may be zero. Returns the
// resolved habit and the updated entry.
func (s *HabitService) AdjustHabit(ctx context.Context, userID, token string, pInc, nInc int) (*db.Habit, *db.HabitEntry, error) {
	habit, err := s.ResolveHabit(ctx, userID, token)
	if err != nil {
		return nil, nil, err
	}
	date, err := s.todayInUserTZ(ctx, userID)
	if err != nil {
		return habit, nil, err
	}
	entry, err := s.incEntry(ctx, userID, habit.ID, date, pInc, nInc)
	if err != nil {
		return habit, nil, err
	}
	return habit, entry, nil
}

// ListHabits returns all non-archived habits for the user, sorted by name.
func (s *HabitService) ListHabits(ctx context.Context, userID string) ([]db.Habit, error) {
	cur, err := s.habitsColl().Find(
		ctx,
		bson.M{"user_id": userID, "archived_at": bson.M{"$exists": false}},
	)
	if err != nil {
		return nil, err
	}
	var habits []db.Habit
	if err := cur.All(ctx, &habits); err != nil {
		return nil, err
	}
	sort.Slice(habits, func(i, j int) bool { return habits[i].Name < habits[j].Name })
	return habits, nil
}

// ResolveHabit finds a habit by slug (preferred) or case-insensitive
// name. Ambiguous prefix matches return an error listing candidates.
func (s *HabitService) ResolveHabit(ctx context.Context, userID, token string) (*db.Habit, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("habit name required")
	}
	slug := slugify(token)
	if habit, err := s.findHabitBySlug(ctx, userID, slug); err == nil && habit != nil {
		return habit, nil
	}
	// Case-insensitive prefix match across name/slug.
	all, err := s.ListHabits(ctx, userID)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(token)
	var matches []db.Habit
	for _, h := range all {
		if strings.ToLower(h.Name) == lower || h.Slug == slug {
			return &h, nil
		}
		if strings.HasPrefix(strings.ToLower(h.Name), lower) || strings.HasPrefix(h.Slug, slug) {
			matches = append(matches, h)
		}
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, h := range matches {
			names = append(names, h.Slug)
		}
		return nil, fmt.Errorf("ambiguous habit %q. Did you mean: %s", token, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("habit %q not found. Use /habit_add %s [positive|negative|both] to create it", token, token)
}

// SumEntries aggregates positive/negative counts for a user across a
// date range (inclusive, `YYYY-MM-DD` strings), optionally restricted to
// a single habit_id. Returns map keyed by habit_id.
func (s *HabitService) SumEntries(ctx context.Context, userID, fromDate, toDate, habitID string) (map[string]struct{ Positive, Negative int }, error) {
	match := bson.M{
		"user_id": userID,
		"date":    bson.M{"$gte": fromDate, "$lte": toDate},
	}
	if habitID != "" {
		match["habit_id"] = habitID
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$group", Value: bson.M{
			"_id":      "$habit_id",
			"positive": bson.M{"$sum": "$positive"},
			"negative": bson.M{"$sum": "$negative"},
		}}},
	}
	cur, err := s.entriesColl().Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID       string `bson:"_id"`
		Positive int    `bson:"positive"`
		Negative int    `bson:"negative"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]struct{ Positive, Negative int }, len(rows))
	for _, r := range rows {
		out[r.ID] = struct{ Positive, Negative int }{r.Positive, r.Negative}
	}
	return out, nil
}

// UserTimezone returns the user's IANA timezone with fallback.
func (s *HabitService) UserTimezone(ctx context.Context, userID string) string {
	if s.profile == nil {
		return db.DefaultTimezone
	}
	return s.profile.GetTimezone(ctx, userID)
}

// UserLocation returns the user's loadable timezone location.
func (s *HabitService) UserLocation(ctx context.Context, userID string) (*time.Location, error) {
	return s.loadLocation(ctx, userID)
}

// EntriesColl returns the habit entries collection.
func (s *HabitService) EntriesColl() *mongo.Collection {
	return s.entriesColl()
}

// Mongo returns the configured Mongo client.
func (s *HabitService) Mongo() odm.MongoClient {
	return s.mongo
}

// DBName returns the configured database name.
func (s *HabitService) DBName() string {
	return s.dbName
}

// --- Command handlers ---------------------------------------------------

func (s *HabitService) cmdAdd(ctx context.Context, userID, rest string) (string, bool, error) {
	if rest == "" {
		return "usage: /habit_add <name> [positive|negative|both] [-- description]", true, nil
	}
	head, description := splitDescription(rest)
	name, polarity := splitNameAndPolarity(head)
	habit, created, err := s.AddHabit(ctx, userID, name, polarity, description)
	if err != nil {
		return "habit_add: " + err.Error(), true, nil
	}
	if !created {
		return fmt.Sprintf("habit already exists: %s (%s)", habit.Slug, habit.Polarity), true, nil
	}
	return fmt.Sprintf("added habit: %s (%s)%s", habit.Slug, habit.Polarity, descSuffix(habit.Description)), true, nil
}

func (s *HabitService) cmdDesc(ctx context.Context, userID, rest string) (string, bool, error) {
	if rest == "" {
		return "usage: /habit_desc <name> <description>", true, nil
	}
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return "usage: /habit_desc <name> <description>", true, nil
	}
	habit, err := s.SetDescription(ctx, userID, parts[0], parts[1])
	if err != nil {
		return "habit_desc: " + err.Error(), true, nil
	}
	if habit.Description == "" {
		return fmt.Sprintf("cleared description for %s", habit.Slug), true, nil
	}
	return fmt.Sprintf("updated %s: %s", habit.Slug, habit.Description), true, nil
}

func (s *HabitService) cmdAdjust(ctx context.Context, userID, rest string, pInc, nInc int) (string, bool, error) {
	if rest == "" {
		if pInc > 0 {
			return "usage: /habit_inc <name>", true, nil
		}
		return "usage: /habit_dec <name>", true, nil
	}
	habit, entry, err := s.AdjustHabit(ctx, userID, rest, pInc, nInc)
	if err != nil {
		return "habit: " + err.Error(), true, nil
	}
	return fmt.Sprintf("%s [%s]: +%d / -%d", habit.Slug, entry.Date, entry.Positive, entry.Negative), true, nil
}

func (s *HabitService) cmdList(ctx context.Context, userID string) (string, bool, error) {
	habits, err := s.ListHabits(ctx, userID)
	if err != nil {
		return "habit_list: " + err.Error(), true, nil
	}
	if len(habits) == 0 {
		return "no habits yet. Try /habit_add <name>", true, nil
	}
	var b strings.Builder
	b.WriteString("habits:\n")
	for _, h := range habits {
		fmt.Fprintf(&b, "- %s (%s)%s\n", h.Slug, h.Polarity, descSuffix(h.Description))
	}
	return strings.TrimRight(b.String(), "\n"), true, nil
}

func (s *HabitService) cmdToday(ctx context.Context, userID string) (string, bool, error) {
	date, err := s.todayInUserTZ(ctx, userID)
	if err != nil {
		return "habit_today: " + err.Error(), true, nil
	}
	return s.renderRange(ctx, userID, date, date, fmt.Sprintf("today (%s)", date))
}

func (s *HabitService) cmdWeek(ctx context.Context, userID string) (string, bool, error) {
	tz, err := s.loadLocation(ctx, userID)
	if err != nil {
		return "habit_week: " + err.Error(), true, nil
	}
	now := time.Now().In(tz)
	start := startOfISOWeek(now)
	from := start.Format("2006-01-02")
	to := now.Format("2006-01-02")
	return s.renderRange(ctx, userID, from, to, fmt.Sprintf("this week (%s..%s)", from, to))
}

// --- Internals ----------------------------------------------------------

func (s *HabitService) habitsColl() *mongo.Collection {
	return s.mongo.Database(s.dbName).Collection(db.CollectionHabits)
}

func (s *HabitService) entriesColl() *mongo.Collection {
	return s.mongo.Database(s.dbName).Collection(db.CollectionHabitEntries)
}

func (s *HabitService) findHabitBySlug(ctx context.Context, userID, slug string) (*db.Habit, error) {
	var h db.Habit
	err := s.habitsColl().FindOne(ctx, bson.M{"user_id": userID, "slug": slug}).Decode(&h)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &h, nil
}

func (s *HabitService) incEntry(ctx context.Context, userID, habitID, date string, pInc, nInc int) (*db.HabitEntry, error) {
	now := time.Now().Unix()
	update := bson.M{
		"$inc": bson.M{"positive": pInc, "negative": nInc},
		"$set": bson.M{"updated_at": now},
		"$setOnInsert": bson.M{
			"_id":      uuid.NewString(),
			"user_id":  userID,
			"habit_id": habitID,
			"date":     date,
		},
	}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := s.entriesColl().UpdateOne(
		ctx,
		bson.M{"user_id": userID, "habit_id": habitID, "date": date},
		update,
		opts,
	)
	if err != nil {
		return nil, fmt.Errorf("inc entry: %w", err)
	}
	var entry db.HabitEntry
	if err := s.entriesColl().FindOne(
		ctx,
		bson.M{"user_id": userID, "habit_id": habitID, "date": date},
	).Decode(&entry); err != nil {
		return nil, fmt.Errorf("reload entry: %w", err)
	}
	return &entry, nil
}

func (s *HabitService) renderRange(ctx context.Context, userID, fromDate, toDate, label string) (string, bool, error) {
	sums, err := s.SumEntries(ctx, userID, fromDate, toDate, "")
	if err != nil {
		return "habit: " + err.Error(), true, nil
	}
	habits, err := s.ListHabits(ctx, userID)
	if err != nil {
		return "habit: " + err.Error(), true, nil
	}
	if len(habits) == 0 {
		return "no habits yet. Try /habit_add <name>", true, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", label)
	for _, h := range habits {
		v := sums[h.ID]
		fmt.Fprintf(&b, "- %s: +%d / -%d\n", h.Slug, v.Positive, v.Negative)
	}
	return strings.TrimRight(b.String(), "\n"), true, nil
}

func (s *HabitService) todayInUserTZ(ctx context.Context, userID string) (string, error) {
	loc, err := s.loadLocation(ctx, userID)
	if err != nil {
		return "", err
	}
	return time.Now().In(loc).Format("2006-01-02"), nil
}

func (s *HabitService) loadLocation(ctx context.Context, userID string) (*time.Location, error) {
	tz := s.UserTimezone(ctx, userID)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Fallback to default.
		fallback, fbErr := time.LoadLocation(db.DefaultTimezone)
		if fbErr != nil {
			return time.UTC, nil
		}
		return fallback, nil
	}
	return loc, nil
}

func splitCmd(text string) (cmd, rest string) {
	parts := strings.SplitN(text, " ", 2)
	cmd = strings.ToLower(parts[0])
	if len(parts) == 2 {
		rest = strings.TrimSpace(parts[1])
	}
	return cmd, rest
}

func splitNameAndPolarity(rest string) (name, polarity string) {
	// Trailing token may be a polarity keyword.
	tokens := strings.Fields(rest)
	if len(tokens) == 0 {
		return "", ""
	}
	last := strings.ToLower(tokens[len(tokens)-1])
	switch last {
	case db.PolarityPositive, db.PolarityNegative, db.PolarityBoth:
		return strings.Join(tokens[:len(tokens)-1], " "), last
	}
	return strings.Join(tokens, " "), ""
}

// splitDescription separates the head (name + optional polarity) from
// the description using `--` as the delimiter. Returns head with the
// description trimmed away and the description text (may be empty).
func splitDescription(rest string) (head, description string) {
	idx := strings.Index(rest, " -- ")
	if idx < 0 {
		if strings.HasSuffix(rest, " --") {
			return strings.TrimSpace(rest[:len(rest)-3]), ""
		}
		return strings.TrimSpace(rest), ""
	}
	return strings.TrimSpace(rest[:idx]), strings.TrimSpace(rest[idx+4:])
}

func descSuffix(description string) string {
	if description == "" {
		return ""
	}
	return " — " + description
}

func normalizePolarity(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case db.PolarityPositive:
		return db.PolarityPositive
	case db.PolarityNegative:
		return db.PolarityNegative
	default:
		return db.PolarityBoth
	}
}

func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-', r == '_', r == ' ':
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// startOfISOWeek returns the Monday 00:00 of t's ISO week, in t's location.
func startOfISOWeek(t time.Time) time.Time {
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // Sunday -> 7
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -(wd - 1))
}

func habitHelp() string {
	return strings.Join([]string{
		"habit commands:",
		"  /habit_add <name> [positive|negative|both] [-- description]",
		"  /habit_desc <name> <description>",
		"  /habit_inc <name>",
		"  /habit_dec <name>",
		"  /habit_list",
		"  /habit_today",
		"  /habit_week",
	}, "\n")
}
