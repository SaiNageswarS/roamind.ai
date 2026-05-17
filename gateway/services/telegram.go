package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/roamind.ai/gateway/db"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Telegram-related constants.
const (
	telegramAPIBase     = "https://api.telegram.org"
	telegramPollSeconds = 30
	telegramChannelName = "telegram"
)

// TelegramService bridges Telegram (via long-poll Bot API) and the Redis
// task streams. Each inbound message becomes a TaskIn with
// channel="telegram"; outbound TaskOut messages are delivered through
// sendMessage using a chat_id resolved from the login collection.
type TelegramService struct {
	rdb    *redis.Client
	mongo  odm.MongoClient
	dbName string
	logins odm.OdmCollectionInterface[db.LoginModel]
	token  string
	http   *http.Client
	offset atomic.Int64
}

// NewTelegramService returns nil when Telegram is disabled: missing
// TELEGRAM_BOT_TOKEN or no MongoDB client provided.
func NewTelegramService(rdb *redis.Client, mongoClient odm.MongoClient, dbName string, dispatcher *EgressDispatcher) *TelegramService {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		logger.Info("Telegram disabled: TELEGRAM_BOT_TOKEN not set")
		return nil
	}
	if mongoClient == nil {
		logger.Info("Telegram disabled: no MongoDB client")
		return nil
	}

	svc := &TelegramService{
		rdb:    rdb,
		mongo:  mongoClient,
		dbName: dbName,
		logins: odm.CollectionOf[db.LoginModel](mongoClient, dbName),
		token:  token,
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

// resolveUserID looks up the login row by telegram.id; refreshes channel
// info on existing rows, creates the row + user_profiles seed on first
// contact.
func (s *TelegramService) resolveUserID(ctx context.Context, from *tgUser, chatID int64) (string, error) {
	existing, err := async.Await(s.logins.FindOne(ctx, bson.M{"telegram.id": from.ID}))
	if err == nil && existing != nil {
		// Refresh changing fields and persist.
		existing.Telegram = &db.TelegramChannel{
			ID:        from.ID,
			ChatID:    chatID,
			Username:  from.Username,
			FirstName: from.FirstName,
			LastName:  from.LastName,
		}
		if _, saveErr := async.Await(s.logins.Save(ctx, *existing)); saveErr != nil {
			logger.Error("login save failed", zap.Error(saveErr), zap.String("user_id", existing.UserID))
		}
		return existing.UserID, nil
	}
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return "", fmt.Errorf("find login: %w", err)
	}

	// First contact: insert login + seed user_profiles.
	newUserID := uuid.NewString()
	login := db.LoginModel{
		UserID: newUserID,
		Telegram: &db.TelegramChannel{
			ID:        from.ID,
			ChatID:    chatID,
			Username:  from.Username,
			FirstName: from.FirstName,
			LastName:  from.LastName,
		},
	}
	if _, err := async.Await(s.logins.Save(ctx, login)); err != nil {
		return "", fmt.Errorf("insert login: %w", err)
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

	login, err := async.Await(s.logins.FindOneByID(ctx, userID))
	if err != nil {
		logger.Error("lookup login failed",
			zap.Error(err), zap.String("user_id", userID))
		return
	}
	if login == nil || login.Telegram == nil {
		logger.Info("no telegram channel for user", zap.String("user_id", userID))
		return
	}
	if err := s.sendMessage(ctx, login.Telegram.ChatID, text); err != nil {
		logger.Error("telegram sendMessage failed",
			zap.Error(err), zap.String("user_id", userID))
	}
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
