package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"

	"go.opentelemetry.io/otel/trace"
)

type actorIDKey struct{}

// WithActorID marks ctx with the signed-in user so every log line carries actor_id.
func WithActorID(ctx context.Context, userID string) context.Context {
	if entry, ok := ctx.Value(requestLogKey{}).(*requestLog); ok {
		entry.actor = userID
	}
	return context.WithValue(ctx, actorIDKey{}, userID)
}

var sensitiveKey = regexp.MustCompile(`(?i)password|secret|token|cookie|csrf|dsn|authorization|session`)

// NewLogger writes JSON to w. Every record (and extra, when given, e.g. the
// OTLP bridge) gets the same redaction and the request_id, actor_id,
// trace_id and span_id found in the context.
func NewLogger(w io.Writer, level slog.Level, extra slog.Handler) *slog.Logger {
	handlers := []slog.Handler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})}
	if extra != nil {
		handlers = append(handlers, extra)
	}
	return slog.New(logHandler{handlers})
}

type logHandler struct{ next []slog.Handler }

func (h logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, next := range h.next {
		if next.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	seen := map[string]bool{}
	r.Attrs(func(a slog.Attr) bool {
		seen[a.Key] = true
		out.AddAttrs(redact(a))
		return true
	})
	add := func(key, value string) {
		if value != "" && !seen[key] {
			out.AddAttrs(slog.String(key, value))
		}
	}
	add("request_id", RequestIDFromContext(ctx))
	actor, _ := ctx.Value(actorIDKey{}).(string)
	add("actor_id", actor)
	if span := trace.SpanContextFromContext(ctx); span.IsValid() {
		add("trace_id", span.TraceID().String())
		add("span_id", span.SpanID().String())
	}
	var err error
	for _, next := range h.next {
		if next.Enabled(ctx, r.Level) {
			err = errors.Join(err, next.Handle(ctx, out.Clone()))
		}
	}
	return err
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		safe[i] = redact(a)
	}
	next := make([]slog.Handler, len(h.next))
	for i, handler := range h.next {
		next[i] = handler.WithAttrs(safe)
	}
	return logHandler{next}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(h.next))
	for i, handler := range h.next {
		next[i] = handler.WithGroup(name)
	}
	return logHandler{next}
}

func redact(a slog.Attr) slog.Attr {
	if sensitiveKey.MatchString(a.Key) {
		return slog.String(a.Key, "[REDACTED]")
	}
	value := a.Value.Resolve()
	switch {
	case value.Kind() == slog.KindGroup:
		group := value.Group()
		safe := make([]any, len(group))
		for i, member := range group {
			safe[i] = redact(member)
		}
		return slog.Group(a.Key, safe...)
	case a.Key == "db.params":
		// Parameter values may be user data; only their types are logged.
		params, _ := value.Any().([]any)
		types := make([]string, len(params))
		for i, p := range params {
			types[i] = "<nil>"
			if p != nil {
				types[i] = fmt.Sprintf("<%T>", p)
			}
		}
		return slog.Any(a.Key, types)
	}
	return slog.Attr{Key: a.Key, Value: value}
}
