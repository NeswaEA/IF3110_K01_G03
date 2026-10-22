package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	db, err := platform.OpenMetadataDB(ctx, cfg.MetadataDatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	sessions, err := auth.NewSessionStore(db, platform.SystemClock{}, auth.SessionOptions{
		IdleTimeout: cfg.SessionIdleTimeout, AbsoluteTimeout: cfg.SessionAbsoluteTimeout,
		RotationInterval: cfg.SessionRotationInterval, GracePeriod: cfg.SessionGracePeriod,
	})
	if err != nil {
		return err
	}
	authHandler, err := auth.NewHTTPHandler(sessions, cfg.AppOrigin, cfg.CookieSecure)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	platform.RegisterHealthRoutes(mux, db)
	authHandler.RegisterRoutes(mux)
	server := platform.NewHTTPServer(cfg.HTTPAddr, mux)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}

	log.Printf("HTTP server listening on %s", server.Addr)
	return platform.Serve(ctx, server, listener)
}
