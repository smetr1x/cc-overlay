package snapshot

import (
	"context"
	"math"
	"time"

	"cc-overlay/overlay-api/internal/config"
	"cc-overlay/overlay-api/internal/prom"
	"cc-overlay/overlay-api/internal/queries"
	"cc-overlay/overlay-api/internal/statusline"
)

// Querier — то, что Builder требует от Prometheus.
type Querier interface {
	Query(ctx context.Context, expr string) ([]prom.Sample, error)
}

const (
	rateWindow     = 5 * time.Minute
	fiveHourWindow = 5 * time.Hour
)

// Builder собирает снимки. Не потокобезопасен: Build вызывает один опрашивающий цикл.
type Builder struct {
	cfg        config.Config
	q          Querier
	qb         queries.Builder
	statusline *statusline.Store
	now        func() time.Time
	onQueryErr func()

	loki        EventsQuerier // nil — Loki не настроен
	onLokiErr   func()
	eventsCache *eventsCache

	tracker *tracker
	// Момент начала сессии не меняется, а запрос за ним дорогой.
	startedAt map[string]time.Time
	// Итоги за длинные окна читают часы истории и пересчитываются реже остального.
	totals   *windowTotals
	totalsAt time.Time
}

type windowTotals struct {
	today, fiveHour *Window
}

// WithEvents подключает Loki. onErr вызывается на каждый неудачный запрос к нему (для метрики).
func (b *Builder) WithEvents(q EventsQuerier, onErr func()) *Builder {
	if onErr == nil {
		onErr = func() {}
	}
	b.loki, b.onLokiErr = q, onErr
	return b
}

// NewBuilder создаёт сборщик. onQueryErr вызывается на каждый неудачный запрос (для метрики).
func NewBuilder(cfg config.Config, q Querier, sl *statusline.Store, now func() time.Time, onQueryErr func()) *Builder {
	if onQueryErr == nil {
		onQueryErr = func() {}
	}
	return &Builder{
		cfg: cfg, q: q, statusline: sl, now: now, onQueryErr: onQueryErr,
		qb:        queries.Builder{JobRegex: cfg.JobRegex, PipelineJob: cfg.PipelineJob, Lookback: cfg.BaselineLookback},
		tracker:   newTracker(),
		startedAt: map[string]time.Time{},
	}
}

func (b *Builder) query(ctx context.Context, expr string) ([]prom.Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, b.cfg.QueryTimeout)
	defer cancel()
	samples, err := b.q.Query(ctx, expr)
	if err != nil {
		b.onQueryErr()
	}
	return samples, err
}

// scalar возвращает значение запроса с единственным результатом.
// Пустой результат, ошибка и NaN дают nil: «нет данных», а не ноль.
func (b *Builder) scalar(ctx context.Context, expr string) *float64 {
	samples, err := b.query(ctx, expr)
	if err != nil || len(samples) != 1 || math.IsNaN(samples[0].Value) || math.IsInf(samples[0].Value, 0) {
		return nil
	}
	return &samples[0].Value
}

// byLabel возвращает значения, сгруппированные по одной метке; nil при ошибке.
func (b *Builder) byLabel(ctx context.Context, expr, label string) map[string]float64 {
	samples, err := b.query(ctx, expr)
	if err != nil {
		return nil
	}
	out := make(map[string]float64, len(samples))
	for _, s := range samples {
		out[s.Labels[label]] = s.Value
	}
	return out
}

