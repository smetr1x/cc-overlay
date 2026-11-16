package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cc-overlay/overlay-api/internal/config"
	"cc-overlay/overlay-api/internal/prom"
	"cc-overlay/overlay-api/internal/queries"
	"cc-overlay/overlay-api/internal/statusline"
)

// fakeProm — поддельный Prometheus: отвечает на точные тексты запросов.
// На неизвестный запрос отдаёт пустой результат, как настоящий при отсутствии данных.
type fakeProm struct {
	results map[string][]prom.Sample
	down    bool
	calls   map[string]int
}

func (f *fakeProm) Query(_ context.Context, expr string) ([]prom.Sample, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[expr]++
	if f.down {
		return nil, errors.New("connection refused")
	}
	return f.results[expr], nil
}

func (f *fakeProm) set(expr string, samples ...prom.Sample) { f.results[expr] = samples }

func val(v float64) prom.Sample { return prom.Sample{Labels: map[string]string{}, Value: v} }
func lab(label, value string, v float64) prom.Sample {
	return prom.Sample{Labels: map[string]string{label: value}, Value: v}
}

type fixture struct {
	t     *testing.T
	prom  *fakeProm
	sl    *statusline.Store
	now   time.Time
	cfg   config.Config
	qb    queries.Builder
	b     *Builder
	qerrs int
}

// 2026-11-17 15:00 по Москве: с полуночи прошло 15 часов.
var moscow = time.FixedZone("MSK", 3*3600)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg, err := config.Load("", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = moscow
	cfg.Thresholds = map[string]config.Threshold{
		"cost_usd_per_hour": {Warn: 5, Crit: 15},
		"five_hour_percent": {Warn: 70, Crit: 90},
		"context_percent":   {Warn: 70, Crit: 90},
	}
	f := &fixture{
		t:    t,
		prom: &fakeProm{results: map[string][]prom.Sample{}},
		sl:   statusline.NewStore(),
		now:  time.Date(2026, 11, 17, 15, 0, 0, 0, moscow),
		cfg:  cfg,
		qb:   queries.Builder{JobRegex: cfg.JobRegex, PipelineJob: cfg.PipelineJob, Lookback: cfg.BaselineLookback},
	}
	f.b = NewBuilder(cfg, f.prom, f.sl, func() time.Time { return f.now }, func() { f.qerrs++ })
	f.prom.set(f.qb.PipelineUp(), val(1))
	return f
}

func (f *fixture) ts(age time.Duration) float64 {
	return float64(f.now.Add(-age).UnixMilli()) / 1000
}

// sessions задаёт сессии: возраст последней точки и суммарные токены.
func (f *fixture) sessions(s map[string]struct {
	age   time.Duration
	total float64
}) {
	var last, totals []prom.Sample
	for id, v := range s {
		last = append(last, lab("session_id", id, f.ts(v.age)))
		totals = append(totals, lab("session_id", id, v.total))
	}
	f.prom.set(f.qb.LastSampleTime(), last...)
	f.prom.set(f.qb.SessionTotals(), totals...)
}

type sess = map[string]struct {
	age   time.Duration
	total float64
}

func (f *fixture) build() Snapshot { return f.b.Build(context.Background()) }

func ptr[T any](v T) *T { return &v }

func eq[T comparable](t *testing.T, name string, got *T, want T) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: nil, ожидалось %v", name, want)
	} else if *got != want {
		t.Errorf("%s: %v, ожидалось %v", name, *got, want)
	}
}

