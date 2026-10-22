package main

import (
	"context"
	"log"
	"net"
	"os/signal"
	"syscall"

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
	runtime, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	handler, cleanup, err := newApp(db, cfg, runtime)
	if err != nil {
		return err
	}
	defer cleanup()
	server := platform.NewHTTPServer(cfg.HTTPAddr, handler)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}

	log.Printf("HTTP server listening on %s", server.Addr)
	return platform.Serve(ctx, server, listener)
}
