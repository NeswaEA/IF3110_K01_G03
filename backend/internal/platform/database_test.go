package platform_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func TestMetadataRejectsInvalidDSN(t *testing.T) {
	for _, dsn := range []string{"", " ", "postgres://user:secret@localhost:invalid/metadata"} {
		db, err := platform.OpenMetadataDB(context.Background(), dsn)
		if db != nil || err == nil {
			t.Fatal("expected no pool and a validation error")
		}
		if err.Error() != "METADATA_DATABASE_URL is required" && err.Error() != "METADATA_DATABASE_URL is invalid" {
			t.Fatal("unexpected or unsanitized error")
		}
	}
}

func TestMetadataCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := platform.OpenMetadataDB(ctx, "postgres://user:secret@localhost:5432/metadata?sslmode=disable")
	if db != nil || err == nil || err.Error() != "metadata database connection failed" {
		t.Fatal("expected sanitized connection failure and no pool")
	}
}

func TestMetadataConnectionIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := platform.OpenMetadataDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Stats().MaxOpenConnections != 5 {
		t.Fatal("unexpected pool limit")
	}
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatal("metadata query failed")
	}
	if err := db.Close(); err != nil {
		t.Fatal("pool close failed")
	}
	if db.PingContext(ctx) == nil {
		t.Fatal("closed pool accepted a query")
	}
}