// Build собирает снимок. Ошибок не возвращает: недоступность данных — это статус снимка.
func (b *Builder) Build(ctx context.Context) (snap Snapshot) {
	now := b.now()
	snap = Snapshot{GeneratedAt: now, Levels: map[string]string{}}
	// Лимиты и события приходят не из Prometheus и показываются при любом его состоянии.
	// Результат именованный, чтобы отложенный вызов дополнял именно возвращаемый снимок.
	defer func() {
		b.addLimits(&snap, now)
		sessionID := ""
		if snap.Session != nil {
			sessionID = snap.Session.ID
		}
		snap.Events = b.events(ctx, sessionID, now)
		if e := snap.Events; e != nil && e.Errors != nil {
			b.level(&snap, "api_errors_15m", e.Errors.Count)
		}
	}()

	// Первый запрос заодно проверяет доступность Prometheus.
	upSamples, err := b.query(ctx, b.qb.PipelineUp())
	if err != nil {
		snap.Status = StatusBackendDown
		return snap
	}
	pipelineUp := len(upSamples) == 1 && upSamples[0].Value == 1

	lastSample := b.byLabel(ctx, b.qb.LastSampleTime(), "session_id")
	totals := b.byLabel(ctx, b.qb.SessionTotals(), "session_id")
	if lastSample == nil || totals == nil {
		snap.Status = StatusBackendDown
		return snap
	}
	b.tracker.observe(totals, now)

	var live, recent []string
	var newest float64
	for id, ts := range lastSample {
		recent = append(recent, id)
		newest = math.Max(newest, ts)
		if now.Sub(time.UnixMilli(int64(ts*1000))) <= b.cfg.StaleAfter() {
			live = append(live, id)
		}
	}
	if len(recent) > 0 {
		age := math.Max(0, now.Sub(time.UnixMilli(int64(newest*1000))).Seconds())
		snap.DataAgeSeconds = &age
	}
	activeSessions := len(live)
	snap.ActiveSessions = &activeSessions

	b.refreshTotals(ctx, now)
	snap.Today, snap.Window5h = b.totals.today, b.totals.fiveHour
	snap.Rate = b.rate(ctx)

	// Когда конвейер стоит, живых сессий не видно; показываем последнюю известную как устаревшую.
	candidates := live
	if !pipelineUp {
		candidates = recent
	}
	if id := b.tracker.pick(candidates); id != "" {
		snap.Session = b.session(ctx, id, now)
	}

	hasHistory := snap.Today != nil || snap.Window5h != nil || len(recent) > 0
	switch {
	case !pipelineUp:
		snap.Status = StatusStale
	case len(live) > 0:
		snap.Status = StatusOK
	case hasHistory:
		snap.Status = StatusIdle
	default:
		snap.Status = StatusNoData
	}

	if snap.Rate != nil && snap.Rate.CostUSDPerHour != nil {
		b.level(&snap, "cost_usd_per_hour", *snap.Rate.CostUSDPerHour)
	}
	if snap.Session != nil && snap.Session.Context != nil {
		b.level(&snap, "context_percent", snap.Session.Context.UsedPercent)
	}
	return snap
}

func (b *Builder) level(snap *Snapshot, name string, v float64) {
	if th, ok := b.cfg.Thresholds[name]; ok {
		snap.Levels[name] = th.Level(v)
	}
}

func (b *Builder) session(ctx context.Context, id string, now time.Time) *Session {
	s := &Session{ID: id}

	if byType := b.byLabel(ctx, b.qb.SessionTokensByType(id), "type"); len(byType) > 0 {
		t := &Tokens{
			Input: byType["input"], Output: byType["output"],
			CacheRead: byType["cacheRead"], CacheCreation: byType["cacheCreation"],
		}
		s.Tokens = t
		if in := t.Input + t.CacheRead + t.CacheCreation; in > 0 {
			ratio := t.CacheRead / in
			s.CacheHitRatio = &ratio
		}
	}
	s.CostUSD = b.scalar(ctx, b.qb.SessionCost(id))
	s.ActiveSeconds = b.scalar(ctx, b.qb.SessionActiveTime(id))

	// Модель основного диалога: по недавней активности, а если сессия молчит — по общему итогу.
	// Вспомогательные запросы (query_source != main) модель не определяют.
	s.Model = topKey(b.byLabel(ctx, b.qb.SessionModelsRecent(id, rateWindow), "model"), true)
	if s.Model == nil {
		s.Model = topKey(b.byLabel(ctx, b.qb.SessionModelsTotal(id), "model"), false)
	}

	// Начало сессии. Если она появилась при работающем сервисе, момент известен с точностью
	// до интервала опроса. Иначе — время первой точки из Prometheus; подзапрос идёт с шагом 30 с,
	// поэтому для совсем новой сессии он может ещё ничего не вернуть.
	start, cached := b.startedAt[id]
	if first := b.tracker.seen[id].firstSeen; !cached && !first.IsZero() {
		start, cached = first, true
		b.startedAt[id] = start
	}
	if !cached {
		if ts := b.scalar(ctx, b.qb.SessionStart(id)); ts != nil {
			start, cached = time.UnixMilli(int64(*ts*1000)), true
			if len(b.startedAt) > 256 {
				clear(b.startedAt)
			}
			b.startedAt[id] = start
		}
	}
	if cached {
		d := math.Max(0, now.Sub(start).Seconds())
		s.StartedAt, s.DurationSeconds = &start, &d
	}

	s.Context = b.context(ctx, id, s.Model, now)
	return s
}

