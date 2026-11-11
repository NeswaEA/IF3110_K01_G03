package telemetry

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/trace"
)

// PgxTracer records every statement on a pgx pool (pool is "metadata" or
// "target"; databaseID is empty for metadata) as a db.query span and a log
// line. Plug it into pgx.ConnConfig.Tracer; no database/sql driver wrapper.
func PgxTracer(pool, databaseID string) pgx.QueryTracer {
	return pgxTracer{pool: pool, databaseID: databaseID}
}

type pgxTracer struct{ pool, databaseID string }

type pgxQueryKey struct{}

type pgxQuery struct {
	query
	start time.Time
	span  trace.Span
}

func (t pgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	q := newQuery(ctx, t.pool, t.databaseID, data.SQL, data.Args)
	ctx, span := Tracer().Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(q.attrs()...))
	return context.WithValue(ctx, pgxQueryKey{}, &pgxQuery{query: q, start: time.Now(), span: span})
}

func (t pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	q, ok := ctx.Value(pgxQueryKey{}).(*pgxQuery)
	if !ok {
		return
	}
	rows := int64(-1)
	if data.Err == nil {
		rows = data.CommandTag.RowsAffected()
	}
	q.finish(ctx, q.span, q.start, rows, data.Err)
}
