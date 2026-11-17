package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// target SQL is traced; the platform logger keeps only the parameter types
	slog.SetDefault(platform.NewLogger(os.Stderr, slog.LevelInfo, nil).With("service", "dbplatform-seed"))
	if err := run(ctx, os.Args[1:], os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Seed completed.")
}

func run(ctx context.Context, args []string, stdin io.Reader) error {
	const usage = "usage: seed admin [--password-stdin] | seed fixtures | seed load | seed bigdata [--rows N]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	var rows int64
	switch args[0] {
	case "admin":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--password-stdin") {
			return errors.New(usage)
		}
	case "fixtures", "load":
		if len(args) != 1 {
			return errors.New(usage)
		}
	case "bigdata":
		var err error
		rows, err = bigDataRows(args[1:])
		if err != nil {
			return err
		}
	default:
		return errors.New(usage)
	}
	if args[0] != "admin" && os.Getenv("APP_ENV") != "development" && os.Getenv("APP_ENV") != "test" {
		return errors.New(args[0] + " requires APP_ENV=development or test")
	}
	db, err := platform.OpenDatabase(ctx, os.Getenv("METADATA_DATABASE_URL"), nil)
	if err != nil {
		return err
	}
	defer db.Close()
	if args[0] == "bigdata" {
		return seedBigData(ctx, db, rows)
	}
	if args[0] == "load" {
		return seedLoad(ctx, db)
	}
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
