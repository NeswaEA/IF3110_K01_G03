package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverAnswersAPanicWithJSONAndLogsItSafely(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(NewLogger(&buf, slog.LevelInfo, nil))
	t.Cleanup(func() { slog.SetDefault(previous) })

	server := NewHTTPServer(":0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("password=hunter2 for postgres://u:p@h/db")
	}))
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"INTERNAL_ERROR"`) ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("a panic must become a JSON 500", w.Code, w.Body.String())
	}
	out := buf.String()
	for _, leaked := range []string{"hunter2", "postgres://"} {
		if strings.Contains(out+w.Body.String(), leaked) {
			t.Fatalf("%q leaked: %s", leaked, out)
		}
	}
	var panicked, request map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("not JSON: %q", raw)
		}
		switch line["event"] {
		case "http.panic":
			panicked = line
		case "http.request":
			request = line
		}
	}
	if panicked == nil || panicked["panic_type"] != "string" || !strings.Contains(panicked["stack"].(string), "recover_test.go") || panicked["request_id"] == nil {
		t.Fatal("incorrect panic line", panicked)
	}
	if request == nil || request["status"] != float64(500) {
		t.Fatal("the request line must record the 500", request)
	}
	if server.ErrorLog == nil {
		t.Fatal("net/http errors must go through slog")
	}
}

func TestRecoverAbortsAResponseThatAlreadyStarted(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for name, handler := range map[string]http.HandlerFunc{
		"after write": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("partial"))
			panic("late failure")
		},
		"abort signal": func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if err, ok := recover().(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					t.Fatal("the connection must be dropped instead of appending a second body", err)
				}
			}()
			Recover(handler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		})
	}
}
