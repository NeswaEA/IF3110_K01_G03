package main

import (
	"context"
	"log"
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	db, err := platform.OpenMetadataDB(context.Background(), cfg.MetadataDatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: http.NewServeMux(),
	}

	log.Printf("HTTP server listening on %s", server.Addr)
	return server.ListenAndServe()
}
