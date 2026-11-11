package main

import (
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	base := map[string]string{"METADATA_DATABASE_URL": "postgres://x@localhost/m", "NATS_URL": "nats://localhost:4222", "SQLITE_TARGET_DIR": dir}
	set := func(extra map[string]string) {
		for _, k := range []string{"METADATA_DATABASE_URL", "NATS_URL", "SQLITE_TARGET_DIR", "WORKER_ID", "WORKER_CONCURRENCY", "JOB_ACK_WAIT",
			"MIGRATION_RETRY_BASE_DELAY", "MIGRATION_RETRY_MAX_DELAY", "MIGRATION_MAX_ATTEMPTS"} {
			t.Setenv(k, "")
		}
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range extra {
			t.Setenv(k, v)
		}
	}

	set(nil)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 4 || cfg.MaxAttempts != 3 || cfg.AckWait != time.Minute || cfg.RetryBaseDelay != 2*time.Second ||
		cfg.RetryMaxDelay != 30*time.Second || cfg.OrderingDelay != time.Second || cfg.WorkerID == "" {
		t.Fatalf("defaults: %+v", cfg)
	}
	set(map[string]string{"WORKER_ID": "w1", "WORKER_CONCURRENCY": "2", "JOB_ACK_WAIT": "10s"})
	if cfg, err = loadConfig(); err != nil || cfg.WorkerID != "w1" || cfg.Concurrency != 2 || cfg.AckWait != 10*time.Second {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
	for _, bad := range []map[string]string{
		{"METADATA_DATABASE_URL": " "}, {"NATS_URL": ""}, {"SQLITE_TARGET_DIR": "relative"},
		{"WORKER_CONCURRENCY": "0"}, {"JOB_ACK_WAIT": "1s"}, {"MIGRATION_MAX_ATTEMPTS": "x"},
		{"MIGRATION_RETRY_BASE_DELAY": "1m", "MIGRATION_RETRY_MAX_DELAY": "30s"},
	} {
		set(bad)
		if _, err := loadConfig(); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
