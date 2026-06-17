package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourteam/crawler-lite/internal/workerapp"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := workerapp.LoadConfig()
	if err != nil {
		log.Error("config load", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	a, err := workerapp.Build(ctx, cfg, log)
	if err != nil {
		log.Error("worker build", "err", err)
		os.Exit(1)
	}

	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("worker run", "err", err)
		os.Exit(1)
	}
}
