// Package pgfault is a PostgreSQL wire proxy for recovery tests: it cuts every
// connection at a chosen point of a migration and refuses new ones until Restore.
// Only simple-protocol queries ('Q') are inspected.
package pgfault

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type Fault string

const (
	DropUserStatement Fault = "user_statement" // before the first user INSERT that contains the marker
	DropBeforeCommit  Fault = "before_commit"  // before COMMIT reaches the server
	DropCommitReply   Fault = "commit_reply"   // after the server committed, before the client hears it
)

type Event struct {
	Mode            Fault
	CommitForwarded bool
	CommitConfirmed bool
}

type Proxy struct {
	listener net.Listener
	upstream string
	mu       sync.Mutex
	peers    map[net.Conn]bool
	online   bool
	mode     Fault
	marker   string
	fired    bool
	queries  int
	events   chan Event
	workers  sync.WaitGroup
}

// New listens on a free local port and forwards to upstream (host:port) until the test ends.
func New(t testing.TB, upstream string) *Proxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{listener: listener, upstream: upstream, peers: map[net.Conn]bool{}, online: true, events: make(chan Event, 1)}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			if !p.add(client) {
				client.Close()
				continue
			}
			p.workers.Add(1)
			go p.serve(client)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		p.disconnect()
		done := make(chan struct{})
		go func() { p.workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("PostgreSQL proxy did not stop")
		}
	})
	return p
}

// HostPort is where clients connect instead of the upstream server.
func (p *Proxy) HostPort() (string, int) {
	addr := p.listener.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// Events reports each fault once it fired and every connection was cut.
func (p *Proxy) Events() <-chan Event { return p.events }

// Arm fires mode once on the next matching query; marker identifies the user's statements.
func (p *Proxy) Arm(mode Fault, marker string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode, p.marker, p.queries, p.fired = mode, marker, 0, false
}

// UserQueries counts queries containing the marker since Arm.
func (p *Proxy) UserQueries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queries
}

// Restore accepts connections again after a fault.
func (p *Proxy) Restore() {
	p.mu.Lock()
	p.online = true
	p.mu.Unlock()
}

func (p *Proxy) add(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.online {
		return false
	}
	p.peers[conn] = true
	return true
}

func (p *Proxy) remove(conn net.Conn) {
	conn.Close()
	p.mu.Lock()
	delete(p.peers, conn)
	p.mu.Unlock()
}

func (p *Proxy) disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.online = false
	for conn := range p.peers {
		conn.Close()
	}
}

func (p *Proxy) command(query string) Fault {
	p.mu.Lock()
	defer p.mu.Unlock()
	userSQL := p.marker != "" && strings.Contains(query, p.marker)
	if userSQL {
		p.queries++
	}
	if p.fired || p.mode == "" {
		return ""
	}
	commit := strings.EqualFold(strings.TrimSpace(query), "commit")
	if (commit && p.mode != DropUserStatement) || (p.mode == DropUserStatement && userSQL && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "INSERT INTO")) {
		p.fired = true
		return p.mode
	}
	return ""
}

func (p *Proxy) cut(event Event) {
	p.disconnect()
	p.events <- event
}

func packet(conn net.Conn, startup bool) ([]byte, error) {
	headerSize, lengthOffset := 5, 1
	if startup {
		headerSize, lengthOffset = 4, 0
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[lengthOffset:])
	if length < 4 || length > 1<<20 {
		return nil, errors.New("invalid PostgreSQL packet length")
	}
	data := make([]byte, int(length)+lengthOffset)
	copy(data, header)
	_, err := io.ReadFull(conn, data[headerSize:])
	return data, err
}

func forward(conn net.Conn, data []byte) error {
	_, err := io.Copy(conn, bytes.NewReader(data))
	return err
}

func (p *Proxy) serve(client net.Conn) {
	defer p.workers.Done()
	defer p.remove(client)
	server, err := net.DialTimeout("tcp", p.upstream, time.Second)
	if err != nil {
		return
	}
	defer p.remove(server)
	if !p.add(server) {
		return
	}
	startup, err := packet(client, true)
	if err != nil || forward(server, startup) != nil {
		return
	}
	var committing atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer client.Close()
		defer server.Close()
		confirmed := false
		for {
			data, err := packet(server, false)
			if err != nil {
				return
			}
			if committing.Load() {
				if data[0] == 'C' && string(data[5:]) == "COMMIT\x00" {
					confirmed = true
				}
				if data[0] == 'Z' {
					p.cut(Event{DropCommitReply, true, confirmed && bytes.Equal(data[5:], []byte{'I'})})
					return
				}
				// The client must receive neither part of the successful commit response.
				continue
			}
			if forward(client, data) != nil {
				return
			}
		}
	}()
	defer func() { client.Close(); server.Close(); <-done }()
	for {
		data, err := packet(client, false)
		if err != nil {
			return
		}
		if data[0] == 'Q' {
			mode := p.command(strings.TrimSuffix(string(data[5:]), "\x00"))
			if mode == DropUserStatement || mode == DropBeforeCommit {
				p.cut(Event{Mode: mode})
				return
			}
			if mode == DropCommitReply {
				committing.Store(true)
			}
		}
		if forward(server, data) != nil {
			return
		}
	}
}
