package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SaiNageswarS/go-api-boot/logger"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Telegram-related constants.
const (
	telegramAPIBase     = "https://api.telegram.org"
	telegramPollSeconds = 30
	telegramChannelName = "telegram"

	collTelegramUsers = "telegram_users"
	collUserProfiles  = "user_profiles"
)

// TelegramService bridges Telegram (via long-poll Bot API) and the Redis
// task streams. Each inbound message is converted into a TaskIn envelope
// with channel="telegram"; outbound TaskOut messages from the agent are
// delivered through sendMessage using a chat_id resolved from MongoDB.
//
// User mapping:
//   - collection telegram_users keyed by _id = telegram user id
//   - field user_id = roamind UUID; chat_id = latest chat id
//   - on first contact a minimal user_profiles document is also upserted
type TelegramService struct {
	rdb    *redis.Client
	db     *mongo.Database
	token  string
	http   *http.Client
	offset atomic.Int64
}

// NewTelegramService constructs the service. Returns nil if disabled:
// missing TELEGRAM_BOT_TOKEN or no MongoDB database provided.
func NewTelegramService(rdb *redis.Client, db *mongo.Database, dispatcher *EgressDispatcher) *TelegramService {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		logger.Info("Telegram disabled: TELEGRAM_BOT_TOKEN not set")
		return nil
	}
	if db == nil {
		logger.Info("Telegram disabled: no MongoDB database")
		return nil
	}

	svc := &TelegramService{
		rdb:   rdb,
		db:    db,
		token: token,
		http: &http.Client{
			Timeout: time.Duration(telegramPollSeconds+10) * time.Second,
		},
	}
	dispatcher.Register(telegramChannelName, svc.handleEgress)
	return svc
}

// Start launches the long-poll loop in a background goroutine.
func (s *TelegramService) Start(ctx context.Context) {
	go s.runLongPoll(ctx)
	logger.Info("Telegram long-poll started")
}

// --- internals ----------------------------------------------------------

