package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const instrumentation = "github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"

// Tracer is the tracer every module uses for its own spans.
func Tracer() trace.Tracer { return otel.Tracer(instrumentation) }

type userSQLKey struct{}
type logAttrsKey struct{}

// WithUserSQL marks statements run under ctx as user-written SQL: they are
// normalised (literals become parameters) before they are logged.
func WithUserSQL(ctx context.Context) context.Context {
	return context.WithValue(ctx, userSQLKey{}, true)
}

// WithLogAttrs adds attributes (execution_id, attempt, batch, ...) to every
// SQL log line recorded under ctx.
func WithLogAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(logAttrsKey{}).([]slog.Attr)
	return context.WithValue(ctx, logAttrsKey{}, append(append([]slog.Attr(nil), prev...), attrs...))
}

// SQLEvent is one finished statement run outside pgx (the SQLite driver).
type SQLEvent struct {
	Pool       string // "target" or "metadata"
	DatabaseID string
	Query      string
	Args       []any
	Start      time.Time
	Rows       int64 // -1 when unknown
	Err        error
}

// ObserveSQL records a statement already run, with the same span and log
// format as the pgx tracer.
func ObserveSQL(ctx context.Context, ev SQLEvent) {
	q := newQuery(ctx, ev.Pool, ev.DatabaseID, ev.Query, ev.Args)
	ctx, span := Tracer().Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(ev.Start), trace.WithAttributes(q.attrs()...))
	q.finish(ctx, span, ev.Start, ev.Rows, ev.Err)
}

type query struct {
	pool, databaseID, operation, text string
	args                              []any
}

func newQuery(ctx context.Context, pool, databaseID, sql string, args []any) query {
	text := strings.TrimSpace(sql)
	if user, _ := ctx.Value(userSQLKey{}).(bool); user {
		text = normalize(text)
	}
	return query{pool: pool, databaseID: databaseID, operation: operation(sql), text: text, args: args}
}

func (q query) attrs() []attribute.KeyValue {
	types := make([]string, len(q.args))
	for i, a := range q.args {
		types[i] = typeOf(a)
	}
	attrs := []attribute.KeyValue{
		attribute.String("db.pool", q.pool), attribute.String("db.operation", q.operation),
		attribute.String("db.query", q.text), attribute.StringSlice("db.params", types),
	}
	if q.databaseID != "" {
		attrs = append(attrs, attribute.String("database_id", q.databaseID))
	}
	return attrs
}

// finish ends span and writes the log line: db.tx.* for transaction control,
// db.query for everything else. Target statements log at INFO, metadata at DEBUG.
func (q query) finish(ctx context.Context, span trace.Span, start time.Time, rows int64, err error) {
	defer span.End()
	elapsed := time.Since(start)
	level, status := slog.LevelInfo, "ok"
	if q.pool != "target" {
		level = slog.LevelDebug
	}
	attrs := []slog.Attr{
		slog.String("event", event(q.operation)), slog.String("db.pool", q.pool),
		slog.String("db.operation", q.operation), slog.String("db.query", q.text),
		slog.Any("db.params", q.args), // the logger keeps only their types
		slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
	}
	if q.databaseID != "" {
		attrs = append(attrs, slog.String("database_id", q.databaseID))
	}
	if rows >= 0 {
		attrs = append(attrs, slog.Int64("rows", rows))
		span.SetAttributes(attribute.Int64("rows", rows))
	}
	if err != nil {
		status = "error"
		span.SetStatus(codes.Error, "statement failed")
		if state := sqlState(err); state != "" {
			attrs = append(attrs, slog.String("sqlstate", state))
			span.SetAttributes(attribute.String("sqlstate", state))
		}
	}
	attrs = append(attrs, slog.String("status", status))
	extra, _ := ctx.Value(logAttrsKey{}).([]slog.Attr)
	attrs = append(attrs, extra...)
	slog.LogAttrs(ctx, level, "sql executed", attrs...)
}

func event(operation string) string {
	switch operation {
	case "BEGIN", "START":
		return "db.tx.begin"
	case "COMMIT", "END":
		return "db.tx.commit"
	case "ROLLBACK", "ABORT":
		return "db.tx.rollback"
	}
	return "db.query"
}

// operation is the statement's first keyword, skipping comments; for a WITH
// statement it is the first verb outside the CTE parentheses.
func operation(sql string) string {
	s := strings.TrimSpace(sql)
	for strings.HasPrefix(s, "--") || strings.HasPrefix(s, "/*") {
		end, skip := strings.IndexByte(s, '\n'), 1
		if strings.HasPrefix(s, "/*") {
			end, skip = strings.Index(s, "*/"), 2
		}
		if end < 0 {
			return ""
		}
		s = strings.TrimSpace(s[end+skip:])
	}
	words := topLevelWords(s)
	if len(words) == 0 {
		return ""
	}
	if words[0] == "WITH" {
		for _, w := range words[1:] {
			switch w {
			case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE":
				return w
			}
		}
	}
	return words[0]
}

// topLevelWords lists the upper-cased words outside parentheses and quotes.
func topLevelWords(s string) []string {
	var words []string
	depth, start := 0, -1
	flush := func(i int) {
		if start >= 0 {
			words = append(words, strings.ToUpper(s[start:i]))
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isWord := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		switch {
		case isWord && depth == 0:
			if start < 0 {
				start = i
			}
			continue
		case c == '\'' || c == '"':
			flush(i)
			if j := strings.IndexByte(s[i+1:], c); j >= 0 {
				i += j + 1
			}
			continue
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		}
		flush(i)
	}
	flush(len(s))
	return words
}

func normalize(sql string) string {
	out, err := pg_query.Normalize(sql)
	if err != nil {
		sum := sha256.Sum256([]byte(sql))
		return "[unparseable] sha256:" + hex.EncodeToString(sum[:])
	}
	return out
}

func typeOf(v any) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("<%T>", v)
}

func sqlState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}
