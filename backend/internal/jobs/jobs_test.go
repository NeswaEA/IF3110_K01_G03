package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs/jobstest"
)

var spans = tracetest.NewInMemoryExporter()

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	os.Exit(m.Run())
}

// metadata returns a database whose search_path is a fresh schema holding
// only the outbox table, so relays in parallel tests never see each other's rows.
func metadata(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_METADATA_DATABASE_URL to run the queue tests")
	}
	ddl, err := os.ReadFile("../../migrations/0008_outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "jobs_test_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	return db
}

func queue(t *testing.T) (*jobstest.Server, jetstream.JetStream) {
	t.Helper()
	srv := jobstest.Start(t)
	_, js := srv.Connect()
	if err := EnsureStream(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	return srv, js
}

// handler records deliveries; behave decides each result.
type handler struct {
	mu        sync.Mutex
	got       []contracts.JobMessage
	traces    []trace.TraceID
	delivered chan contracts.JobMessage
	behave    func(ctx context.Context, n int) contracts.JobResult
}

func newHandler(behave func(ctx context.Context, n int) contracts.JobResult) *handler {
	if behave == nil {
		behave = func(context.Context, int) contracts.JobResult { return contracts.JobResult{Ack: true, Reason: "done"} }
	}
	return &handler{delivered: make(chan contracts.JobMessage, 100), behave: behave}
}

func (h *handler) Handle(ctx context.Context, m contracts.JobMessage) contracts.JobResult {
	h.mu.Lock()
	h.got = append(h.got, m)
	h.traces = append(h.traces, trace.SpanContextFromContext(ctx).TraceID())
	n := len(h.got)
	h.mu.Unlock()
	h.delivered <- m
	return h.behave(ctx, n)
}

func (h *handler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.got)
}

func (h *handler) wait(t *testing.T, timeout time.Duration) contracts.JobMessage {
	t.Helper()
	select {
	case m := <-h.delivered:
		return m
	case <-time.After(timeout):
		t.Fatal("no job delivered")
	}
	return contracts.JobMessage{}
}

func run(t *testing.T, fn func(ctx context.Context) error) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = fn(ctx); close(done) }()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

