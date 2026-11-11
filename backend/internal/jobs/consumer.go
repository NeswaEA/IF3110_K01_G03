package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

type ConsumerOptions struct {
	Concurrency     int           // WORKER_CONCURRENCY, default 4
	AckWait         time.Duration // JOB_ACK_WAIT, default 60s
	ShutdownTimeout time.Duration // how long Run waits for running handlers after ctx ends, default 30s
	FetchWait       time.Duration // long-poll per fetch, default 5s
}

// Consumer pulls job messages and hands each to the JobHandler. While a
// handler runs, the message is kept in progress, so a long migration is never
// redelivered to a second worker; a worker that dies stops the heartbeat and
// JetStream redelivers after AckWait.
type Consumer struct {
	js jetstream.JetStream
	h  contracts.JobHandler
	o  ConsumerOptions
}

func NewConsumer(js jetstream.JetStream, h contracts.JobHandler, o ConsumerOptions) *Consumer {
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.AckWait <= 0 {
		o.AckWait = 60 * time.Second
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 30 * time.Second
	}
	if o.FetchWait <= 0 {
		o.FetchWait = 5 * time.Second
	}
	return &Consumer{js: js, h: h, o: o}
}

// Run consumes until ctx ends. Then it stops fetching and cancels the
// handlers' context: a handler is expected to finish its current batch and
// return without Ack, so the message is released to another worker. Run
// returns once handlers are done or ShutdownTimeout passes.
func (c *Consumer) Run(ctx context.Context) error {
	cons, err := c.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       ConsumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       c.o.AckWait,
		MaxDeliver:    -1,
		MaxAckPending: 256,
	})
	if err != nil {
		return err
	}
	handlerCtx, stopHandlers := context.WithCancel(context.WithoutCancel(ctx))
	defer stopHandlers()

	var wg sync.WaitGroup
	for range c.o.Concurrency {
		wg.Go(func() {
			for ctx.Err() == nil {
				fetchCtx, cancel := context.WithTimeout(ctx, c.o.FetchWait) // long poll that also ends on shutdown
				batch, err := cons.Fetch(1, jetstream.FetchContext(fetchCtx))
				if err != nil {
					cancel()
					if ctx.Err() == nil {
						slog.WarnContext(ctx, "job fetch failed", "event", "job.fetch_failed", "error", err.Error())
						sleep(ctx, time.Second)
					}
					continue
				}
				for msg := range batch.Messages() {
					c.handle(handlerCtx, msg)
				}
				cancel()
			}
		})
	}

	<-ctx.Done()
	stopHandlers()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(c.o.ShutdownTimeout):
		slog.Warn("handlers still running at shutdown; their messages will be redelivered", "event", "worker.shutdown_timeout")
	}
	return nil
}

func (c *Consumer) handle(ctx context.Context, msg jetstream.Msg) {
	carrier := propagation.MapCarrier{} // NATS header keys are case-sensitive; the relay writes them lower-case
	for k, v := range msg.Headers() {
		if len(v) > 0 {
			carrier[k] = v[0]
		}
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := telemetry.Tracer().Start(ctx, "migration.job", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	var p payload
	if err := json.Unmarshal(msg.Data(), &p); err != nil || !contracts.ValidID(p.ExecutionID) || !contracts.ValidID(p.DatabaseID) {
		slog.WarnContext(ctx, "malformed job message dropped", "event", "job.malformed", "subject", msg.Subject())
		_ = msg.Term()
		return
	}
	span.SetAttributes(attribute.String("execution_id", p.ExecutionID), attribute.String("database_id", p.DatabaseID))
	delivered := uint64(1)
	if meta, err := msg.Metadata(); err == nil {
		delivered = meta.NumDelivered
	}
	slog.InfoContext(ctx, "job received", "event", "job.received", "execution_id", p.ExecutionID, "job_id", p.ExecutionID,
		"database_id", p.DatabaseID, "delivery", delivered)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(c.o.AckWait / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = msg.InProgress()
			}
		}
	}()

	res := c.call(ctx, contracts.JobMessage{ExecutionID: p.ExecutionID, DatabaseID: p.DatabaseID})
	span.SetAttributes(attribute.String("reason", res.Reason))
	telemetry.RecordDelivery(ctx, res.Reason)
	var err error
	if res.Ack {
		err = msg.Ack()
	} else {
		err = msg.NakWithDelay(res.Delay)
	}
	if err != nil {
		slog.WarnContext(ctx, "job acknowledgement failed", "event", "job.ack_failed", "execution_id", p.ExecutionID, "error", err.Error())
	}
}

// call runs the handler; a panic is logged and the job retried later rather
// than taking the worker down.
func (c *Consumer) call(ctx context.Context, m contracts.JobMessage) (res contracts.JobResult) {
	defer func() {
		if v := recover(); v != nil {
			slog.ErrorContext(ctx, "job handler panicked", "event", "job.panic", "execution_id", m.ExecutionID, "error", fmt.Sprint(v))
			res = contracts.JobResult{Delay: 5 * time.Second, Reason: "retry"}
		}
	}()
	return c.h.Handle(ctx, m)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
