package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	buf.Reset()
	return line
}

func TestLoggerRedactsSecretsAndParameterValues(t *testing.T) {
	var buf, extraBuf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, slog.NewJSONHandler(&extraBuf, nil))
	logger.With("dsn", "postgres://u:p@h/db").Info("sql executed",
		"password", "hunter2", "X-CSRF-Token", "abc", "session_id", "s1",
		slog.Group("request", "Authorization", "Bearer x", "path", "/api/v1/databases"),
		"db.params", []any{int64(1), "secret value", nil},
		"event", "db.query")
	for _, out := range []string{buf.String(), extraBuf.String()} {
		for _, leaked := range []string{"postgres://", "hunter2", `"abc"`, "s1", "Bearer", "secret value"} {
			if strings.Contains(out, leaked) {
				t.Fatalf("%q leaked in %s", leaked, out)
			}
		}
	}
	line := logLine(t, &buf)
	if line["password"] != "[REDACTED]" || line["dsn"] != "[REDACTED]" || line["event"] != "db.query" {
		t.Fatal(line)
	}
	if request := line["request"].(map[string]any); request["Authorization"] != "[REDACTED]" || request["path"] != "/api/v1/databases" {
		t.Fatal(request)
	}
	if params := fmt.Sprint(line["db.params"]); params != "[<int64> <string> <nil>]" {
		t.Fatal(params)
	}
}

func TestLoggerAddsContextCorrelationOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, nil)
	var ctx context.Context
	RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	span := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})
	ctx = trace.ContextWithSpanContext(WithActorID(ctx, "10000000-0000-4000-8000-000000000003"), span)

	logger.InfoContext(ctx, "request done", "request_id", "explicit")
	line := logLine(t, &buf)
	if line["request_id"] != "explicit" || line["actor_id"] != "10000000-0000-4000-8000-000000000003" ||
		line["trace_id"] != span.TraceID().String() || line["span_id"] != span.SpanID().String() {
		t.Fatal(line)
	}
	logger.InfoContext(ctx, "again")
	if line := logLine(t, &buf); line["request_id"] != RequestIDFromContext(ctx) || line["request_id"] == "" {
		t.Fatal(line)
	}
	logger.Info("no context")
	if line := logLine(t, &buf); line["trace_id"] != nil || line["actor_id"] != nil {
		t.Fatal(line)
	}
	logger.Debug("below level")
	if buf.Len() != 0 {
		t.Fatal("debug written at info level")
	}
}
