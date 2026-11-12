package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	MetadataDatabaseURL string `json:"-"`
	EncryptionKey       string `json:"-"`
	SQLiteDir           string
	NATSURL             string `json:"-"`
	WorkerID            string
	Concurrency         int
	MaxAttempts         int
	ConnectTimeout      time.Duration
	HeartbeatInterval   time.Duration
	AckWait             time.Duration
	LockRetryMaxDelay   time.Duration
	OrderingDelay       time.Duration
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	StatementTimeout    time.Duration
	LockTimeout         time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		MetadataDatabaseURL: os.Getenv("METADATA_DATABASE_URL"),
		EncryptionKey:       os.Getenv("CONNECTION_ENCRYPTION_KEY"),
		SQLiteDir:           os.Getenv("SQLITE_TARGET_DIR"),
		NATSURL:             os.Getenv("NATS_URL"),
		WorkerID:            os.Getenv("WORKER_ID"),
	}
	if strings.TrimSpace(cfg.MetadataDatabaseURL) == "" {
		return config{}, errors.New("METADATA_DATABASE_URL is required")
	}
	if cfg.NATSURL == "" {
		return config{}, errors.New("NATS_URL is required")
	}
	if !filepath.IsAbs(cfg.SQLiteDir) {
		return config{}, errors.New("SQLITE_TARGET_DIR must be an absolute directory")
	}
	if info, err := os.Stat(cfg.SQLiteDir); err != nil || !info.IsDir() {
		return config{}, errors.New("SQLITE_TARGET_DIR must exist")
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID, _ = os.Hostname()
	}
	for _, s := range []struct {
		name     string
		value    *int
		fallback int
	}{{"WORKER_CONCURRENCY", &cfg.Concurrency, 4}, {"MIGRATION_MAX_ATTEMPTS", &cfg.MaxAttempts, 3}} {
		*s.value = s.fallback
		if raw := os.Getenv(s.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 64 {
				return config{}, errors.New(s.name + " must be an integer from 1 to 64")
			}
			*s.value = n
		}
	}
	for _, s := range []struct {
		name     string
		value    *time.Duration
		fallback time.Duration
	}{
		{"TARGET_CONNECT_TIMEOUT", &cfg.ConnectTimeout, 5 * time.Second},
		{"WORKER_HEARTBEAT_INTERVAL", &cfg.HeartbeatInterval, 5 * time.Second},
		{"JOB_ACK_WAIT", &cfg.AckWait, 60 * time.Second},
		{"JOB_LOCK_RETRY_MAX_DELAY", &cfg.LockRetryMaxDelay, 30 * time.Second},
		{"ORDERING_DELAY", &cfg.OrderingDelay, time.Second},
		{"MIGRATION_RETRY_BASE_DELAY", &cfg.RetryBaseDelay, 2 * time.Second},
		{"MIGRATION_RETRY_MAX_DELAY", &cfg.RetryMaxDelay, 30 * time.Second},
		{"MIGRATION_STATEMENT_TIMEOUT", &cfg.StatementTimeout, 30 * time.Minute},
		{"MIGRATION_LOCK_TIMEOUT", &cfg.LockTimeout, 5 * time.Second},
	} {
		*s.value = s.fallback
		if raw := os.Getenv(s.name); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return config{}, errors.New(s.name + " must be a positive duration")
			}
			*s.value = d
		}
	}
	if cfg.AckWait < 3*time.Second {
		return config{}, errors.New("JOB_ACK_WAIT must be at least 3s")
	}
	if cfg.RetryBaseDelay > cfg.RetryMaxDelay {
		return config{}, errors.New("MIGRATION_RETRY_BASE_DELAY must not exceed MIGRATION_RETRY_MAX_DELAY")
	}
	return cfg, nil
}
