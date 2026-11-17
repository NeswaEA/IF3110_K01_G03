package platform

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// OpenDatabase opens the metadata pool; tracer (nil in CLIs) records every statement.
func OpenDatabase(ctx context.Context, dsn string, tracer pgx.QueryTracer) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("METADATA_DATABASE_URL is required")
	}
	conns := 10
	if raw := os.Getenv("METADATA_MAX_OPEN_CONNS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return nil, errors.New("METADATA_MAX_OPEN_CONNS must be an integer from 1 to 100")
		}
		conns = n
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("METADATA_DATABASE_URL is invalid")
	}
	cfg.ConnectTimeout = 5 * time.Second
	cfg.Tracer = tracer
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("metadata database connection failed")
	}
	return db, nil
}
