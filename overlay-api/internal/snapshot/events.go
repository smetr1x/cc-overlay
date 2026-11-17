package snapshot

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"cc-overlay/overlay-api/internal/loki"
	"cc-overlay/overlay-api/internal/prom"
)

// Блок событий (фаза 4): последний запрос к API, ошибки и латентность из Loki.
// Источник — события claude_code.api_request и claude_code.api_error.

// Events — показатели из событий. Поле Available различает «Loki недоступен» и «событий нет».
type Events struct {
	Available   bool         `json:"available"`
	LastRequest *LastRequest `json:"last_request"`
	Errors      *Errors      `json:"errors"`
	Latency     *Latency     `json:"latency"`
}

// LastRequest — последний запрос основного диалога: текущей сессии, а без неё — любой.
type LastRequest struct {
	At                  time.Time `json:"at"`
	AgeSeconds          float64   `json:"age_seconds"`
	Model               *string   `json:"model"`
	DurationMs          *float64  `json:"duration_ms"`
	TTFTMs              *float64  `json:"ttft_ms"` // время до первого токена; в документации нет, приходит с 2.1.319
	InputTokens         *float64  `json:"input_tokens"`
	OutputTokens        *float64  `json:"output_tokens"`
	CacheReadTokens     *float64  `json:"cache_read_tokens"`
	CacheCreationTokens *float64  `json:"cache_creation_tokens"`
	CostUSD             *float64  `json:"cost_usd"`
}

// Errors — ошибки API за окно по всем сессиям.
type Errors struct {
	WindowSeconds int       `json:"window_seconds"`
	Count         float64   `json:"count"`
	Last          *APIError `json:"last"`
}

// APIError — последняя ошибка. Текст ошибки не передаётся: в нём может оказаться что угодно.
type APIError struct {
	At         time.Time `json:"at"`
	AgeSeconds float64   `json:"age_seconds"`
	StatusCode *string   `json:"status_code"` // нет для ошибок без HTTP-ответа
	Model      *string   `json:"model"`
	Attempt    *float64  `json:"attempt"`
}

// Latency — длительность запросов основного диалога за окно по всем сессиям.
type Latency struct {
	WindowSeconds int      `json:"window_seconds"`
	Requests      float64  `json:"requests"`
	P50Ms         *float64 `json:"p50_ms"`
	P95Ms         *float64 `json:"p95_ms"`
}

// EventsQuerier — то, что сборщику нужно от Loki.
type EventsQuerier interface {
	Query(ctx context.Context, expr string, at time.Time) ([]prom.Sample, error)
	Last(ctx context.Context, expr string, start, end time.Time) (*loki.Entry, error)
}

const (
	lastRequestLookback = 24 * time.Hour
	errorsWindow        = 15 * time.Minute
	latencyWindow       = time.Hour
)

// logQL строит запросы. Атрибуты событий лежат в structured metadata с «_» вместо точек.
type logQL struct{ jobRegex string }

func (q logQL) stream() string { return fmt.Sprintf(`{service_name=~"%s"}`, q.jobRegex) }

// mainRequests — запросы основного диалога: "main" в терминале, "sdk" в расширении VS Code (ADR-0011).
func (q logQL) mainRequests() string {
	return q.stream() + ` | event_name="api_request" | query_source=~"main|sdk"`
}

func (q logQL) lastRequest(sessionID string) string {
	if sessionID == "" {
		return q.mainRequests()
	}
	return q.mainRequests() + " | session_id=" + strconv.Quote(sessionID)
}

func (q logQL) apiErrors() string { return q.stream() + ` | event_name="api_error"` }

func (q logQL) errorCount() string {
	return fmt.Sprintf("sum(count_over_time(%s [%s]))", q.apiErrors(), dur(errorsWindow))
}

func (q logQL) requestCount() string {
	return fmt.Sprintf("sum(count_over_time(%s [%s]))", q.mainRequests(), dur(latencyWindow))
}

func (q logQL) latencyQuantile(phi float64) string {
	return fmt.Sprintf(`quantile_over_time(%g, %s | unwrap duration_ms | __error__="" [%s]) by ()`,
		phi, q.mainRequests(), dur(latencyWindow))
}

func dur(d time.Duration) string { return strconv.Itoa(int(d.Seconds())) + "s" }

func label(e *loki.Entry, key string) *string {
	if v, ok := e.Labels[key]; ok && v != "" {
		return &v
	}
	return nil
}

