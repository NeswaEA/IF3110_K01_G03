// Command worker runs migration jobs from the JetStream queue: it holds the
// per-target migration lock, executes, and reports progress as events.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

const service = "dbplatform-worker"

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
		slog.Error("telemetry setup failed", "event", "worker.failed", "error", err.Error())
		os.Exit(1)
	}
	slog.SetDefault(platform.NewLogger(os.Stdout, level, telemetry.LogHandler(service)).With("service", service))
	err = run(ctx)
	flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = errors.Join(err, shutdownTelemetry(flush))
	if err != nil {
		slog.Error("worker stopped", "event", "worker.failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cipher, err := connections.NewCipher(cfg.EncryptionKey)
	if err != nil {
		return errors.New("CONNECTION_ENCRYPTION_KEY must encode 32 bytes in base64")
	}
	db, err := platform.OpenMetadataDB(ctx, cfg.MetadataDatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	defer telemetry.RegisterPool("metadata", db)()
	registry := connections.NewRegistry(db, cipher, connections.RegistryOptions{ConnectTimeout: cfg.ConnectTimeout, SQLiteDir: cfg.SQLiteDir})
	defer registry.Close()
	lockers := map[database.Driver]database.MigrationLocker{database.Postgres: postgres.Locker{}, database.SQLite: sqlite.Locker{Metadata: db}}

	nc, err := nats.Connect(cfg.NATSURL, nats.Name(service+"/"+cfg.WorkerID), nats.MaxReconnects(-1))
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
	events := jobs.NewEventBus(nc)
	handler, stale := newExecutor(registry, lockers, events, cfg)

	slog.InfoContext(ctx, "worker started", "event", "worker.started", "worker_id", cfg.WorkerID, "concurrency", cfg.Concurrency)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return jobs.NewConsumer(js, handler, jobs.ConsumerOptions{Concurrency: cfg.Concurrency, AckWait: cfg.AckWait}).Run(gctx)
	})
	if stale != nil {
		g.Go(func() error {
			return jobs.NewSweeper(stale, jobs.NewOutbox(db), db, jobs.SweeperOptions{OlderThan: 2 * cfg.AckWait}).Run(gctx)
		})
	}
	err = g.Wait()
	slog.Info("worker stopped", "event", "worker.stopped", "worker_id", cfg.WorkerID)
	return err
}

// newExecutor builds the job handler and the stale-job source.
// shortcut: until the migrations executor exists every job is left in the
// queue (Nak, retried later) so no job is lost or run; replace the body with
// migrations.NewExecutor wired to these dependencies once it lands.
func newExecutor(_ database.TargetProvider, _ map[database.Driver]database.MigrationLocker, _ contracts.EventPublisher, cfg config) (contracts.JobHandler, contracts.StaleJobSource) {
	return waitingHandler{delay: cfg.LockRetryMaxDelay}, nil
}

type waitingHandler struct{ delay time.Duration }

func (h waitingHandler) Handle(ctx context.Context, m contracts.JobMessage) contracts.JobResult {
	slog.WarnContext(ctx, "no executor wired; job left in the queue", "event", "job.deferred", "execution_id", m.ExecutionID)
	return contracts.JobResult{Delay: h.delay, Reason: "retry"}
}