func TestOKSession(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {2 * time.Second, 1510}})
	f.prom.set(f.qb.SessionTokensByType("a"),
		lab("type", "input", 160), lab("type", "output", 50), lab("type", "cacheRead", 1000), lab("type", "cacheCreation", 300))
	f.prom.set(f.qb.SessionCost("a"), val(0.75))
	f.prom.set(f.qb.SessionActiveTime("a"), val(42))
	f.prom.set(f.qb.SessionModelsRecent("a", rateWindow), lab("model", "sonnet", 150), lab("model", "opus", 0))
	f.prom.set(f.qb.SessionStart("a"), val(f.ts(10*time.Minute)))
	f.prom.set(f.qb.WindowTokens(rateWindow), val(500))
	f.prom.set(f.qb.WindowCost(rateWindow), val(0.5))
	f.prom.set(f.qb.WindowTokens(15*time.Hour), val(9000))
	f.prom.set(f.qb.WindowCacheRead(15*time.Hour), val(80000))
	f.prom.set(f.qb.WindowCost(15*time.Hour), val(3.5))
	f.prom.set(f.qb.WindowTokens(fiveHourWindow), val(4000))

	s := f.build()

	if s.Status != StatusOK {
		t.Fatalf("статус %s", s.Status)
	}
	eq(t, "active_sessions", s.ActiveSessions, 1)
	eq(t, "data_age_seconds", s.DataAgeSeconds, 2.0)
	if s.Session == nil || s.Session.ID != "a" {
		t.Fatalf("сессия: %+v", s.Session)
	}
	if got := *s.Session.Tokens; got != (Tokens{Input: 160, Output: 50, CacheRead: 1000, CacheCreation: 300}) {
		t.Errorf("токены: %+v", got)
	}
	eq(t, "cost_usd", s.Session.CostUSD, 0.75)
	eq(t, "active_seconds", s.Session.ActiveSeconds, 42.0)
	eq(t, "model", s.Session.Model, "sonnet")
	eq(t, "duration_seconds", s.Session.DurationSeconds, 600.0)
	eq(t, "cache_hit_ratio", s.Session.CacheHitRatio, 1000.0/1460.0)

	eq(t, "tokens_per_minute", s.Rate.TokensPerMinute, 100.0)
	eq(t, "cost_usd_per_hour", s.Rate.CostUSDPerHour, 6.0)
	if s.Levels["cost_usd_per_hour"] != "warn" {
		t.Errorf("уровень ≈$/ч при 6 и порогах 5/15: %q", s.Levels["cost_usd_per_hour"])
	}

	if s.Today == nil || s.Today.Seconds != 15*3600 {
		t.Fatalf("окно «сегодня» должно начинаться в локальную полночь: %+v", s.Today)
	}
	eq(t, "today.tokens", s.Today.Tokens, 9000.0)
	eq(t, "today.cache_read_tokens", s.Today.CacheReadTokens, 80000.0)
	eq(t, "today.cost_usd", s.Today.CostUSD, 3.5)
	eq(t, "window_5h.tokens", s.Window5h.Tokens, 4000.0)
	if s.Window5h.CostUSD != nil {
		t.Error("window_5h.cost_usd: данных не было, ожидался nil")
	}
	if s.Limits != nil || s.Session.Context != nil {
		t.Error("status line ничего не присылал: limits и context должны быть nil")
	}
}

// Главное правило контракта: отсутствие данных — это null, а не ноль.
func TestMissingDataIsNullNotZero(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {time.Second, 10}})
	// На запросы по сессии Prometheus ничего не вернул.
	raw, err := json.Marshal(f.build())
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, want := range []string{
		`"tokens":null`, `"cost_usd":null`, `"model":null`, `"cache_hit_ratio":null`,
		`"started_at":null`, `"context":null`, `"rate":null`, `"today":null`, `"window_5h":null`, `"limits":null`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("в снимке нет %s:\n%s", want, js)
		}
	}
	if strings.Contains(js, `:0,`) || strings.Contains(js, `:0}`) {
		t.Errorf("в снимке без данных появился ноль:\n%s", js)
	}
}