func number(e *loki.Entry, key string) *float64 {
	v, ok := e.Labels[key]
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

// eventsCache хранит последний результат: Loki опрашивается реже, чем Prometheus.
type eventsCache struct {
	at        time.Time
	sessionID string
	events    *Events
}

// fetchEvents опрашивает Loki. Недоступность Loki не ошибка сборки: Available=false.
func (b *Builder) fetchEvents(ctx context.Context, sessionID string, now time.Time) *Events {
	q := logQL{jobRegex: b.cfg.JobRegex}
	query := func(expr string) ([]prom.Sample, error) {
		ctx, cancel := context.WithTimeout(ctx, b.cfg.QueryTimeout)
		defer cancel()
		s, err := b.loki.Query(ctx, expr, now)
		if err != nil {
			b.onLokiErr()
		}
		return s, err
	}
	scalar := func(expr string) *float64 {
		s, err := query(expr)
		if err != nil || len(s) != 1 {
			return nil
		}
		return &s[0].Value
	}
	last := func(expr string, lookback time.Duration) (*loki.Entry, error) {
		ctx, cancel := context.WithTimeout(ctx, b.cfg.QueryTimeout)
		defer cancel()
		e, err := b.loki.Last(ctx, expr, now.Add(-lookback), now)
		if err != nil {
			b.onLokiErr()
		}
		return e, err
	}

	ev := &Events{}
	req, err := last(q.lastRequest(sessionID), lastRequestLookback)
	if err != nil {
		return ev // Available=false: Loki недоступен или запрос отвергнут
	}
	ev.Available = true
	if req != nil {
		ev.LastRequest = &LastRequest{
			At:                  req.Time,
			Model:               label(req, "model"),
			DurationMs:          number(req, "duration_ms"),
			TTFTMs:              number(req, "ttft_ms"),
			InputTokens:         number(req, "input_tokens"),
			OutputTokens:        number(req, "output_tokens"),
			CacheReadTokens:     number(req, "cache_read_tokens"),
			CacheCreationTokens: number(req, "cache_creation_tokens"),
			CostUSD:             number(req, "cost_usd"),
		}
	}

	// Пустой результат count_over_time означает «ни одного события»: это известный ноль.
	if s, err := query(q.errorCount()); err == nil {
		errs := &Errors{WindowSeconds: int(errorsWindow.Seconds())}
		if len(s) == 1 {
			errs.Count = s[0].Value
		}
		if errs.Count > 0 {
			if e, err := last(q.apiErrors(), errorsWindow); err == nil && e != nil {
				errs.Last = &APIError{At: e.Time, StatusCode: label(e, "status_code"), Model: label(e, "model"), Attempt: number(e, "attempt")}
			}
		}
		ev.Errors = errs
	}

	if s, err := query(q.requestCount()); err == nil {
		lat := &Latency{WindowSeconds: int(latencyWindow.Seconds())}
		if len(s) == 1 {
			lat.Requests = s[0].Value
		}
		if lat.Requests > 0 {
			lat.P50Ms = scalar(q.latencyQuantile(0.5))
			lat.P95Ms = scalar(q.latencyQuantile(0.95))
		}
		ev.Latency = lat
	}
	return ev
}

// events возвращает блок событий из кэша или обновляет его. Возраст пересчитывается на каждом снимке.
func (b *Builder) events(ctx context.Context, sessionID string, now time.Time) *Events {
	if b.loki == nil {
		return nil
	}
	c := b.eventsCache
	if c == nil || c.sessionID != sessionID || now.Sub(c.at) >= b.cfg.EventsInterval {
		c = &eventsCache{at: now, sessionID: sessionID, events: b.fetchEvents(ctx, sessionID, now)}
		b.eventsCache = c
	}
	// Копия, чтобы возраст в кэше не накапливался.
	out := *c.events
	if r := out.LastRequest; r != nil {
		cp := *r
		cp.AgeSeconds = max(0, now.Sub(cp.At).Seconds())
		out.LastRequest = &cp
	}
	if e := out.Errors; e != nil && e.Last != nil {
		cpErr, cpLast := *e, *e.Last
		cpLast.AgeSeconds = max(0, now.Sub(cpLast.At).Seconds())
		cpErr.Last = &cpLast
		out.Errors = &cpErr
	}
	return &out
}
