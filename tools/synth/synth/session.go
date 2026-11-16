// Package synth имитирует телеметрию Claude Code: те же имена метрик, единицы
// и атрибуты, что описаны в документации «Monitoring usage».
//
// Одна Session соответствует одному процессу Claude Code: у неё свой
// MeterProvider и свой session.id, как у настоящих параллельных сессий.
package synth

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

// ServiceName отличается от настоящего "claude-code", чтобы синтетику
// можно было отфильтровать по метке job.
const ServiceName = "claude-code-synth"

// Temporality — режим экспорта счётчиков.
type Temporality string

const (
	Cumulative Temporality = "cumulative"
	Delta      Temporality = "delta"
)

// ParseTemporality разбирает значение флага или переменной окружения.
func ParseTemporality(s string) (Temporality, error) {
	switch Temporality(s) {
	case Cumulative, Delta:
		return Temporality(s), nil
	}
	return "", fmt.Errorf("temporality %q: ожидается cumulative или delta", s)
}

// Config описывает, куда и как сессия экспортирует метрики.
type Config struct {
	Endpoint    string        // host:port приёмника OTLP gRPC
	Temporality Temporality   //
	Interval    time.Duration // интервал экспорта, аналог OTEL_METRIC_EXPORT_INTERVAL
	ServiceName string        // service.name ресурса; пусто — ServiceName по умолчанию
}

// Session — одна синтетическая сессия Claude Code.
type Session struct {
	ID string

	provider *sdkmetric.MeterProvider
	identity []attribute.KeyValue

	// События (сигнал logs). nil, если сессия создана без экспортёра событий.
	logs   *sdklog.LoggerProvider
	logger otellog.Logger

	tokens   metric.Int64Counter
	cost     metric.Float64Counter
	sessions metric.Int64Counter
	active   metric.Float64Counter
}

// NewSession создаёт сессию, экспортирующую по OTLP gRPC.
func NewSession(ctx context.Context, id string, cfg Config) (*Session, error) {
	temporality := metricdata.CumulativeTemporality
	if cfg.Temporality == Delta {
		temporality = metricdata.DeltaTemporality
	}
	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
		otlpmetricgrpc.WithInsecure(),
		otlpmetricgrpc.WithTemporalitySelector(
			func(sdkmetric.InstrumentKind) metricdata.Temporality { return temporality },
		),
	)
	if err != nil {
		return nil, fmt.Errorf("экспортёр OTLP: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(cfg.Interval))
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = ServiceName
	}
	s, err := newSession(id, serviceName, reader)
	if err != nil {
		return nil, err
	}

	logExporter, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(cfg.Endpoint), otlploggrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("экспортёр событий OTLP: %w", err)
	}
	s.logs = sdklog.NewLoggerProvider(
		sdklog.WithResource(sessionResource(serviceName)),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)
	s.logger = s.logs.Logger("com.anthropic.claude_code.events")
	return s, nil
}

func sessionResource(serviceName string) *resource.Resource {
	return resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", "0.0.0-synth"),
		attribute.String("os.type", "linux"),
	)
}