func TestStatuses(t *testing.T) {
	t.Run("no_data: Prometheus пуст", func(t *testing.T) {
		f := newFixture(t)
		s := f.build()
		if s.Status != StatusNoData || s.Session != nil || s.DataAgeSeconds != nil {
			t.Errorf("%+v", s)
		}
		eq(t, "active_sessions", s.ActiveSessions, 0)
	})

	t.Run("idle: сессия завершилась, история есть", func(t *testing.T) {
		f := newFixture(t)
		f.sessions(sess{"a": {40 * time.Second, 100}}) // старше двух интервалов экспорта
		s := f.build()
		if s.Status != StatusIdle || s.Session != nil {
			t.Errorf("статус %s, сессия %+v", s.Status, s.Session)
		}
		eq(t, "data_age_seconds", s.DataAgeSeconds, 40.0)
	})

	t.Run("idle: сегодня работали, сейчас серий нет", func(t *testing.T) {
		f := newFixture(t)
		f.prom.set(f.qb.WindowCost(15*time.Hour), val(1.5))
		if s := f.build(); s.Status != StatusIdle {
			t.Errorf("статус %s", s.Status)
		}
	})

	t.Run("stale: коллектор не отдаёт метрики", func(t *testing.T) {
		f := newFixture(t)
		f.prom.set(f.qb.PipelineUp(), val(0))
		f.sessions(sess{"a": {90 * time.Second, 100}})
		f.prom.set(f.qb.SessionCost("a"), val(2))
		s := f.build()
		if s.Status != StatusStale {
			t.Fatalf("статус %s", s.Status)
		}
		// Последняя известная сессия показывается, но возраст данных виден.
		if s.Session == nil || s.Session.ID != "a" {
			t.Errorf("сессия: %+v", s.Session)
		}
		eq(t, "data_age_seconds", s.DataAgeSeconds, 90.0)
	})

	t.Run("stale: цель scrape исчезла из Prometheus", func(t *testing.T) {
		f := newFixture(t)
		f.prom.set(f.qb.PipelineUp())
		if s := f.build(); s.Status != StatusStale {
			t.Errorf("статус %s", s.Status)
		}
	})

	t.Run("backend_down: Prometheus недоступен", func(t *testing.T) {
		f := newFixture(t)
		f.prom.down = true
		s := f.build()
		if s.Status != StatusBackendDown || s.ActiveSessions != nil || s.Session != nil || s.Today != nil {
			t.Errorf("%+v", s)
		}
		if f.qerrs == 0 {
			t.Error("ошибка запроса должна попасть в метрику")
		}
	})
}

func TestCurrentSessionFollowsActivity(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {time.Second, 500}, "b": {time.Second, 100}})
	// После старта сервиса активность неизвестна: берётся сессия с большим итогом.
	if s := f.build(); s.Session.ID != "a" {
		t.Fatalf("первый опрос: %s", s.Session.ID)
	}
	eq(t, "active_sessions", f.build().ActiveSessions, 2)

	f.now = f.now.Add(time.Second)
	f.sessions(sess{"a": {time.Second, 500}, "b": {time.Second, 130}})
	if s := f.build(); s.Session.ID != "b" {
		t.Errorf("после активности в b текущей должна стать b: %s", s.Session.ID)
	}

	// Новая сессия, появившаяся при работающем сервисе, сразу становится текущей.
	f.now = f.now.Add(time.Second)
	f.sessions(sess{"a": {time.Second, 500}, "b": {time.Second, 130}, "c": {time.Second, 5}})
	if s := f.build(); s.Session.ID != "c" {
		t.Errorf("новая сессия: %s", s.Session.ID)
	}

	// Сессия c завершилась: текущей снова становится последняя активная из живых.
	f.now = f.now.Add(time.Minute)
	f.sessions(sess{"a": {time.Second, 500}, "b": {time.Second, 130}, "c": {time.Minute, 5}})
	s := f.build()
	if s.Session.ID != "b" {
		t.Errorf("после завершения c: %s", s.Session.ID)
	}
	eq(t, "active_sessions", s.ActiveSessions, 2)
}

func TestSessionStartPrefersObservedAppearance(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"old": {time.Second, 10}})
	f.build()

	// Сессия new появилась при работающем сервисе: Prometheus о начале не спрашиваем.
	f.now = f.now.Add(5 * time.Second)
	appeared := f.now
	f.sessions(sess{"old": {time.Second, 10}, "new": {time.Second, 1}})
	f.build()
	f.now = f.now.Add(20 * time.Second)
	f.sessions(sess{"old": {time.Second, 10}, "new": {time.Second, 2}})
	s := f.build()
	if s.Session.ID != "new" || !s.Session.StartedAt.Equal(appeared) {
		t.Errorf("сессия %s, начало %v, ожидалось %v", s.Session.ID, s.Session.StartedAt, appeared)
	}
	eq(t, "duration_seconds", s.Session.DurationSeconds, 20.0)
	if n := f.prom.calls[f.qb.SessionStart("new")]; n != 0 {
		t.Errorf("лишний запрос начала сессии: %d", n)
	}
}

