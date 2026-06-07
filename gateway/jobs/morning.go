package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/roamind.ai/gateway/db"
	"github.com/SaiNageswarS/roamind.ai/gateway/services"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	morningCheckInterval = 15 * time.Minute
	morningStartHour     = 8
	morningEndHour       = 11
	morningLookbackDays  = 14
)

// morningPrompt builds a day-start prompt that references habit history over
// the past two weeks rather than today (which has no entries yet).
func morningPrompt(today, twoWeeksAgo string) string {
	return fmt.Sprintf(
		`Good morning! It's a new day (%s). To help plan today, review my tasks and habit history. Pick habit history from %s to %s (the past two weeks) and use it to suggest a practical schedule. Highlight patterns, prioritize tasks and note which habits have been consistent or slipping, and recommend a concise order of work for today.`,
		today, twoWeeksAgo, today,
	)
}

// MorningJob sends a daily planning prompt to users over Telegram.
type MorningJob struct {
	rdb     *redis.Client
	logins  odm.OdmCollectionInterface[db.LoginModel]
	habit   *services.HabitService
	profile *db.ProfileRepo
	mu      sync.Mutex
	sent    map[string]string
	once    sync.Once
}

// NewMorningJob returns nil when required dependencies are unavailable.
func NewMorningJob(rdb *redis.Client, logins odm.OdmCollectionInterface[db.LoginModel], habit *services.HabitService, profile *db.ProfileRepo) *MorningJob {
	if rdb == nil || logins == nil || habit == nil || profile == nil {
		return nil
	}
	return &MorningJob{
		rdb:     rdb,
		logins:  logins,
		habit:   habit,
		profile: profile,
		sent:    make(map[string]string),
	}
}

// Start begins the background scheduler.
func (j *MorningJob) Start(ctx context.Context) {
	if j == nil {
		return
	}
	j.once.Do(func() { go j.loop(ctx) })
}

func (j *MorningJob) loop(ctx context.Context) {
	logger.Info("morning scheduler started")
	j.checkAndSend(ctx)
	ticker := time.NewTicker(morningCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.checkAndSend(ctx)
		}
	}
}

func (j *MorningJob) checkAndSend(ctx context.Context) {
	logins, err := j.fetchTelegramLogins(ctx)
	if err != nil {
		logger.Error("fetch telegram logins failed", zap.Error(err))
		return
	}
	if len(logins) == 0 {
		return
	}

	for _, login := range logins {
		if login.Telegram == nil {
			continue
		}
		if err := j.sendIfDue(ctx, login); err != nil {
			logger.Error("morning schedule send failed",
				zap.Error(err), zap.String("user_id", login.UserID))
		}
	}
}

func (j *MorningJob) fetchTelegramLogins(ctx context.Context) ([]db.LoginModel, error) {
	logins, err := async.Await(j.logins.Find(ctx, bson.M{"telegram.chat_id": bson.M{"$exists": true}}, bson.D{}, 0, 0))
	if err != nil {
		return nil, err
	}
	return logins, nil
}

func (j *MorningJob) sendIfDue(ctx context.Context, login db.LoginModel) error {
	loc := j.userLocation(ctx, login.UserID)
	now := time.Now().In(loc)
	if !j.shouldSend(login.UserID, now) {
		return nil
	}

	// Mode guidance is provided by the agent's system prompt; avoid
	// duplicating it here to prevent conflicting instructions.
	today := now.Format("2006-01-02")
	twoWeeksAgo := now.AddDate(0, 0, -morningLookbackDays).Format("2006-01-02")
	prompt := morningPrompt(today, twoWeeksAgo)

	internalID := uuid.NewString()
	taskIn := &pb.TaskIn{
		Id:           internalID,
		TraceId:      internalID,
		UserId:       login.UserID,
		Channel:      "telegram",
		ChannelMsgId: "",
		Text:         prompt,
		ReceivedAt:   timestamppb.Now(),
	}
	if _, err := services.XAddTaskIn(ctx, j.rdb, taskIn); err != nil {
		return fmt.Errorf("enqueue morning prompt: %w", err)
	}
	j.markSent(login.UserID, now)
	logger.Info("morning prompt enqueued",
		zap.String("user_id", login.UserID),
		zap.String("scheduled_for", now.Format("2006-01-02 15:04")))
	return nil
}

func (j *MorningJob) shouldSend(userID string, now time.Time) bool {
	if now.Hour() < morningStartHour || now.Hour() >= morningEndHour {
		return false
	}
	date := now.Format("2006-01-02")

	j.mu.Lock()
	defer j.mu.Unlock()
	return j.sent[userID] != date
}

func (j *MorningJob) markSent(userID string, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sent[userID] = now.Format("2006-01-02")
}

func (j *MorningJob) userLocation(ctx context.Context, userID string) *time.Location {
	tz := j.habit.UserTimezone(ctx, userID)
	loc, err := time.LoadLocation(tz)
	if err == nil {
		return loc
	}
	fallback, err := time.LoadLocation(db.DefaultTimezone)
	if err != nil {
		return time.UTC
	}
	return fallback
}
