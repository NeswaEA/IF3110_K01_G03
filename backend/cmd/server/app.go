package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/explain"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/viewer"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

type runtimeConfig struct {
	EncryptionKey    string `json:"-"`
	SQLiteDir        string
	WebDir           string
	ConnectTimeout   time.Duration
	QueryTimeout     time.Duration
	MigrationTimeout time.Duration
	DiffTimeout      time.Duration
	AtlasBin         string
}

func loadRuntimeConfig() (runtimeConfig, error) {
	cfg := runtimeConfig{EncryptionKey: os.Getenv("CONNECTION_ENCRYPTION_KEY"), SQLiteDir: os.Getenv("SQLITE_TARGET_DIR"), WebDir: os.Getenv("WEB_DIST_DIR"), AtlasBin: os.Getenv("ATLAS_BIN"), ConnectTimeout: 5 * time.Second, QueryTimeout: 10 * time.Second, MigrationTimeout: 30 * time.Second, DiffTimeout: 30 * time.Second}
	if !filepath.IsAbs(cfg.SQLiteDir) {
		return runtimeConfig{}, errors.New("SQLITE_TARGET_DIR must be an absolute directory")
	}
	info, err := os.Stat(cfg.SQLiteDir)
	if err != nil || !info.IsDir() {
		return runtimeConfig{}, errors.New("SQLITE_TARGET_DIR must exist")
	}
	if cfg.WebDir == "" {
		cfg.WebDir = "web/dist"
	}
	for _, setting := range []struct {
		name  string
		value *time.Duration
	}{{"TARGET_CONNECT_TIMEOUT", &cfg.ConnectTimeout}, {"TARGET_QUERY_TIMEOUT", &cfg.QueryTimeout}, {"MIGRATION_TIMEOUT", &cfg.MigrationTimeout}, {"DIFF_TIMEOUT", &cfg.DiffTimeout}} {
		if raw := os.Getenv(setting.name); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return runtimeConfig{}, errors.New(setting.name + " must be a positive duration")
			}
			*setting.value = value
		}
	}
	return cfg, nil
}

func newApp(ctx context.Context, db *sql.DB, cfg platform.Config, runtime runtimeConfig) (http.Handler, func(), error) {
	cipher, err := connections.NewCipher(runtime.EncryptionKey)
	if err != nil {
		return nil, nil, errors.New("CONNECTION_ENCRYPTION_KEY must encode 32 bytes in base64")
	}
	identity, err := connections.IdentityFromDSN(cfg.MetadataDatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := auth.NewSessionStore(db, platform.SystemClock{}, auth.SessionOptions{IdleTimeout: cfg.SessionIdleTimeout, AbsoluteTimeout: cfg.SessionAbsoluteTimeout, RotationInterval: cfg.SessionRotationInterval, GracePeriod: cfg.SessionGracePeriod})
	if err != nil {
		return nil, nil, err
	}
	authHandler, err := auth.NewHTTPHandler(sessions, cfg.AppOrigin, cfg.CookieSecure)
	if err != nil {
		return nil, nil, err
	}
	assets, err := webui.LoadAssets(runtime.WebDir)
	if err != nil {
		return nil, nil, err
	}
	renderer, err := webui.NewRenderer(assets)
	if err != nil {
		return nil, nil, err
	}
	frontend, closeFrontend, err := newFrontend(runtime.WebDir, http.HandlerFunc(renderer.NotFound))
	if err != nil {
		return nil, nil, err
	}
	registry := connections.NewRegistry(db, cipher, connections.RegistryOptions{ConnectTimeout: runtime.ConnectTimeout, SQLiteDir: runtime.SQLiteDir})
	cleanup := func() { registry.Close(); closeFrontend() }
	policy := access.NewPolicy(db)
	lifecycle := access.NewDatabaseAccessLifecycle(sessions)
	generators, diffFeatures := diffGenerators(ctx, registry, runtime)
	connectionService := connections.NewService(db, cipher, registry, migrations.LedgerInitializer{}, lifecycle, migrations.NewStore(db), connections.ServiceOptions{
		Policy: connections.Policy{AllowInsecureSSL: cfg.AppEnv != "production"}, SQLiteDir: runtime.SQLiteDir, MetadataIdentity: identity, QueryTimeout: runtime.QueryTimeout,
		Features: connections.Features{Runner: map[database.Driver]bool{database.Postgres: true, database.SQLite: true}, Diff: diffFeatures, Explain: map[database.Driver]bool{database.Postgres: true, database.SQLite: true}},
	})
	inspector := database.DispatchInspector{ByDriver: map[database.Driver]database.Inspector{database.Postgres: postgres.Inspector{}, database.SQLite: sqlite.Inspector{}}}
	migrationService := migrations.NewService(db, registry, inspector, policy, platform.SystemClock{}, migrations.Options{Timeout: runtime.MigrationTimeout})
	if err := migrationService.Recover(ctx); err != nil {
		cleanup()
		return nil, nil, err
	}
	diffService := schemadiff.NewService(registry, inspector, policy, func(ctx context.Context, p contracts.Principal, id string) (int64, error) {
		head, err := migrationService.Head(ctx, p, id)
		return head.Version, err
	}, schemadiff.Options{Timeout: runtime.DiffTimeout, Generators: generators})
	viewService := viewer.NewService(registry, inspector, map[database.Driver]viewer.TableReader{database.Postgres: postgres.Reader{}, database.SQLite: sqlite.Reader{}}, runtime.QueryTimeout)
	explainService := explain.NewService(registry, registry, map[database.Driver]explain.Planner{database.Postgres: postgres.Planner{}, database.SQLite: sqlite.Planner{}}, runtime.QueryTimeout)
	protected := http.NewServeMux()
	userService := access.NewUserService(db, sessions, policy, platform.SystemClock{}, auth.NormalizeUsername, (auth.PasswordHasher{}).Hash)
	groupService := access.NewGroupService(db, sessions, policy, platform.SystemClock{})
	access.RegisterUserRoutes(protected, access.NewUserHandler(userService, auth.PrincipalFromContext))
	access.RegisterGroupRoutes(protected, access.NewGroupHandler(groupService, auth.PrincipalFromContext))
	connections.RegisterRoutes(protected, connections.NewHandler(connectionService, policy, auth.PrincipalFromContext))
	viewer.RegisterRoutes(protected, viewer.NewHandler(viewService, policy, auth.PrincipalFromContext))
	explain.RegisterRoutes(protected, explain.NewHandler(explainService, policy, auth.PrincipalFromContext))
	migrations.RegisterRoutes(protected, migrations.NewHandler(migrationService, auth.PrincipalFromContext))
	schemadiff.RegisterRoutes(protected, schemadiff.NewHandler(diffService, auth.PrincipalFromContext))
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "NOT_FOUND", Message: "Endpoint tidak ditemukan."})
	})
	mux := http.NewServeMux()
	platform.RegisterHealthRoutes(mux, db)
	authHandler.RegisterRoutes(mux)
	secured := authHandler.RequireSession(authHandler.RequireCSRF(protected))
	mux.Handle("/api/", secured)
	mux.Handle("/api", secured)
	webui.RegisterRoutes(mux, webui.NewPages(renderer, policy, auth.PrincipalFromContext, auth.RedirectToLogin, connectionService, viewService, userService, groupService).WithMigrations(migrationService), authHandler.RequirePage)
	mux.Handle("/", frontend)
	return mux, cleanup, nil
}
