// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// tracer — спаны relay: запросы API и passthrough-соединения (без содержимого:
// relay его не расшифровывает). Без InitTracing — no-op.
var tracer = otel.Tracer("github.com/gate4ai/gate4ai/relay")

// InitTracing включает экспорт трейсов OpenTelemetry по OTLP/HTTP на endpoint
// (host:port, без TLS — коллектор на localhost) с долей выборки ratio.
// Контекст трассировки из входящих запросов не принимается (пропагатор не
// задан): клиенты relay не должны влиять на выборку. Возвращает функцию
// остановки, дописывающую буфер.
func InitTracing(ctx context.Context, endpoint string, ratio float64, version string) (func(context.Context) error, error) {
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure())
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("gate4ai-relay"), semconv.ServiceVersion(version)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(ratio)))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// traceAPI оборачивает API спанами; /api/health (проверки деплоя) не трассируется.
func traceAPI(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "relay.api",
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/api/health" }))
}
