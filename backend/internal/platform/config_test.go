package platform_test

import (
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func configEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("APP_ORIGIN", "http://localhost:8080")
	t.Setenv("METADATA_DATABASE_URL", "postgres://user:secret@localhost/metadata")
}

func TestConfigDefaults(t *testing.T) {
	configEnv(t)
	cfg, err := platform.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppEnv != "development" || cfg.HTTPAddr != ":8080" || cfg.AppOrigin != "http://localhost:8080" || cfg.MetadataDatabaseURL != "postgres://user:secret@localhost/metadata" {
		t.Fatal("incorrect defaults or supplied values")
	}
}

func TestConfigOverrides(t *testing.T) {
	configEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("HTTP_ADDR", "[::1]:9000")
	t.Setenv("APP_ORIGIN", "https://example.org/")
	cfg, err := platform.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppEnv != "production" || cfg.HTTPAddr != "[::1]:9000" || cfg.AppOrigin != "https://example.org" {
		t.Fatal("incorrect overrides or origin normalization")
	}
}

func TestConfigRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct{ name, key, value string }{
		{"environment", "APP_ENV", "secret"},
		{"address", "HTTP_ADDR", "localhost"},
		{"zero port", "HTTP_ADDR", ":0"},
		{"large port", "HTTP_ADDR", ":65536"},
		{"signed port", "HTTP_ADDR", ":+8080"},
		{"service port", "HTTP_ADDR", ":http"},
		{"missing origin", "APP_ORIGIN", ""},
		{"origin scheme", "APP_ORIGIN", "ftp://example.org"},
		{"origin credentials", "APP_ORIGIN", "http://user:secret@example.org"},
		{"origin path", "APP_ORIGIN", "http://example.org/login"},
		{"origin query", "APP_ORIGIN", "http://example.org?password=secret"},
		{"origin empty query", "APP_ORIGIN", "http://example.org?"},
		{"origin fragment", "APP_ORIGIN", "http://example.org#secret"},
		{"origin port", "APP_ORIGIN", "http://example.org:65536"},
		{"origin malformed", "APP_ORIGIN", "http://example.org:%secret"},
		{"missing metadata", "METADATA_DATABASE_URL", ""},
		{"blank metadata", "METADATA_DATABASE_URL", "  "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configEnv(t)
			t.Setenv(tt.key, tt.value)
			cfg, err := platform.LoadConfig()
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected validation error for %s", tt.key)
			}
			if cfg != (platform.Config{}) || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid configuration or input leaked")
			}
		})
	}
}
