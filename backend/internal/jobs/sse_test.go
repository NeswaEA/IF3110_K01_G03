package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs/jobstest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func sseServer(t *testing.T, user string, configure func(*EventsHandler)) (*httptest.Server, *EventBus) {
	t.Helper()
	srv := jobstest.Start(t)
	nc, _ := srv.Connect()
	bus := NewEventBus(nc)
	principal := func(context.Context) (contracts.Principal, bool) {
		return contracts.Principal{UserID: user, Username: "u"}, user != ""
	}
	h := NewEventsHandler(bus, testkit.NewFixtureAccessChecker(), principal)
	if configure != nil {
		configure(h)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, bus
}

func get(t *testing.T, ts *httptest.Server, id string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/databases/"+id+"/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func errorCode(t *testing.T, res *http.Response) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code
}

func TestEventsRequireSessionAndGrant(t *testing.T) {
	anon, _ := sseServer(t, "", nil)
	if res := get(t, anon, testkit.DBPG); res.StatusCode != http.StatusUnauthorized || errorCode(t, res) != "UNAUTHENTICATED" {
		t.Fatalf("anonymous: %d", res.StatusCode)
	}
	none, _ := sseServer(t, testkit.UserNone, nil)
	if res := get(t, none, testkit.DBPG); res.StatusCode != http.StatusNotFound || errorCode(t, res) != "NOT_FOUND" {
		t.Fatalf("no grant: %d", res.StatusCode)
	}
	if res := get(t, none, "NOT-A-UUID"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id: %d", res.StatusCode)
	}
}

func TestEventsStreamToViewer(t *testing.T) {
	ts, bus := sseServer(t, testkit.UserView, func(h *EventsHandler) { h.Heartbeat = 100 * time.Millisecond })
	res := get(t, ts, testkit.DBPG)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" ||
		res.Header.Get("Cache-Control") != "no-cache" || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status %d headers %v", res.StatusCode, res.Header)
	}
	r := bufio.NewReader(res.Body)
	if line, _ := r.ReadString('\n'); line != "retry: 3000\n" {
		t.Fatalf("first line %q", line)
	}
	// Another database's event never reaches this stream.
	bus.Publish(context.Background(), contracts.DatabaseEvent{DatabaseID: testkit.DBSQLite, Type: "execution.updated", ResourceID: "x", Status: "running"})
	at := time.Date(2026, 11, 15, 8, 0, 1, 0, time.UTC)
	go func() {
		time.Sleep(200 * time.Millisecond) // after the subscription is live
		bus.Publish(context.Background(), contracts.DatabaseEvent{DatabaseID: testkit.DBPG, Type: "execution.updated",
			ResourceID: "0193a0c4-7f1e-7c2a-9d3b-5f2e8a1b4c6d", Status: "running", At: at})
	}()
	var sawPing bool
	var event []string
	for len(event) < 3 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case line == ": ping\n":
			sawPing = true
		case line != "\n":
			event = append(event, strings.TrimSuffix(line, "\n"))
		}
	}
	want := []string{"event: execution.updated", "id: 0193a0c4-7f1e-7c2a-9d3b-5f2e8a1b4c6d",
		`data: {"database_id":"30000000-0000-4000-8000-000000000001","resource_id":"0193a0c4-7f1e-7c2a-9d3b-5f2e8a1b4c6d","status":"running","at":"2026-11-15T08:00:01Z"}`}
	for i := range want {
		if event[i] != want[i] {
			t.Fatalf("event line %d = %q, want %q", i, event[i], want[i])
		}
	}
	if !sawPing {
		// the heartbeat is 100ms and the event came after 200ms
		t.Fatal("no heartbeat comment before the event")
	}
}

func TestEventsStreamClosesWhenAccessIsRevoked(t *testing.T) {
	var checks atomic.Int32
	ts, _ := sseServer(t, testkit.UserView, func(h *EventsHandler) {
		h.Recheck = 100 * time.Millisecond
		h.Revalidate = func(context.Context, contracts.Principal, string) error {
			if checks.Add(1) > 1 {
				return errors.New("session revoked")
			}
			return nil
		}
	})
	res := get(t, ts, testkit.DBPG)
	done := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(res.Body).ReadString(0) // reads until the server closes
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after access was revoked")
	}
}
