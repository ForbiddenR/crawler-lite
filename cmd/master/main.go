package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/bcrypt"

	"github.com/yourteam/crawler-lite/internal/app"
)

func main() {
	// Allow `master hash-password <pw>` as a tiny utility subcommand.
	if len(os.Args) >= 2 && os.Args[1] == "hash-password" {
		hashPasswordCmd(os.Args[2:])
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := app.LoadConfig()
	if err != nil {
		log.Error("config load", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	a, err := app.Build(ctx, cfg, log)
	if err != nil {
		log.Error("app build", "err", err)
		os.Exit(1)
	}

	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("app run", "err", err)
		os.Exit(1)
	}
}

func hashPasswordCmd(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: master hash-password <password>")
		os.Exit(2)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(args[0]), 10)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bcrypt:", err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}
