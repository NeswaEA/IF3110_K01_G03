package main

import (
	"context"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
)

type diffRegistry interface {
	database.TargetProvider
	database.DiffConfigProvider
}

func diffGenerators(ctx context.Context, registry diffRegistry, cfg runtimeConfig) (map[database.Driver]schemadiff.Generator, map[database.Driver]bool) {
	atlas := schemadiff.AtlasGenerator{Config: registry, Binary: cfg.AtlasBin, Timeout: cfg.DiffTimeout}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	available := atlas.Check(probeCtx) == nil
	return map[database.Driver]schemadiff.Generator{database.Postgres: atlas, database.SQLite: schemadiff.SQLiteGenerator{Provider: registry}}, map[database.Driver]bool{database.Postgres: available, database.SQLite: true}
}
