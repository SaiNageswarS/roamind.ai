package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/SaiNageswarS/go-api-boot/dotenv"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/gateway/services"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func main() {
	dotenv.LoadEnv()

	rdb, err := newRedisClient()
	if err != nil {
		logger.Fatal("redis init failed", zap.Error(err))
	}

	boot, err := server.New().
		GRPCPort(":50051").
		HTTPPort(":8081").
		Provide(rdb).
		RegisterService(
			server.Adapt(pb.RegisterAssistantCLIServer),
			services.NewCliService,
		).
		Build()

	if err != nil {
		logger.Fatal("Dependency Injection Failed", zap.Error(err))
	}

	ctx := getCancellableContext()
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
