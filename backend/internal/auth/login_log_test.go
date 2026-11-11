package auth_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func TestLoginAttemptsAreLoggedWithoutCredentials(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(platform.NewLogger(&buf, slog.LevelInfo, nil))
	t.Cleanup(func() { slog.SetDefault(previous) })
	f := newAuthHTTPFixture(t)

	prelogin, csrf := f.csrf(t, nil)
	const typo = "example password 123 typed into the username"
	f.request("POST", "/api/v1/auth/login", prelogin, csrf, authOrigin, `{"username":"`+typo+`","password":"wrong password 456"}`)
	cookie, _ := f.login(t)

	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("not JSON: %q", raw)
		}
		if line["event"] == "auth.login" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 2 {
		t.Fatal("expected one line per attempt", lines)
	}
	if lines[0]["status"] != "failed" || lines[0]["error_code"] != "UNAUTHENTICATED" || lines[0]["level"] != "WARN" || lines[0]["actor_id"] != nil || lines[0]["request_id"] == nil {
		t.Fatal("incorrect failed attempt", lines[0])
	}
	if lines[1]["status"] != "ok" || lines[1]["actor_id"] != sessionUser || lines[1]["level"] != "INFO" {
		t.Fatal("incorrect successful attempt", lines[1])
	}
	for _, secret := range []string{"wrong password 456", authPassword, typo, prelogin.Value, cookie.Value, csrf} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("%q leaked into the log", secret)
		}
	}
}
