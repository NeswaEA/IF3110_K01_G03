package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

var (
	spans   = tracetest.NewInMemoryExporter()
	logMu   sync.Mutex
	logBuf  bytes.Buffer
	promReg = promclient.NewRegistry()
)

type syncWriter struct{}

func (syncWriter) Write(p []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()
	return logBuf.Write(p)
}

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)))
	exporter, err := otelprom.New(otelprom.WithRegisterer(promReg))
	if err != nil {
		panic(err)
	}
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)))
	slog.SetDefault(platform.NewLogger(syncWriter{}, slog.LevelDebug, nil))
	os.Exit(m.Run())
}

func logLines(t *testing.T) []map[string]any {
	t.Helper()
	logMu.Lock()
	defer logMu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(logBuf.Bytes()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func resetCapture() {
	spans.Reset()
	logMu.Lock()
	logBuf.Reset()
	logMu.Unlock()
}

func tracedTarget(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_TARGET_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_TARGET_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_TARGET_DATABASE_URL to run the SQL tracer tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tracer = PgxTracer("target", "30000000-0000-4000-8000-000000000001")
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPgxTracerLogsAndTracesEveryStatement(t *testing.T) {
	db := tracedTarget(t)
	resetCapture()
	ctx, parent := Tracer().Start(context.Background(), "migration.job")
	ctx = WithLogAttrs(ctx, slog.String("execution_id", "0193a0c4-7f1e-7c2a-9d3b-5f2e8a1b4c6d"), slog.Int("attempt", 1))
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	user := WithUserSQL(ctx)
	steps := []struct {
		ctx  context.Context
		sql  string
		args []any
	}{
		{ctx, "BEGIN", nil},
		{user, "SELECT 'secret-literal' AS v, $1::int AS n", []any{int64(42)}},
		{ctx, "COMMIT", nil},
	}
	for _, s := range steps {
		if _, err := conn.ExecContext(s.ctx, s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	_, failed := conn.ExecContext(user, "SELECT 1/0")
	if failed == nil {
		t.Fatal("division by zero succeeded")
	}
	parent.End()

	lines := logLines(t)
	if len(lines) != 4 {
		t.Fatalf("got %d log lines, want 4: %v", len(lines), lines)
	}
	wantEvents := []string{"db.tx.begin", "db.query", "db.tx.commit", "db.query"}
	traceID := parent.SpanContext().TraceID().String()
	ended := spans.GetSpans()
	for i, line := range lines {
		if line["event"] != wantEvents[i] {
			t.Errorf("line %d event = %v, want %s", i, line["event"], wantEvents[i])
		}
		if line["trace_id"] != traceID {
			t.Errorf("line %d trace_id = %v, want %s", i, line["trace_id"], traceID)
		}
		if line["level"] != "INFO" || line["db.pool"] != "target" || line["database_id"] != "30000000-0000-4000-8000-000000000001" {
			t.Errorf("line %d: %v", i, line)
		}
		if line["execution_id"] != "0193a0c4-7f1e-7c2a-9d3b-5f2e8a1b4c6d" || line["attempt"] != float64(1) {
			t.Errorf("line %d lacks the execution attributes: %v", i, line)
		}
		if _, ok := line["duration_ms"].(float64); !ok {
			t.Errorf("line %d has no duration_ms", i)
		}
		// The log line belongs to the db.query span that was active.
		if ended[i].SpanContext.SpanID().String() != line["span_id"] || ended[i].Name != "db.query" {
			t.Errorf("line %d span_id = %v, span %d is %s %s", i, line["span_id"], i, ended[i].Name, ended[i].SpanContext.SpanID())
		}
	}
	query := lines[1]
	if q, _ := query["db.query"].(string); strings.Contains(q, "secret-literal") || !strings.Contains(q, "$2") {
		t.Errorf("user SQL not normalised: %q", q)
	}
	if params, _ := query["db.params"].([]any); len(params) != 1 || params[0] != "<int64>" {
		t.Errorf("db.params = %v, want only the type", query["db.params"])
	}
	if query["rows"] != float64(1) || query["db.operation"] != "SELECT" || query["status"] != "ok" {
		t.Errorf("query line: %v", query)
	}
	if lines[3]["sqlstate"] != "22012" || lines[3]["status"] != "error" {
		t.Errorf("failed statement line: %v", lines[3])
	}
	if raw := logBuf.String(); strings.Contains(raw, "42") && strings.Contains(raw, `"db.params":[42`) {
		t.Error("parameter value leaked into the log")
	}
}

func TestObserveSQLMatchesTracerFormat(t *testing.T) {
	resetCapture()
	ctx := WithUserSQL(context.Background())
	ObserveSQL(ctx, SQLEvent{Pool: "target", DatabaseID: "30000000-0000-4000-8000-000000000002",
		Query: "UPDATE items SET name = 'x' WHERE id = ?", Args: []any{int64(7)}, Start: time.Now().Add(-5 * time.Millisecond), Rows: 1})
	lines := logLines(t)
	if len(lines) != 1 {
		t.Fatalf("got %d lines", len(lines))
	}
	l := lines[0]
	if l["event"] != "db.query" || l["db.operation"] != "UPDATE" || l["rows"] != float64(1) || l["trace_id"] == nil {
		t.Fatalf("line: %v", l)
	}
	if q := l["db.query"].(string); strings.Contains(q, "'x'") {
		t.Fatalf("literal kept: %q", q)
	}
	if d := l["duration_ms"].(float64); d < 5 {
		t.Fatalf("duration_ms = %v, want >= 5", d)
	}
	if got := spans.GetSpans(); len(got) != 1 || got[0].Name != "db.query" {
		t.Fatalf("spans: %v", got)
	}
}

func TestUnparseableUserSQLIsHashed(t *testing.T) {
	if got := normalize("SELEC 'pw'"); !strings.HasPrefix(got, "[unparseable] sha256:") || strings.Contains(got, "pw") {
		t.Fatalf("normalize = %q", got)
	}
}

func TestOperation(t *testing.T) {
	cases := map[string]string{
		"begin isolation level read committed": "BEGIN",
		"  COMMIT":                             "COMMIT",
		"-- note\nrollback":                    "ROLLBACK",
		"/* x */ SELECT 1":                     "SELECT",
		"WITH batch AS (SELECT id FROM t WHERE id > $1 LIMIT $3) UPDATE t SET p = round(t.p, 2) FROM batch RETURNING t.id": "UPDATE",
		"with x as (select 1) select * from x": "SELECT",
		"INSERT INTO t SELECT * FROM 'WITH'":   "INSERT",
		"":                                     "",
		"(":                                    "",
	}
	for sql, want := range cases {
		if got := operation(sql); got != want {
			t.Errorf("operation(%q) = %q, want %q", sql, got, want)
		}
	}
}

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestMetricNamesAndLabels(t *testing.T) {
	ctx := context.Background()
	RecordQueueWait(ctx, "apply", time.Second)
	RecordLockWait(ctx, "postgres", time.Second)
	RecordDelivery(ctx, "done")
	RecordRetry(ctx, "apply", "batch")
	RecordFinished(ctx, "apply", "batch", "succeeded", time.Minute)
	RecordBatch(ctx, "up", "committed", 1000)
	if err := RegisterJobGauge(func(context.Context) (map[string]int64, error) {
		return map[string]int64{"queued": 2, "running": 1}, nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", "postgres://unused@127.0.0.1:1/x")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer RegisterPool("metadata", db)()

	body := scrape(t)
	for _, name := range []string{
		"migration_jobs{", "migration_jobs_finished_total{", "migration_job_retries_total{", "migration_job_deliveries_total{",
		"migration_queue_wait_seconds_bucket{", "migration_lock_wait_seconds_bucket{", "migration_duration_seconds_bucket{",
		"migration_batches_total{", "migration_rows_total{", `db_pool_connections{`, "db_pool_max_open_connections{",
		"db_pool_wait_count_total{", "db_pool_wait_duration_seconds_total{",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("metric %s missing from /metrics", name)
		}
	}
	for _, forbidden := range []string{"execution_id", "database_id", "user_id", "db_query"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("forbidden label %s in /metrics", forbidden)
		}
	}
}

func TestPoolInUseRisesDuringLongQuery(t *testing.T) {
	db := tracedTarget(t)
	defer RegisterPool("target", db)()
	inUse := func() bool {
		for _, line := range strings.Split(scrape(t), "\n") {
			if strings.HasPrefix(line, "db_pool_connections{") && strings.Contains(line, `pool="target"`) &&
				strings.Contains(line, `state="in_use"`) {
				return !strings.HasSuffix(line, " 0")
			}
		}
		return false
	}
	if inUse() {
		t.Fatal("in_use before the query")
	}
	done := make(chan error)
	go func() {
		_, err := db.ExecContext(context.Background(), "SELECT pg_sleep(0.5)")
		done <- err
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	for !inUse() {
		if time.Now().After(deadline) {
			t.Fatal("db_pool_connections{state=in_use} never rose during the query")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPHandlerNamesSpanByInnerRoute(t *testing.T) {
	resetCapture()
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/v1/databases/{id}/schema", func(w http.ResponseWriter, r *http.Request) {})
	outer := http.NewServeMux()
	// middleware between the muxes copies the request, hiding the inner pattern
	outer.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Route(inner).ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), struct{}{}, 1)))
	}))
	outer.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {})
	h := HTTPHandler(outer)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/databases/30000000-0000-4000-8000-000000000001/schema", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	got := spans.GetSpans()
	if len(got) != 2 || got[0].Name != "GET /api/v1/databases/{id}/schema" || got[1].Name != "GET /healthz" {
		t.Fatalf("span names: %v", got)
	}
	body := scrape(t)
	if !strings.Contains(body, `http_route="/api/v1/databases/{id}/schema"`) || strings.Contains(body, "30000000-0000-4000-8000-000000000001") {
		t.Fatalf("http_route label missing or raw path leaked:\n%s", body)
	}
}