// context выбирает источник заполнения контекста: свежий status line точнее,
// телеметрия работает там, где status line нет (расширение VS Code).
func (b *Builder) context(ctx context.Context, id string, model *string, now time.Time) *Context {
	var fromStatusline *Context
	var window *float64
	if c := b.statusline.Context(id); c != nil {
		age := now.Sub(c.ReceivedAt)
		window = c.WindowSize
		fromStatusline = &Context{
			UsedPercent: c.UsedPercent, WindowTokens: c.WindowSize, Source: "statusline",
			AgeSeconds: age.Seconds(), Stale: age > b.cfg.StatuslineMaxAge,
		}
		if !fromStatusline.Stale {
			return fromStatusline
		}
	}

	tokens := b.scalar(ctx, b.qb.SessionContextTokens(id))
	if tokens == nil {
		return fromStatusline // устаревшее значение лучше, чем никакого; оно помечено stale
	}
	if window == nil {
		name := ""
		if model != nil {
			name = *model
		}
		size := b.cfg.ContextWindow(name)
		window = &size
	}
	c := &Context{
		UsedPercent: *tokens / *window * 100, Tokens: tokens, WindowTokens: window,
		Source: "telemetry", Approximate: true,
	}
	if ts := b.scalar(ctx, b.qb.SessionContextTime(id)); ts != nil {
		c.AgeSeconds = math.Max(0, now.Sub(time.UnixMilli(int64(*ts*1000))).Seconds())
	}
	return c
}

// topKey возвращает ключ с наибольшим значением. При positiveOnly нулевые значения не считаются.
func topKey(m map[string]float64, positiveOnly bool) *string {
	var best *string
	var bestV float64
	for k, v := range m {
		if positiveOnly && v <= 0 {
			continue
		}
		if best == nil || v > bestV || (v == bestV && k < *best) {
			k := k
			best, bestV = &k, v
		}
	}
	return best
}

func (b *Builder) rate(ctx context.Context) *Rate {
	r := &Rate{}
	if v := b.scalar(ctx, b.qb.WindowTokens(rateWindow)); v != nil {
		perMin := *v / rateWindow.Minutes()
		r.TokensPerMinute = &perMin
	}
	if v := b.scalar(ctx, b.qb.WindowCost(rateWindow)); v != nil {
		perHour := *v / rateWindow.Hours()
		r.CostUSDPerHour = &perHour
	}
	if r.TokensPerMinute == nil && r.CostUSDPerHour == nil {
		return nil
	}
	return r
}

// sinceMidnight возвращает время, прошедшее с локальной полуночи.
func sinceMidnight(now time.Time, loc *time.Location) time.Duration {
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return local.Sub(midnight)
}

func (b *Builder) refreshTotals(ctx context.Context, now time.Time) {
	if b.totals != nil && now.Sub(b.totalsAt) < b.cfg.TotalsInterval {
		return
	}
	b.totals = &windowTotals{
		today:    b.window(ctx, sinceMidnight(now, b.cfg.Location)),
		fiveHour: b.window(ctx, fiveHourWindow),
	}
	b.totalsAt = now
}

// window считает итог за окно; nil, если в окне нет данных.
func (b *Builder) window(ctx context.Context, d time.Duration) *Window {
	w := &Window{
		Seconds:         int(d.Seconds()),
		Tokens:          b.scalar(ctx, b.qb.WindowTokens(d)),
		CacheReadTokens: b.scalar(ctx, b.qb.WindowCacheRead(d)),
		CostUSD:         b.scalar(ctx, b.qb.WindowCost(d)),
	}
	if w.Tokens == nil && w.CacheReadTokens == nil && w.CostUSD == nil {
		return nil
	}
	if resets := b.scalar(ctx, b.qb.WindowResets(d)); resets != nil && *resets > 0 {
		w.Approximate = true
	}
	return w
}

func (b *Builder) addLimits(snap *Snapshot, now time.Time) {
	l := b.statusline.Limits(now)
	if l == nil {
		return
	}
	age := now.Sub(l.ReceivedAt)
	out := &Limits{Source: l.Source, AgeSeconds: age.Seconds(), Stale: age > b.cfg.StatuslineMaxAge}
	conv := func(in *statusline.Limit, threshold string) *Limit {
		if in == nil {
			return nil
		}
		lim := &Limit{UsedPercent: in.UsedPercent, ResetsAt: in.ResetsAt}
		if in.ResetsAt != nil {
			left := in.ResetsAt.Sub(now).Seconds()
			lim.ResetsInSeconds = &left
		}
		if threshold != "" {
			b.level(snap, threshold, in.UsedPercent)
		}
		return lim
	}
	out.FiveHour = conv(l.FiveHour, "five_hour_percent")
	out.SevenDay = conv(l.SevenDay, "seven_day_percent")
	for _, sc := range l.Scoped {
		out.Scoped = append(out.Scoped, ScopedLimit{Label: sc.Label, Limit: *conv(&sc.Limit, "")})
	}
	snap.Limits = out
}
