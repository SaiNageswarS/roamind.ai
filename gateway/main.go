package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SaiNageswarS/go-api-boot/dotenv"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/gateway/services"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/zap"
)

func main() {
	dotenv.LoadEnv()

	ctx := getCancellableContext()

	rdb, err := newRedisClient()
	if err != nil {
		logger.Fatal("redis init failed", zap.Error(err))
	}

	// MongoDB is optional at the gateway level. Telegram requires it; CLI
	// does not. Failure here is logged but non-fatal.
	mongoDB := newMongoDB(ctx)

	dispatcher := services.NewEgressDispatcher(rdb)

	boot, err := server.New().
		GRPCPort(":50051").
		HTTPPort(":8081").
		Provide(rdb).
		Provide(dispatcher).
		RegisterService(
			server.Adapt(pb.RegisterAssistantCLIServer),
			services.NewCliService,
		).
		Build()
	if err != nil {
		logger.Fatal("Dependency Injection Failed", zap.Error(err))
	}

	// Start optional Telegram bridge if configured.
	if tg := services.NewTelegramService(rdb, mongoDB, dispatcher); tg != nil {
		tg.Start(ctx)
	}

	// Single dispatcher consumes tasks.out and fans out to channels.
	dispatcher.Start(ctx)

	if err := boot.Serve(ctx); err != nil {
		logger.Error("server exited", zap.Error(err))
	}
}

func newRedisClient() (*redis.Client, error) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	logger.Info("Connected to Redis", zap.String("url", url))
	return rdb, nil
}

// newMongoDB connects using MONGO_URI; returns nil when unconfigured or on
// error (Telegram bridge will be disabled in that case).
func newMongoDB(ctx context.Context) *mongo.Database {
	uri := strings.TrimSpace(os.Getenv("MONGO_URI"))
	if uri == "" {
		logger.Info("MongoDB disabled: MONGO_URI not set")
		return nil
	}
	dbName := strings.TrimSpace(os.Getenv("MONGO_DB"))
	if dbName == "" {
		dbName = "roamind"
	}

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		logger.Error("mongo connect failed", zap.Error(err))
		return nil
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		logger.Error("mongo ping failed", zap.Error(err))
		return nil
	}
	logger.Info("Connected to MongoDB", zap.String("db", dbName))
	return client.Database(dbName)
}

func getCancellableContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sig
		cancel()
	}()

	return ctx
}
