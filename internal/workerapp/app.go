// Package workerapp is the worker process composition root. The worker is
// just one long-lived gRPC client that opens a stream to the master, sends
// Hello, and processes assignments.
//
// Week 1 prints assignments and ACKs them as ACCEPTED → RUNNING → SUCCEEDED
// after a 2s delay so the master sees the lifecycle. Real Python execution
// arrives in week 2.
package workerapp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yourteam/crawler-lite/internal/runner"
)

type App struct {
	cfg    Config
	log    *slog.Logger
	worker *runner.Worker
}

func Build(_ context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if cfg.WorkerID == "" {
		return nil, fmt.Errorf("WORKER_ID is required")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	w := runner.NewWorker(runner.Config{
		MasterAddr:   cfg.MasterGRPCAddr,
		WorkerID:     cfg.WorkerID,
		Concurrency:  cfg.Concurrency,
		Capabilities: cfg.Capabilities(),
		SharedSecret: cfg.WorkerSharedSecret,
	}, log)
	return &App{cfg: cfg, log: log, worker: w}, nil
}

func (a *App) Run(ctx context.Context) error {
	a.log.Info("worker starting",
		"worker_id", a.cfg.WorkerID,
		"master", a.cfg.MasterGRPCAddr,
		"concurrency", a.cfg.Concurrency,
	)
	return a.worker.Run(ctx)
}