// newSession собирает сессию поверх произвольного reader; в тестах это ManualReader.
func newSession(id, serviceName string, reader sdkmetric.Reader) (*Session, error) {
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(sessionResource(serviceName)),
		sdkmetric.WithReader(reader),
	)
	meter := provider.Meter("com.anthropic.claude_code")

	s := &Session{
		ID:       id,
		provider: provider,
		// Стандартные атрибуты, которые Claude Code ставит на каждую точку.
		// Пять идентификаторов пользователя нужны, чтобы тест проверил их удаление в коллекторе.
		identity: []attribute.KeyValue{
			attribute.String("session.id", id),
			attribute.String("terminal.type", "synth"),
			attribute.String("user.email", "synth@example.com"),
			attribute.String("user.account_uuid", "00000000-0000-4000-8000-000000000001"),
			attribute.String("user.account_id", "user_synth"),
			attribute.String("user.id", "synth-anonymous-id"),
			attribute.String("organization.id", "00000000-0000-4000-8000-000000000002"),
		},
	}

	var err error
	if s.tokens, err = meter.Int64Counter("claude_code.token.usage", metric.WithUnit("tokens")); err != nil {
		return nil, err
	}
	if s.cost, err = meter.Float64Counter("claude_code.cost.usage", metric.WithUnit("USD")); err != nil {
		return nil, err
	}
	if s.sessions, err = meter.Int64Counter("claude_code.session.count"); err != nil {
		return nil, err
	}
	if s.active, err = meter.Float64Counter("claude_code.active_time.total", metric.WithUnit("s")); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) attrs(extra ...attribute.KeyValue) metric.MeasurementOption {
	all := make([]attribute.KeyValue, 0, len(s.identity)+len(extra))
	all = append(all, s.identity...)
	all = append(all, extra...)
	return metric.WithAttributes(all...)
}

// Start отмечает начало сессии (claude_code.session.count).
func (s *Session) Start(ctx context.Context) {
	s.sessions.Add(ctx, 1, s.attrs(attribute.String("start_type", "fresh")))
}

// AddTokens добавляет токены. tokenType: input, output, cacheRead, cacheCreation.
// querySource: main, subagent, auxiliary.
func (s *Session) AddTokens(ctx context.Context, tokenType, model, querySource string, n int64) {
	s.tokens.Add(ctx, n, s.attrs(
		attribute.String("type", tokenType),
		attribute.String("model", model),
		attribute.String("query_source", querySource),
	))
}

// AddCost добавляет оценку стоимости в долларах.
func (s *Session) AddCost(ctx context.Context, model, querySource string, usd float64) {
	s.cost.Add(ctx, usd, s.attrs(
		attribute.String("model", model),
		attribute.String("query_source", querySource),
	))
}

// AddActiveTime добавляет активное время. activityType: user или cli.
func (s *Session) AddActiveTime(ctx context.Context, activityType string, seconds float64) {
	s.active.Add(ctx, seconds, s.attrs(attribute.String("type", activityType)))
}

// APIRequest имитирует один запрос к API: событие claude_code.api_request
// с токенами по типам и те же токены в счётчике claude_code.token.usage.
func (s *Session) APIRequest(ctx context.Context, model, querySource string, input, output, cacheRead, cacheCreation int64) {
	for typ, n := range map[string]int64{"input": input, "output": output, "cacheRead": cacheRead, "cacheCreation": cacheCreation} {
		if n > 0 {
			s.AddTokens(ctx, typ, model, querySource, n)
		}
	}
	if s.logger == nil {
		return
	}
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetBody(attribute.StringValue("claude_code.api_request"))
	rec.AddAttributes(
		attribute.String("event.name", "api_request"),
		attribute.String("session.id", s.ID),
		attribute.String("user.email", "synth@example.com"),
		attribute.String("model", model),
		attribute.String("query_source", querySource),
		// Документация не говорит, числа это или строки, поэтому синтетика шлёт оба вида:
		// конвейер обязан понимать и то, и другое.
		attribute.Int64("input_tokens", input),
		attribute.Int64("output_tokens", output),
		attribute.String("cache_read_tokens", fmt.Sprint(cacheRead)),
		attribute.Int64("cache_creation_tokens", cacheCreation),
	)
	s.logger.Emit(ctx, rec)
}

// Flush экспортирует накопленное немедленно, не дожидаясь интервала.
func (s *Session) Flush(ctx context.Context) error {
	if s.logs != nil {
		if err := s.logs.ForceFlush(ctx); err != nil {
			return err
		}
	}
	return s.provider.ForceFlush(ctx)
}

// Close экспортирует остаток и завершает сессию: серии перестают обновляться,
// как при выходе из Claude Code.
func (s *Session) Close(ctx context.Context) error {
	if s.logs != nil {
		_ = s.logs.Shutdown(ctx)
	}
	return s.provider.Shutdown(ctx)
}
