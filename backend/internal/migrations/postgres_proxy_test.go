package migrations

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

type postgresFault string

const (
	dropUserStatement postgresFault = "user_statement"
	dropBeforeCommit  postgresFault = "before_commit"
	dropCommitReply   postgresFault = "commit_reply"
)

type postgresFaultEvent struct {
	mode            postgresFault
	commitForwarded bool
	commitConfirmed bool
}

type postgresFaultProxy struct {
	listener net.Listener
	upstream string
	mu       sync.Mutex
	peers    map[net.Conn]bool
	online   bool
	mode     postgresFault
	marker   string
	fired    bool
	queries  int
	events   chan postgresFaultEvent
	workers  sync.WaitGroup
}

func newPostgresFaultProxy(t *testing.T, upstream string) *postgresFaultProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &postgresFaultProxy{listener: listener, upstream: upstream, peers: map[net.Conn]bool{}, online: true, events: make(chan postgresFaultEvent, 1)}
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

func (p *postgresFaultProxy) add(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.online {
		return false
	}
	p.peers[conn] = true
	return true
}

func (p *postgresFaultProxy) remove(conn net.Conn) {
	conn.Close()
	p.mu.Lock()
	delete(p.peers, conn)
	p.mu.Unlock()
}

func (p *postgresFaultProxy) disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.online = false
	for conn := range p.peers {
		conn.Close()
	}
}

func (p *postgresFaultProxy) restore() {
	p.mu.Lock()
	p.online = true
	p.mu.Unlock()
}

func (p *postgresFaultProxy) arm(mode postgresFault, marker string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode, p.marker, p.queries, p.fired = mode, marker, 0, false
}

func (p *postgresFaultProxy) userQueries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queries
}

func (p *postgresFaultProxy) command(query string) postgresFault {
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
	if (commit && p.mode != dropUserStatement) || (p.mode == dropUserStatement && userSQL && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "INSERT INTO")) {
		p.fired = true
		return p.mode
	}
	return ""
}

func (p *postgresFaultProxy) cut(event postgresFaultEvent) {
	p.disconnect()
	p.events <- event
}

func postgresPacket(conn net.Conn, startup bool) ([]byte, error) {
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
	packet := make([]byte, int(length)+lengthOffset)
	copy(packet, header)
	_, err := io.ReadFull(conn, packet[headerSize:])
	return packet, err
}

func forwardPostgresPacket(conn net.Conn, packet []byte) error {
	_, err := io.Copy(conn, bytes.NewReader(packet))
	return err
}

func (p *postgresFaultProxy) serve(client net.Conn) {
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
	startup, err := postgresPacket(client, true)
	if err != nil || forwardPostgresPacket(server, startup) != nil {
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
			packet, err := postgresPacket(server, false)
			if err != nil {
				return
			}
			if committing.Load() {
				if packet[0] == 'C' && string(packet[5:]) == "COMMIT\x00" {
					confirmed = true
				}
				if packet[0] == 'Z' {
					p.cut(postgresFaultEvent{dropCommitReply, true, confirmed && bytes.Equal(packet[5:], []byte{'I'})})
					return
				}
				// The client must receive neither part of the successful commit response.
				continue
			}
			if forwardPostgresPacket(client, packet) != nil {
				return
			}
		}
	}()
	defer func() { client.Close(); server.Close(); <-done }()
	for {
		packet, err := postgresPacket(client, false)
		if err != nil {
			return
		}
		if packet[0] == 'Q' {
			mode := p.command(strings.TrimSuffix(string(packet[5:]), "\x00"))
			if mode == dropUserStatement || mode == dropBeforeCommit {
				p.cut(postgresFaultEvent{mode: mode})
				return
			}
			if mode == dropCommitReply {
				committing.Store(true)
			}
		}
		if forwardPostgresPacket(server, packet) != nil {
			return
		}
	}
}