func TestModelFallsBackToSessionTotal(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {time.Second, 10}})
	// За последние 5 минут основной диалог молчал.
	f.prom.set(f.qb.SessionModelsRecent("a", rateWindow), lab("model", "sonnet", 0))
	f.prom.set(f.qb.SessionModelsTotal("a"), lab("model", "sonnet", 900), lab("model", "opus", 100))
	eq(t, "model", f.build().Session.Model, "sonnet")
}

func TestWindowApproximateOnResets(t *testing.T) {
	f := newFixture(t)
	f.prom.set(f.qb.WindowTokens(fiveHourWindow), val(100))
	f.prom.set(f.qb.WindowResets(fiveHourWindow), val(1))
	f.prom.set(f.qb.WindowTokens(15*time.Hour), val(100))
	f.prom.set(f.qb.WindowResets(15*time.Hour), val(0))
	s := f.build()
	if !s.Window5h.Approximate {
		t.Error("сброс счётчика в окне должен выставлять approximate")
	}
	if s.Today.Approximate {
		t.Error("без сбросов approximate выставляться не должен")
	}
}

func TestTotalsAreCachedAndSessionStartIsCached(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {time.Second, 10}})
	f.prom.set(f.qb.SessionStart("a"), val(f.ts(time.Minute)))
	today := f.qb.WindowTokens(15 * time.Hour)
	f.prom.set(today, val(1))

	f.build()
	f.now = f.now.Add(3 * time.Second)
	f.sessions(sess{"a": {time.Second, 10}})
	f.build()
	if n := f.prom.calls[today]; n != 1 {
		t.Errorf("итог за сегодня запрошен %d раз за 3 с при интервале 10 с", n)
	}
	if n := f.prom.calls[f.qb.SessionStart("a")]; n != 1 {
		t.Errorf("начало сессии запрошено %d раз, ожидался один", n)
	}

	f.now = f.now.Add(10 * time.Second)
	f.sessions(sess{"a": {time.Second, 10}})
	f.build()
	if n := f.prom.calls[f.qb.WindowTokens(15*time.Hour+13*time.Second)]; n != 1 {
		t.Errorf("после интервала итог должен пересчитаться с новым окном, запросов: %d", n)
	}
}

func TestContextFromTelemetry(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {time.Second, 10}})
	f.prom.set(f.qb.SessionModelsTotal("a"), lab("model", "big", 5))
	f.prom.set(f.qb.SessionContextTokens("a"), val(150000))
	f.prom.set(f.qb.SessionContextTime("a"), val(f.ts(40*time.Second)))

	// Status line молчит (расширение VS Code): оценка по телеметрии и окну из конфига.
	c := f.build().Session.Context
	if c == nil || c.Source != "telemetry" || !c.Approximate || c.Stale {
		t.Fatalf("контекст: %+v", c)
	}
	if c.UsedPercent != 75 || *c.Tokens != 150000 || *c.WindowTokens != 200000 || c.AgeSeconds != 40 {
		t.Errorf("контекст: %+v", c)
	}
	if lvl := f.build().Levels["context_percent"]; lvl != "warn" {
		t.Errorf("уровень: %q", lvl)
	}

	// Окно модели из конфига.
	f.b.cfg.ContextWindows = map[string]float64{"big": 1000000}
	if c = f.build().Session.Context; c.UsedPercent != 15 {
		t.Errorf("с окном в миллион: %+v", c)
	}

	// Свежий status line точнее и имеет приоритет.
	report := statusline.Report{SessionID: "a"}
	report.Context = &struct {
		UsedPercentage *float64 `json:"used_percentage"`
		WindowSize     *float64 `json:"window_size"`
	}{ptr(12.0), ptr(1000000.0)}
	f.sl.Put(report, f.now)
	if c = f.build().Session.Context; c.Source != "statusline" || c.UsedPercent != 12 || c.Approximate {
		t.Errorf("приоритет status line: %+v", c)
	}

	// Status line устарел: снова телеметрия, а размер окна берётся из его отчёта.
	f.now = f.now.Add(20 * time.Minute)
	f.sessions(sess{"a": {time.Second, 10}})
	f.b.cfg.ContextWindows = nil
	if c = f.build().Session.Context; c.Source != "telemetry" || *c.WindowTokens != 1000000 || c.UsedPercent != 15 {
		t.Errorf("после устаревания status line: %+v", c)
	}
}

