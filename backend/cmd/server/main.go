package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	level := slog.LevelInfo
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		if err := level.UnmarshalText([]byte(raw)); err != nil {
			slog.Error("LOG_LEVEL must be debug, info, warn or error")
			os.Exit(1)
		}
	}
	slog.SetDefault(platform.NewLogger(os.Stdout, level, nil).With("service", "dbplatform-api"))
	if err := run(ctx); err != nil {
		slog.Error("server stopped", "event", "server.failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	db, err := platform.OpenDatabase(ctx, cfg.MetadataDatabaseURL, telemetry.PgxTracer("metadata", ""))
	if err != nil {
		return err
	}
	defer db.Close()
	runtime, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	handler, cleanup, err := newApp(ctx, db, cfg, runtime)
	if err != nil {
		return err
	}
	defer cleanup()
	server := platform.NewHTTPServer(cfg.HTTPAddr, handler)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}

	slog.Info("HTTP server listening", "event", "server.listening", "addr", server.Addr)
	return platform.Serve(ctx, server, listener)
}
