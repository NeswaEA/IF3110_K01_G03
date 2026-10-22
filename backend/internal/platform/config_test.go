package platform_test

import (
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func configEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("APP_ORIGIN", "http://localhost:8080")
	t.Setenv("METADATA_DATABASE_URL", "postgres://user:secret@localhost/metadata")
	for _, key := range []string{"SESSION_IDLE_TIMEOUT", "SESSION_ABSOLUTE_TIMEOUT", "SESSION_ROTATION_INTERVAL", "SESSION_GRACE_PERIOD", "COOKIE_SECURE"} {
		t.Setenv(key, "")
	}
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
	if cfg.SessionIdleTimeout != 30*time.Minute || cfg.SessionAbsoluteTimeout != 8*time.Hour || cfg.SessionRotationInterval != 15*time.Minute || cfg.SessionGracePeriod != 30*time.Second || cfg.CookieSecure {
		t.Fatal("incorrect session defaults")
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
		{"invalid idle", "SESSION_IDLE_TIMEOUT", "secret"},
		{"zero absolute", "SESSION_ABSOLUTE_TIMEOUT", "0s"},
		{"negative rotation", "SESSION_ROTATION_INTERVAL", "-1m"},
		{"fractional grace", "SESSION_GRACE_PERIOD", "1ns"},
		{"invalid secure", "COOKIE_SECURE", "secret"},
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

func TestSessionConfigOverridesAndSecureCookie(t *testing.T) {
	for _, tc := range []struct {
		env, origin, secure string
		want                bool
	}{
		{"development", "http://localhost:8080", "false", false},
		{"development", "http://localhost:8080", "true", true},
		{"development", "https://example.org", "false", true},
		{"production", "http://localhost:8080", "false", true},
	} {
		t.Run(tc.env+tc.origin+tc.secure, func(t *testing.T) {
			configEnv(t)
			t.Setenv("APP_ENV", tc.env)
			t.Setenv("APP_ORIGIN", tc.origin)
			t.Setenv("COOKIE_SECURE", tc.secure)
			t.Setenv("SESSION_IDLE_TIMEOUT", "20m")
			t.Setenv("SESSION_ABSOLUTE_TIMEOUT", "4h")
			t.Setenv("SESSION_ROTATION_INTERVAL", "5m")
			t.Setenv("SESSION_GRACE_PERIOD", "10s")
			cfg, err := platform.LoadConfig()
			if err != nil || cfg.CookieSecure != tc.want || cfg.SessionIdleTimeout != 20*time.Minute || cfg.SessionAbsoluteTimeout != 4*time.Hour || cfg.SessionRotationInterval != 5*time.Minute || cfg.SessionGracePeriod != 10*time.Second {
				t.Fatal("incorrect session configuration")
			}
		})
	}
}
