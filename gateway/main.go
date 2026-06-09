package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SaiNageswarS/go-api-boot/dotenv"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/gateway/db"
	"github.com/SaiNageswarS/roamind.ai/gateway/jobs"
	"github.com/SaiNageswarS/roamind.ai/gateway/services"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const defaultMongoDB = "roamind"

func main() {
	dotenv.LoadEnv()

	ctx := getCancellableContext()

	rdb, err := newRedisClient()
	if err != nil {
		logger.Fatal("redis init failed", zap.Error(err))
	}

	mongoClient := odm.ProvideMongoClient()

	if mongoClient != nil {
		if err := db.EnsureHabitIndexes(ctx, mongoClient, defaultMongoDB); err != nil {
			logger.Fatal("ensure habit indexes failed", zap.Error(err))
		}
	}

	profileRepo := db.NewProfileRepo(mongoClient, defaultMongoDB)
	habitService := services.NewHabitService(mongoClient, defaultMongoDB, profileRepo)
	profileService := services.NewProfileService(profileRepo)
	rollupEngine := jobs.NewRollupEngine(habitService)
	logins := odm.CollectionOf[db.LoginModel](mongoClient, defaultMongoDB)
	morningJob := jobs.NewMorningJob(rdb, logins, habitService, profileRepo)

	dispatcher := services.NewEgressDispatcher(rdb)

	boot, err := server.New().
		HTTPPort(":8081").
		Provide(rdb).
		Provide(dispatcher).
		Provide(habitService).
		Provide(profileService).
		Build()
	if err != nil {
		logger.Fatal("Dependency Injection Failed", zap.Error(err))
	}

	if tg := services.NewTelegramService(rdb, mongoClient, defaultMongoDB, dispatcher, habitService, profileService); tg != nil {
		tg.Start(ctx)
	}

	if morningJob != nil {
		morningJob.Start(ctx)
	}
	rollupEngine.Start(ctx)

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

	const maxAttempts = 10
	const retryDelay = 2 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = rdb.Ping(context.Background()).Err()
		if err == nil {
			logger.Info("Connected to Redis",
				zap.String("url", url),
				zap.Int("attempt", attempt),
			)
			return rdb, nil
		}

		if attempt == maxAttempts {
			break
		}

		logger.Info("Redis not ready yet, retrying",
			zap.String("url", url),
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", maxAttempts),
			zap.Duration("retry_delay", retryDelay),
			zap.Error(err),
		)

		time.Sleep(retryDelay)
	}

	return nil, err
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