func enqueue(t *testing.T, ctx context.Context, db *sql.DB, commit bool) contracts.JobMessage {
	t.Helper()
	m := contracts.JobMessage{ExecutionID: uuid.Must(uuid.NewV7()).String(), DatabaseID: "30000000-0000-4000-8000-000000000001"}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewOutbox(db).Enqueue(ctx, tx, m); err != nil {
		t.Fatal(err)
	}
	if commit {
		err = tx.Commit()
	} else {
		err = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func streamMessages(t *testing.T, js jetstream.JetStream) uint64 {
	t.Helper()
	s, err := js.Stream(context.Background(), StreamName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

func TestEnsureStreamIsIdempotent(t *testing.T) {
	_, js := queue(t)
	if err := EnsureStream(context.Background(), js); err != nil {
		t.Fatalf("second EnsureStream: %v", err)
	}
	s, err := js.Stream(context.Background(), StreamName)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.CachedInfo().Config
	if cfg.Retention != jetstream.WorkQueuePolicy || cfg.Storage != jetstream.FileStorage || cfg.Duplicates != 2*time.Minute ||
		cfg.MaxAge != 7*24*time.Hour || len(cfg.Subjects) != 1 || cfg.Subjects[0] != "migrations.jobs.*" {
		t.Fatalf("stream config: %+v", cfg)
	}
}

func TestEnqueueFollowsTransactionAndDeliversOnce(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	ctx := context.Background()
	enqueue(t, ctx, db, false)
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM outbox_messages`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rolled-back enqueue left %d outbox rows (%v)", rows, err)
	}
	m := enqueue(t, ctx, db, true)

	relay := NewRelay(db, js, RelayOptions{PollInterval: 50 * time.Millisecond})
	run(t, relay.Run)
	h := newHandler(nil)
	run(t, NewConsumer(js, h, ConsumerOptions{Concurrency: 2, FetchWait: 200 * time.Millisecond}).Run)
	if got := h.wait(t, 5*time.Second); got != m {
		t.Fatalf("delivered %+v, want %+v", got, m)
	}
	time.Sleep(time.Second)
	if n := h.count(); n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
}

func TestRelayPublishesMessageIDAndTraceContext(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	spans.Reset()
	ctx, root := otel.Tracer("test").Start(context.Background(), "HTTP POST execute")
	m := enqueue(t, ctx, db, true)
	root.End()

	relay := NewRelay(db, js, RelayOptions{})
	if n, err := relay.publishBatch(context.Background()); err != nil || n != 1 {
		t.Fatalf("publishBatch = %d, %v", n, err)
	}
	var id string
	var published sql.NullTime
	if err := db.QueryRow(`SELECT id, published_at FROM outbox_messages`).Scan(&id, &published); err != nil || !published.Valid {
		t.Fatalf("published_at not set: %v", err)
	}
	s, _ := js.Stream(context.Background(), StreamName)
	raw, err := s.GetMsg(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Header.Get("Nats-Msg-Id") != id || !strings.HasPrefix(raw.Header.Get("traceparent"), "00-"+root.SpanContext().TraceID().String()) {
		t.Fatalf("headers %v, want Nats-Msg-Id %s and the request trace", raw.Header, id)
	}
	if raw.Subject != "migrations.jobs."+m.DatabaseID {
		t.Fatalf("subject %s", raw.Subject)
	}

	// The worker's span continues the request trace.
	h := newHandler(nil)
	run(t, NewConsumer(js, h, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	h.wait(t, 5*time.Second)
	h.mu.Lock()
	got := h.traces[0]
	h.mu.Unlock()
	if got != root.SpanContext().TraceID() {
		t.Fatalf("handler trace %s, want %s", got, root.SpanContext().TraceID())
	}
}

func TestParallelRelaysPublishEachRowOnce(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	const total = 150
	for range total {
		enqueue(t, ctx, db, true)
	}
	var wg sync.WaitGroup
	for range 2 {
		relay := NewRelay(db, js, RelayOptions{BatchSize: 10})
		wg.Go(func() {
			for {
				n, err := relay.publishBatch(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					var left int
					_ = db.QueryRow(`SELECT count(*) FROM outbox_messages WHERE published_at IS NULL`).Scan(&left)
					if left == 0 {
						return
					}
				}
			}
		})
	}
	wg.Wait()
	logMu.Lock()
	publishes := strings.Count(logs.String(), `"event":"outbox.published"`)
	logMu.Unlock()
	if publishes != total || streamMessages(t, js) != total {
		t.Fatalf("published %d times, stream holds %d; want %d each", publishes, streamMessages(t, js), total)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestRepublishAfterCrashIsDeduplicated(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	enqueue(t, context.Background(), db, true)
	relay := NewRelay(db, js, RelayOptions{})
	if _, err := relay.publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Crash after publish, before commit: the row is still unpublished.
	if _, err := db.Exec(`UPDATE outbox_messages SET published_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := streamMessages(t, js); n != 1 {
		t.Fatalf("stream holds %d messages after a republish, want 1", n)
	}
}

func TestRelayKeepsRowWhenPublishFails(t *testing.T) {
	db := metadata(t)
	srv, js := queue(t)
	enqueue(t, context.Background(), db, true)
	srv.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := NewRelay(db, js, RelayOptions{}).publishBatch(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var lastErr sql.NullString
	if err := db.QueryRow(`SELECT publish_attempts, last_error FROM outbox_messages WHERE published_at IS NULL`).Scan(&attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !lastErr.Valid {
		t.Fatalf("attempts %d, last_error %v", attempts, lastErr)
	}
}

func TestInProgressPreventsRedeliveryOfLongJob(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	enqueue(t, context.Background(), db, true)
	if _, err := NewRelay(db, js, RelayOptions{}).publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := newHandler(func(ctx context.Context, n int) contracts.JobResult {
		time.Sleep(3500 * time.Millisecond) // longer than AckWait
		return contracts.JobResult{Ack: true, Reason: "done"}
	})
	run(t, NewConsumer(js, h, ConsumerOptions{Concurrency: 2, AckWait: time.Second, FetchWait: 200 * time.Millisecond}).Run)
	h.wait(t, 5*time.Second)
	time.Sleep(4500 * time.Millisecond)
	if n := h.count(); n != 1 {
		t.Fatalf("delivered %d times while the handler was alive, want 1", n)
	}
}

func TestNakRedeliversAfterDelay(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	enqueue(t, context.Background(), db, true)
	if _, err := NewRelay(db, js, RelayOptions{}).publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := newHandler(func(_ context.Context, n int) contracts.JobResult {
		if n == 1 {
			return contracts.JobResult{Delay: 300 * time.Millisecond, Reason: "lock_busy"}
		}
		return contracts.JobResult{Ack: true, Reason: "done"}
	})
	run(t, NewConsumer(js, h, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	h.wait(t, 5*time.Second)
	start := time.Now()
	h.wait(t, 5*time.Second)
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("redelivered after %v, want the 300ms delay", d)
	}
}

func TestShutdownReleasesUnfinishedJobToAnotherWorker(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	enqueue(t, context.Background(), db, true)
	if _, err := NewRelay(db, js, RelayOptions{}).publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	first := newHandler(func(ctx context.Context, _ int) contracts.JobResult {
		<-ctx.Done()                       // shutdown signal
		time.Sleep(200 * time.Millisecond) // the batch in flight finishes
		close(finished)
		return contracts.JobResult{Reason: "retry"} // not acked
	})
	stop := run(t, NewConsumer(js, first, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	first.wait(t, 5*time.Second)
	stop()
	select {
	case <-finished:
	default:
		t.Fatal("Run returned before the handler finished")
	}
	second := newHandler(nil)
	run(t, NewConsumer(js, second, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	second.wait(t, 5*time.Second)
}

func TestQueuedJobSurvivesNATSRestart(t *testing.T) {
	db := metadata(t)
	srv, js := queue(t)
	enqueue(t, context.Background(), db, true)
	if _, err := NewRelay(db, js, RelayOptions{}).publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Restart()
	_, js = srv.Connect()
	if err := EnsureStream(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	h := newHandler(nil)
	run(t, NewConsumer(js, h, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	h.wait(t, 5*time.Second)
}

type staleSource []contracts.JobMessage

func (s staleSource) StaleJobs(context.Context, time.Duration) ([]contracts.JobMessage, error) {
	return s, nil
}

func TestSweeperReenqueuesStaleJobs(t *testing.T) {
	db := metadata(t)
	_, js := queue(t)
	m := contracts.JobMessage{ExecutionID: uuid.Must(uuid.NewV7()).String(), DatabaseID: "30000000-0000-4000-8000-000000000002"}
	if err := NewSweeper(staleSource{m}, NewOutbox(db), db, SweeperOptions{}).sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRelay(db, js, RelayOptions{}).publishBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := newHandler(nil)
	run(t, NewConsumer(js, h, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	if got := h.wait(t, 5*time.Second); got != m {
		t.Fatalf("got %+v, want %+v", got, m)
	}
}

func TestMalformedMessageIsTerminated(t *testing.T) {
	_, js := queue(t)
	if _, err := js.Publish(context.Background(), "migrations.jobs.x", []byte(`{"execution_id":"nope"}`)); err != nil {
		t.Fatal(err)
	}
	h := newHandler(nil)
	run(t, NewConsumer(js, h, ConsumerOptions{FetchWait: 200 * time.Millisecond}).Run)
	time.Sleep(time.Second)
	if h.count() != 0 {
		t.Fatal("malformed message reached the handler")
	}
}

func TestEnqueueRejectsBadIDs(t *testing.T) {
	err := NewOutbox(nil).Enqueue(context.Background(), nil, contracts.JobMessage{ExecutionID: "x", DatabaseID: "y"})
	if err == nil {
		t.Fatal("bad ids accepted")
	}
	_ = fmt.Sprint(err)
}
