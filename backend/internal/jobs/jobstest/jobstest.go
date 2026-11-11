// Package jobstest runs an in-process NATS server with JetStream for tests.
package jobstest

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Server is an in-process NATS server whose JetStream files live in StoreDir,
// so a restart keeps the stream.
type Server struct {
	t        testing.TB
	StoreDir string
	srv      *server.Server
}

// Start runs a server on a random port, stopped when the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, StoreDir: t.TempDir()}
	s.start()
	t.Cleanup(s.Stop)
	return s
}

func (s *Server) start() {
	s.t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: server.RANDOM_PORT, JetStream: true,
		StoreDir: s.StoreDir, NoLog: true, NoSigs: true})
	if err != nil {
		s.t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		s.t.Fatal("nats server did not start")
	}
	s.srv = srv
}

// Stop shuts the server down; it is safe to call twice.
func (s *Server) Stop() {
	if s.srv != nil {
		s.srv.Shutdown()
		s.srv.WaitForShutdown()
		s.srv = nil
	}
}

// Restart stops the server and starts a new one on the same store; the new
// server listens on another port, so connect again.
func (s *Server) Restart() {
	s.t.Helper()
	s.Stop()
	s.start()
}

// Connect opens a client connection and its JetStream context, closed when
// the test ends.
func (s *Server) Connect() (*nats.Conn, jetstream.JetStream) {
	s.t.Helper()
	nc, err := nats.Connect(s.srv.ClientURL())
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		s.t.Fatal(err)
	}
	return nc, js
}
