package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/SaiNageswarS/go-api-boot/dotenv"
	"github.com/SaiNageswarS/go-api-boot/embed"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/mcp/controller"
	"github.com/SaiNageswarS/roamind.ai/mcp/handler"
	"go.uber.org/zap"
)

func main() {
	dotenv.LoadEnv()

	boot, err := server.New().
		GRPCPort(":50051").
		HTTPPort(":8081").

		// mongo and embedding clients
		ProvideFunc(odm.ProvideMongoClient).
		ProvideFunc(embed.ProvideJinaAIEmbeddingClient).

		// handlers
		ProvideFunc(handler.ProvideProfileCardSearchHandler).

		// controllers
		AddRestController(controller.ProvideProfileCardsController).
		Build()

	if err != nil {
		logger.Fatal("Dependency Injection Failed", zap.Error(err))
	}

	ctx := getCancellableContext()
	boot.Serve(ctx)
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
