// Package telemetry wires OpenTelemetry (traces, metrics, logs) for the API and
// the worker, and records every SQL statement as a span and a JSON log line.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime/debug"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var logProvider *sdklog.LoggerProvider

// Setup installs the global tracer, meter and logger providers and the W3C
// propagator. Exporters follow the standard OTEL_*_EXPORTER variables, with
// console traces, Prometheus metrics and no log export by default. An
// unreachable OTLP endpoint never blocks the caller: export is batched.
func Setup(ctx context.Context, service string) (shutdown func(context.Context) error, err error) {
	defaultEnv("OTEL_TRACES_EXPORTER", "console")
	defaultEnv("OTEL_METRICS_EXPORTER", "prometheus")
	defaultEnv("OTEL_LOGS_EXPORTER", "none")

	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service), attribute.String("service.version", version())),
		resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil, err
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("telemetry export failed", "event", "telemetry.error", "error", err.Error())
	}))

	var shutdowns []func(context.Context) error
	shutdown = func(ctx context.Context) error {
		var err error
		for i := len(shutdowns) - 1; i >= 0; i-- {
			err = errors.Join(err, shutdowns[i](ctx))
		}
		return err
	}
	fail := func(err error) (func(context.Context) error, error) {
		return nil, errors.Join(err, shutdown(ctx))
	}

	spans, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return fail(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(spans))
	otel.SetTracerProvider(tp)
	shutdowns = append(shutdowns, tp.Shutdown)

	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fail(err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	shutdowns = append(shutdowns, mp.Shutdown)

	logs, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return fail(err)
	}
	if !autoexport.IsNoneLogExporter(logs) {
		logProvider = sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))
		global.SetLoggerProvider(logProvider)
		shutdowns = append(shutdowns, logProvider.Shutdown)
	}
	return shutdown, nil
}

// LogHandler is the slog bridge to the OTLP log exporter, for the logger's
// extra handler; nil when log export is off. Call it after Setup.
func LogHandler(service string) slog.Handler {
	if logProvider == nil {
		return nil
	}
	return otelslog.NewHandler(service, otelslog.WithLoggerProvider(logProvider))
}

func defaultEnv(key, value string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, value)
	}
}

func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return s.Value[:12]
		}
	}
	return "dev"
}