func TestLimitsFromUsageReport(t *testing.T) {
	f := newFixture(t)
	reset := f.now.Add(time.Hour).Format(time.RFC3339Nano)
	f.sl.PutUsage(statusline.UsageReport{Limits: []statusline.UsageRow{
		{Kind: "session", Percent: 75, ResetsAt: &reset},
		{Kind: "weekly_all", Percent: 7},
		{Kind: "weekly_scoped", Percent: 10, Scope: &struct {
			Model *struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
			Surface *struct {
				DisplayName string `json:"display_name"`
			} `json:"surface"`
		}{Model: &struct {
			DisplayName string `json:"display_name"`
		}{"Fable"}}},
	}}, f.now.Add(-5*time.Second))

	l := f.build().Limits
	if l == nil || l.Source != "usage" || l.FiveHour.UsedPercent != 75 || l.SevenDay.UsedPercent != 7 {
		t.Fatalf("лимиты: %+v", l)
	}
	eq(t, "resets_in_seconds", l.FiveHour.ResetsInSeconds, 3600.0)
	if len(l.Scoped) != 1 || l.Scoped[0].Label != "Fable" || l.Scoped[0].UsedPercent != 10 {
		t.Errorf("лимит по модели: %+v", l.Scoped)
	}
	if lvl := f.build().Levels; lvl["five_hour_percent"] != "warn" || lvl["seven_day_percent"] != "" {
		// порог недельного лимита в этой фикстуре не задан
		t.Errorf("уровни: %v", lvl)
	}
}

func TestStatuslineLimitsAndContext(t *testing.T) {
	f := newFixture(t)
	f.sessions(sess{"a": {time.Second, 10}})
	reset := float64(f.now.Add(90 * time.Minute).Unix())
	report := statusline.Report{SessionID: "a"}
	report.Context = &struct {
		UsedPercentage *float64 `json:"used_percentage"`
		WindowSize     *float64 `json:"window_size"`
	}{ptr(75.0), ptr(200000.0)}
	report.RateLimits = &struct {
		FiveHour *statusline.Window `json:"five_hour"`
		SevenDay *statusline.Window `json:"seven_day"`
	}{FiveHour: &statusline.Window{UsedPercentage: ptr(93.0), ResetsAt: &reset}}
	f.sl.Put(report, f.now.Add(-30*time.Second))

	s := f.build()
	if s.Limits == nil || s.Limits.FiveHour == nil || s.Limits.SevenDay != nil {
		t.Fatalf("лимиты: %+v", s.Limits)
	}
	if s.Limits.FiveHour.UsedPercent != 93 || s.Limits.Stale || s.Limits.AgeSeconds != 30 {
		t.Errorf("лимит 5 часов: %+v, %+v", s.Limits.FiveHour, s.Limits)
	}
	eq(t, "resets_in_seconds", s.Limits.FiveHour.ResetsInSeconds, 5400.0)
	if s.Session.Context == nil || s.Session.Context.UsedPercent != 75 || s.Session.Context.Stale {
		t.Errorf("контекст: %+v", s.Session.Context)
	}
	if s.Levels["five_hour_percent"] != "crit" || s.Levels["context_percent"] != "warn" {
		t.Errorf("уровни: %v", s.Levels)
	}

	// Status line молчит дольше порога: значения остаются, но помечаются устаревшими.
	f.now = f.now.Add(15 * time.Minute)
	f.sessions(sess{"a": {time.Second, 10}})
	s = f.build()
	if !s.Limits.Stale || !s.Session.Context.Stale {
		t.Errorf("ожидалась пометка stale: %+v, %+v", s.Limits, s.Session.Context)
	}

	// Лимиты не зависят от Prometheus и видны даже при его недоступности.
	f.prom.down = true
	if s = f.build(); s.Status != StatusBackendDown || s.Limits == nil {
		t.Errorf("при недоступном Prometheus лимиты должны остаться: %s, %+v", s.Status, s.Limits)
	}
}
