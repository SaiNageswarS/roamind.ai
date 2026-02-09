package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/SaiNageswarS/go-api-boot/config"
	"github.com/SaiNageswarS/go-api-boot/dotenv"
	"github.com/SaiNageswarS/go-api-boot/embed"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/memory/appconfig"
	"github.com/SaiNageswarS/roamind.ai/workflows/activities"
	"go.uber.org/zap"

	temporalClient "go.temporal.io/sdk/client"
)

func main() {
	dotenv.LoadEnv()

	ccfgg := &appconfig.AppConfig{}
	err := config.LoadConfig("config.ini", ccfgg)
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}

	boot, err := server.New().
		GRPCPort(":50051").
		HTTPPort(":8081").
		Provide(ccfgg).

		// embedding and mongo clients
		ProvideFunc(embed.ProvideJinaAIEmbeddingClient).
		ProvideFunc(odm.ProvideMongoClient).

		// temporal activities and workflows
		WithTemporal(ccfgg.TemporalGoTaskQueue, &temporalClient.Options{
			HostPort: ccfgg.TemporalHostPort,
		}).
		RegisterTemporalActivity(activities.ProvideActivities).
		RegisterTemporalWorkflow(SaveProfileCardWorkflow).
		Build()

	if err != nil {
		logger.Fatal("Dependency Injection Failed", zap.Error(err))
	}

	ctx := getCancellableContext()
	// catch SIGINT ‑> cancel
	_ = boot.Serve(ctx)
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