// Telegram Bot API DTOs (only the fields we use).
type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message,omitempty"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	Text      string  `json:"text"`
	From      *tgUser `json:"from,omitempty"`
	Chat      *tgChat `json:"chat,omitempty"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgGetUpdatesResp struct {
	OK          bool       `json:"ok"`
	Result      []tgUpdate `json:"result"`
	Description string     `json:"description,omitempty"`
}

// telegramUserDoc is the storage shape in the telegram_users collection.
type telegramUserDoc struct {
	TelegramID int64     `bson:"_id"`
	UserID     string    `bson:"user_id"`
	ChatID     int64     `bson:"chat_id"`
	FirstName  string    `bson:"first_name,omitempty"`
	LastName   string    `bson:"last_name,omitempty"`
	Username   string    `bson:"username,omitempty"`
	UpdatedAt  time.Time `bson:"updated_at"`
}

func (s *TelegramService) runLongPoll(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := s.getUpdates(ctx)
		if err != nil {
			logger.Error("telegram getUpdates failed", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			s.handleUpdate(ctx, u)
			if u.UpdateID >= s.offset.Load() {
				s.offset.Store(u.UpdateID + 1)
			}
		}
	}
}

func (s *TelegramService) getUpdates(ctx context.Context) ([]tgUpdate, error) {
	u := fmt.Sprintf(
		"%s/bot%s/getUpdates?timeout=%d&offset=%d&allowed_updates=%%5B%%22message%%22%%5D",
		telegramAPIBase, s.token, telegramPollSeconds, s.offset.Load(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var parsed tgGetUpdatesResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode getUpdates: %w (body=%s)", err, string(body))
	}
	if !parsed.OK {
		return nil, fmt.Errorf("getUpdates not ok: %s", parsed.Description)
	}
	return parsed.Result, nil
}

func (s *TelegramService) handleUpdate(ctx context.Context, u tgUpdate) {
	if u.Message == nil || u.Message.From == nil || u.Message.Chat == nil {
		return
	}
	if strings.TrimSpace(u.Message.Text) == "" {
		return
	}

	from := u.Message.From
	userID, err := s.resolveUserID(ctx, from, u.Message.Chat.ID)
	if err != nil {
		logger.Error("resolve telegram user failed",
			zap.Error(err), zap.Int64("tg_id", from.ID))
		return
	}

	internalID := uuid.NewString()
	taskIn := &pb.TaskIn{
		Id:           internalID,
		TraceId:      internalID,
		UserId:       userID,
		Channel:      telegramChannelName,
		ChannelMsgId: strconv.FormatInt(u.Message.MessageID, 10),
		Text:         u.Message.Text,
		ReceivedAt:   timestamppb.Now(),
	}
	if _, err := XAddTaskIn(ctx, s.rdb, taskIn); err != nil {
		logger.Error("telegram xadd tasks.in failed",
			zap.Error(err), zap.String("user_id", userID))
		return
	}
	logger.Info("Telegram message enqueued",
		zap.String("id", internalID),
		zap.String("user_id", userID),
		zap.Int64("tg_id", from.ID))
}

// resolveUserID returns the roamind UUID for a Telegram user, creating
// new mapping + user_profiles entries on first contact and refreshing
// chat_id / names on each call.
func (s *TelegramService) resolveUserID(ctx context.Context, from *tgUser, chatID int64) (string, error) {
	coll := s.db.Collection(collTelegramUsers)
	now := time.Now().UTC()

	var existing telegramUserDoc
	err := coll.FindOne(ctx, bson.M{"_id": from.ID}).Decode(&existing)
	if err == nil {
		// Update changing fields (chat id can change, names can be edited).
		_, _ = coll.UpdateOne(ctx, bson.M{"_id": from.ID}, bson.M{
			"$set": bson.M{
				"chat_id":    chatID,
				"first_name": from.FirstName,
				"last_name":  from.LastName,
				"username":   from.Username,
				"updated_at": now,
			},
		})
		return existing.UserID, nil
	}
	if err != mongo.ErrNoDocuments {
		return "", fmt.Errorf("find telegram_users: %w", err)
	}

	// First contact: create both telegram_users and user_profiles.
	newUserID := uuid.NewString()
	doc := telegramUserDoc{
		TelegramID: from.ID,
		UserID:     newUserID,
		ChatID:     chatID,
		FirstName:  from.FirstName,
		LastName:   from.LastName,
		Username:   from.Username,
		UpdatedAt:  now,
	}
	if _, err := coll.InsertOne(ctx, doc); err != nil {
		return "", fmt.Errorf("insert telegram_users: %w", err)
	}

	name := strings.TrimSpace(from.FirstName + " " + from.LastName)
	if name == "" {
		name = from.Username
	}
	profiles := s.db.Collection(collUserProfiles)
	_, err = profiles.UpdateOne(ctx,
		bson.M{"user_id": newUserID},
		bson.M{
			"$setOnInsert": bson.M{
				"user_id":    newUserID,
				"name":       name,
				"updated_at": now,
				"extras": bson.M{
					"telegram_id":       from.ID,
					"telegram_username": from.Username,
				},
			},
		},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		logger.Error("upsert user_profiles failed",
			zap.Error(err), zap.String("user_id", newUserID))
	}
	return newUserID, nil
}

// handleEgress is the dispatcher callback for the "telegram" channel.
func (s *TelegramService) handleEgress(ctx context.Context, taskOut *pb.TaskOut) {
	userID := taskOut.GetUserId()
	if userID == "" {
		logger.Info("telegram egress missing user_id")
		return
	}
	text := taskOut.GetPayload()
	if text == "" {
		return
	}

	chatID, err := s.lookupChatID(ctx, userID)
	if err != nil {
		logger.Error("lookup telegram chat_id failed",
			zap.Error(err), zap.String("user_id", userID))
		return
	}
	if err := s.sendMessage(ctx, chatID, text); err != nil {
		logger.Error("telegram sendMessage failed",
			zap.Error(err), zap.String("user_id", userID))
	}
}

func (s *TelegramService) lookupChatID(ctx context.Context, userID string) (int64, error) {
	var doc telegramUserDoc
	err := s.db.Collection(collTelegramUsers).
		FindOne(ctx, bson.M{"user_id": userID}).Decode(&doc)
	if err != nil {
		return 0, err
	}
	return doc.ChatID, nil
}

func (s *TelegramService) sendMessage(ctx context.Context, chatID int64, text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, s.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendMessage status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
