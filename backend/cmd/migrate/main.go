package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Metadata migrations are up to date.")
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "up" {
		return errors.New("usage: migrate up")
	}
	db, err := platform.OpenMetadataDB(ctx, os.Getenv("METADATA_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	return platform.MigrateMetadata(ctx, db, metadata.Files)
}
