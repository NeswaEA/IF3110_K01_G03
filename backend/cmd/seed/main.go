package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Seed completed.")
}

func run(ctx context.Context, args []string, stdin io.Reader) error {
	if len(args) == 0 || (args[0] != "admin" && args[0] != "fixtures") || len(args) > 2 || (len(args) == 2 && (args[0] != "admin" || args[1] != "--password-stdin")) {
		return errors.New("usage: seed admin [--password-stdin] | seed fixtures")
	}
	if args[0] == "fixtures" && os.Getenv("APP_ENV") != "development" && os.Getenv("APP_ENV") != "test" {
		return errors.New("fixtures require APP_ENV=development or test")
	}
	db, err := platform.OpenMetadataDB(ctx, os.Getenv("METADATA_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("could not begin seed transaction")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1397048644)`); err != nil {
		return errors.New("could not lock seed transaction")
	}
	if args[0] == "admin" {
		err = seedAdmin(ctx, tx, len(args) == 2, stdin)
	} else {
		err = seedFixtures(ctx, tx)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.New("seed commit could not be confirmed; inspect metadata before retrying")
	}
	return nil
}

func seedAdmin(ctx context.Context, tx *sql.Tx, useStdin bool, stdin io.Reader) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE active AND is_platform_admin)`).Scan(&exists); err != nil {
		return errors.New("could not inspect users; run metadata migrations first")
	}
	if exists {
		return nil
	}
	username, err := auth.NormalizeUsername(os.Getenv("BOOTSTRAP_ADMIN_USERNAME"))
	if err != nil {
		return errors.New("BOOTSTRAP_ADMIN_USERNAME must be 3-64 ASCII letters, digits, underscores, dots, or hyphens")
	}
	password := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	if useStdin {
		raw, err := io.ReadAll(io.LimitReader(stdin, 1027))
		if err != nil || len(raw) > 1026 {
			return errors.New("could not read a valid password from stdin")
		}
		password = string(raw)
		if strings.HasSuffix(password, "\n") {
			password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
		}
	}
	hash, err := (auth.PasswordHasher{}).Hash(ctx, password)
	if err != nil {
		return errors.New("admin password must be 12-128 characters and at most 1024 bytes; hashing must be available")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES(gen_random_uuid(),$1,$2,true)`, username, hash)
	if err != nil {
		return errors.New("could not create admin; ensure the username is not already in use")
	}
	return nil
}
