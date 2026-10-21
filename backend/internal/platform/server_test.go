package platform_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func TestHTTPTimeouts(t *testing.T) {
	server := platform.NewHTTPServer(":8080", http.NewServeMux())
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second || server.WriteTimeout != 40*time.Second || server.IdleTimeout != time.Minute {
		t.Fatal("unexpected HTTP timeouts")
	}
}

func TestGracefulShutdownFinishesActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started, release, shuttingDown := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := platform.NewHTTPServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			if r.Context().Err() != nil {
				http.Error(w, "canceled", 500)
				return
			}
			w.Write([]byte("finished"))
		case <-r.Context().Done():
			http.Error(w, "canceled", 500)
		}
	}))
	server.RegisterOnShutdown(func() { close(shuttingDown) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- platform.Serve(ctx, server, listener) }()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response := make(chan string, 1)
	go func() {
		res, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			response <- "request failed"
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || res.Header.Get("X-Request-ID") == "" {
			response <- "invalid response"
			return
		}
		response <- string(body)
	}()
	waitSignal(t, started)
	cancel()
	waitSignal(t, shuttingDown)
	select {
	case err := <-done:
		t.Fatalf("shutdown completed before active request: %v", err)
	default:
	}
	close(release)
	select {
	case body := <-response:
		if body != "finished" {
			t.Fatal(body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if res, err := client.Get("http://" + listener.Addr().String()); err == nil {
		res.Body.Close()
		t.Fatal("server accepted a request after shutdown")
	}
}

func TestServeReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if err := platform.Serve(context.Background(), platform.NewHTTPServer("", http.NewServeMux()), listener); err == nil {
		t.Fatal("expected listener failure")
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server")
	}
}
