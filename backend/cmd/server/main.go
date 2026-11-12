package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

const service = "dbplatform-api"

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
	shutdownTelemetry, err := telemetry.Setup(ctx, service)
	if err != nil {
		slog.Error("telemetry setup failed", "event", "server.failed", "error", err.Error())
		os.Exit(1)
	}
	slog.SetDefault(platform.NewLogger(os.Stdout, level, telemetry.LogHandler(service)).With("service", service))
	err = run(ctx)
	flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = errors.Join(err, shutdownTelemetry(flush)); err != nil {
		slog.Error("server stopped", "event", "server.failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	runtime, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	db, err := platform.OpenDatabase(ctx, cfg.MetadataDatabaseURL, telemetry.PgxTracer("metadata", ""))
	if err != nil {
		return err
	}
	defer db.Close()
	defer telemetry.RegisterPool("metadata", db)()
	nc, err := nats.Connect(runtime.NATSURL, nats.Name(service), nats.MaxReconnects(-1))
	if err != nil {
		return errors.New("cannot connect to NATS_URL")
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	if err := jobs.EnsureStream(ctx, js); err != nil {
		return err
	}
	relay := jobs.NewRelay(db, js, jobs.RelayOptions{PollInterval: runtime.OutboxPollInterval})
	runtime.NotifyJobs = relay.Notify
	handler, cleanup, err := newApp(ctx, db, cfg, runtime, jobs.NewEventBus(nc))
	if err != nil {
		return err
	}
	defer cleanup()
	server := platform.NewHTTPServer(cfg.HTTPAddr, handler)
	// Outermost, so the request log line already runs inside the request span.
	server.Handler = telemetry.HTTPHandler(server.Handler)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}

	slog.Info("HTTP server listening", "event", "server.listening", "addr", server.Addr)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return platform.Serve(gctx, server, listener) })
	return g.Wait()
}
