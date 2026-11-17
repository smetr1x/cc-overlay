package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cc-overlay/overlay-api/internal/loki"
	"cc-overlay/overlay-api/internal/prom"
)

// fakeLoki отвечает на точные тексты запросов. Неизвестный запрос — пустой результат, как у настоящего Loki.
type fakeLoki struct {
	samples map[string][]prom.Sample
	entries map[string]*loki.Entry
	down    bool
	calls   int
	ranges  map[string][2]time.Time
}

func newFakeLoki() *fakeLoki {
	return &fakeLoki{samples: map[string][]prom.Sample{}, entries: map[string]*loki.Entry{}, ranges: map[string][2]time.Time{}}
}

func (f *fakeLoki) Query(_ context.Context, expr string, _ time.Time) ([]prom.Sample, error) {
	f.calls++
	if f.down {
		return nil, errors.New("connection refused")
	}
	return f.samples[expr], nil
}

func (f *fakeLoki) Last(_ context.Context, expr string, start, end time.Time) (*loki.Entry, error) {
	f.calls++
	if f.down {
		return nil, errors.New("connection refused")
	}
	f.ranges[expr] = [2]time.Time{start, end}
	return f.entries[expr], nil
}

func withLoki(f *fixture) (*fakeLoki, logQL) {
	fl := newFakeLoki()
	lokiErrs := 0
	f.b.WithEvents(fl, func() { lokiErrs++ })
	return fl, logQL{jobRegex: f.cfg.JobRegex}
}

func TestEventsDisabledWithoutLoki(t *testing.T) {
	f := newFixture(t)
	if e := f.build().Events; e != nil {
		t.Errorf("без Loki блок событий должен быть null: %+v", e)
	}
}

func TestEventsFromLoki(t *testing.T) {
	f := newFixture(t)
	fl, q := withLoki(f)
	f.sessions(sess{"a": {time.Second, 10}})

	fl.entries[q.lastRequest("a")] = &loki.Entry{
		Time: f.now.Add(-7 * time.Second),
		Labels: map[string]string{
			"model": "claude-sonnet-5-5", "duration_ms": "6604", "ttft_ms": "5042",
			"input_tokens": "4", "output_tokens": "229", "cache_read_tokens": "29813", "cache_creation_tokens": "9383",
			"cost_usd": "0.0428113",
		},
	}
	fl.samples[q.errorCount()] = []prom.Sample{val(2)}
	fl.entries[q.apiErrors()] = &loki.Entry{
		Time:   f.now.Add(-3 * time.Minute),
		Labels: map[string]string{"status_code": "529", "model": "claude-sonnet-5-5", "attempt": "2", "error": "Overloaded"},
	}
	fl.samples[q.requestCount()] = []prom.Sample{val(12)}
	fl.samples[q.latencyQuantile(0.5)] = []prom.Sample{val(4100)}
	fl.samples[q.latencyQuantile(0.95)] = []prom.Sample{val(9800)}

	e := f.build().Events
	if e == nil || !e.Available {
		t.Fatalf("события: %+v", e)
	}
	r := e.LastRequest
	if r == nil || *r.Model != "claude-sonnet-5-5" || *r.DurationMs != 6604 || *r.TTFTMs != 5042 || r.AgeSeconds != 7 {
		t.Fatalf("последний запрос: %+v", r)
	}
	if *r.InputTokens != 4 || *r.CacheReadTokens != 29813 || *r.CostUSD != 0.0428113 {
		t.Errorf("токены и стоимость: %+v", r)
	}
	if e.Errors == nil || e.Errors.Count != 2 || e.Errors.WindowSeconds != 900 {
		t.Fatalf("ошибки: %+v", e.Errors)
	}
	if l := e.Errors.Last; l == nil || *l.StatusCode != "529" || *l.Attempt != 2 || l.AgeSeconds != 180 {
		t.Errorf("последняя ошибка: %+v", l)
	}
	if lat := e.Latency; lat == nil || lat.Requests != 12 || *lat.P50Ms != 4100 || *lat.P95Ms != 9800 {
		t.Errorf("латентность: %+v", lat)
	}
	if lvl := f.build().Levels["api_errors_15m"]; lvl != "" {
		// в фикстуре порога для ошибок нет: уровень не выставляется
		t.Errorf("уровень без порога: %q", lvl)
	}
	// Окно поиска последнего запроса — сутки до текущего момента.
	if rg := fl.ranges[q.lastRequest("a")]; !rg[1].Equal(f.now) || f.now.Sub(rg[0]) != 24*time.Hour {
		t.Errorf("окно поиска: %v", rg)
	}
}

func TestEventsKnownZeroAndMissing(t *testing.T) {
	f := newFixture(t)
	fl, q := withLoki(f)
	// Событий нет: Loki отвечает пустыми результатами.
	e := f.build().Events
	if e == nil || !e.Available || e.LastRequest != nil {
		t.Fatalf("события: %+v", e)
	}
	if e.Errors == nil || e.Errors.Count != 0 || e.Errors.Last != nil {
		t.Errorf("ноль ошибок — известное значение: %+v", e.Errors)
	}
	if e.Latency == nil || e.Latency.Requests != 0 || e.Latency.P95Ms != nil {
		t.Errorf("без запросов перцентили — null: %+v", e.Latency)
	}
	// Без текущей сессии ищется последний запрос любой сессии.
	if _, asked := fl.ranges[q.lastRequest("")]; !asked {
		t.Error("без сессии должен запрашиваться последний запрос по всем сессиям")
	}
}

func TestEventsLokiDown(t *testing.T) {
	f := newFixture(t)
	fl, _ := withLoki(f)
	fl.down = true
	f.sessions(sess{"a": {time.Second, 10}})
	s := f.build()
	if s.Status != StatusOK {
		t.Errorf("недоступность Loki не должна менять общий статус: %s", s.Status)
	}
	if e := s.Events; e == nil || e.Available || e.LastRequest != nil || e.Errors != nil || e.Latency != nil {
		t.Errorf("события при недоступном Loki: %+v", e)
	}
}

func TestEventsCachedAndAgeTicks(t *testing.T) {
	f := newFixture(t)
	fl, q := withLoki(f)
	fl.entries[q.lastRequest("")] = &loki.Entry{Time: f.now.Add(-time.Second), Labels: map[string]string{"model": "m"}}

	f.build()
	calls := fl.calls
	f.now = f.now.Add(time.Second)
	e := f.build().Events
	if fl.calls != calls {
		t.Errorf("Loki опрошен раньше events_interval: %d запросов", fl.calls-calls)
	}
	if e.LastRequest.AgeSeconds != 2 {
		t.Errorf("возраст должен расти между опросами: %v", e.LastRequest.AgeSeconds)
	}
	f.now = f.now.Add(2 * time.Second)
	f.build()
	if fl.calls == calls {
		t.Error("после events_interval Loki должен опрашиваться снова")
	}
}

func TestLogQLEscapesSessionID(t *testing.T) {
	q := logQL{jobRegex: "claude-code(-desktop)?"}
	got := q.lastRequest(`x" or 1=1 | y="`)
	if !strings.HasSuffix(got, `| session_id="x\" or 1=1 | y=\""`) {
		t.Errorf("идентификатор не экранирован: %s", got)
	}
	if !strings.HasPrefix(got, `{service_name=~"claude-code(-desktop)?"} | event_name="api_request" | query_source=~"main|sdk"`) {
		t.Errorf("селектор: %s", got)
	}
}
