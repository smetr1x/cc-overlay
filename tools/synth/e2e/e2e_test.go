//go:build e2e

// Сквозные тесты конвейера: синтетические сессии → коллектор → Prometheus.
// Запускаются внутри тестового compose-проекта: make test-e2e.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cc-overlay/tools/synth/synth"
)

const (
	tokens = "claude_code_token_usage_tokens_total"
	cost   = "claude_code_cost_usage_USD_total"

	exportInterval = time.Second
	deadline       = 45 * time.Second
)

func env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("переменная окружения %s не задана; запускай через make test-e2e", key)
	}
	return v
}

// --- клиент Prometheus ---

type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// query выполняет мгновенный запрос и возвращает значения по ключу из меток,
// например `session_id=a,type=input`.
func query(t *testing.T, expr string) (map[string]float64, error) {
	t.Helper()
	u := env(t, "PROM_URL") + "/api/v1/query?query=" + url.QueryEscape(expr)
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", pr.Error)
	}
	out := map[string]float64{}
	for _, r := range pr.Data.Result {
		keys := make([]string, 0, len(r.Metric))
		for k, v := range r.Metric {
			keys = append(keys, k+"="+v)
		}
		slices.Sort(keys)
		var val float64
		if _, err := fmt.Sscanf(r.Value[1].(string), "%g", &val); err != nil {
			return nil, err
		}
		out[strings.Join(keys, ",")] = val
	}
	return out, nil
}

// series возвращает наборы меток всех серий, подходящих под селектор.
func series(t *testing.T, selector string) []map[string]string {
	t.Helper()
	u := env(t, "PROM_URL") + "/api/v1/series?match[]=" + url.QueryEscape(selector)
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

func equal(got, want map[string]float64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || math.Abs(g-w) > 1e-9 {
			return false
		}
	}
	return true
}

// eventually повторяет запрос, пока результат не совпадёт с ожидаемым:
// точке нужно время, чтобы пройти коллектор и scrape.
func eventually(t *testing.T, name, expr string, want map[string]float64) {
	t.Helper()
	var got map[string]float64
	var err error
	for start := time.Now(); time.Since(start) < deadline; time.Sleep(500 * time.Millisecond) {
		if got, err = query(t, expr); err == nil && equal(got, want) {
			return
		}
	}
	t.Errorf("%s\n  запрос:    %s\n  ожидалось: %v\n  получено:  %v (ошибка: %v)", name, expr, want, got, err)
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	for start := time.Now(); time.Since(start) < deadline; time.Sleep(300 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return
		}
	}
	t.Fatalf("%s не отвечает", addr)
}

// --- сценарий ---

const (
	sonnet = "claude-synth-large"
	haiku  = "claude-synth-small"
)

// runTwoSessions изображает две параллельные сессии с известными числами.
// Сессии остаются открытыми и возвращаются вызывающему.
func runTwoSessions(t *testing.T, cfg synth.Config, idA, idB string) (a, b *synth.Session) {
	t.Helper()
	ctx := context.Background()
	var err error
	if a, err = synth.NewSession(ctx, idA, cfg); err != nil {
		t.Fatal(err)
	}
	if b, err = synth.NewSession(ctx, idB, cfg); err != nil {
		t.Fatal(err)
	}
	flush := func(s *synth.Session) {
		if err := s.Flush(ctx); err != nil {
			t.Errorf("flush %s: %v", s.ID, err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.Start(ctx)
		a.AddTokens(ctx, "input", sonnet, "main", 100)
		a.AddTokens(ctx, "output", sonnet, "main", 20)
		a.AddTokens(ctx, "cacheRead", sonnet, "main", 1000)
		a.AddTokens(ctx, "cacheCreation", sonnet, "main", 300)
		a.AddCost(ctx, sonnet, "main", 0.5)
		a.AddActiveTime(ctx, "cli", 4)
		flush(a)
		time.Sleep(1500 * time.Millisecond)
		// Второй шаг: прирост существующих серий и новая серия (другая модель, субагент).
		a.AddTokens(ctx, "input", sonnet, "main", 50)
		a.AddTokens(ctx, "output", sonnet, "main", 30)
		a.AddTokens(ctx, "input", haiku, "subagent", 10)
		a.AddCost(ctx, sonnet, "main", 0.25)
		flush(a)
	}()
	go func() {
		defer wg.Done()
		b.Start(ctx)
		b.AddTokens(ctx, "input", sonnet, "main", 7)
		b.AddTokens(ctx, "output", sonnet, "main", 3)
		b.AddCost(ctx, sonnet, "main", 0.01)
		flush(b)
		time.Sleep(1500 * time.Millisecond)
		b.AddTokens(ctx, "cacheRead", sonnet, "main", 400)
		flush(b)
	}()
	wg.Wait()
	return a, b
}

func testPipeline(t *testing.T, temporality synth.Temporality, endpoint string) {
	waitTCP(t, endpoint)

	run := fmt.Sprintf("%s-%d", temporality, time.Now().UnixNano())
	idA, idB := "e2e-a-"+run, "e2e-b-"+run
	sel := fmt.Sprintf(`session_id=~"%s|%s"`, idA, idB)

	cfg := synth.Config{Endpoint: endpoint, Temporality: temporality, Interval: exportInterval}
	a, b := runTwoSessions(t, cfg, idA, idB)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.Close(ctx)
		b.Close(ctx)
	})

	key := func(id, typ string) string { return "session_id=" + id + ",type=" + typ }
	wantTokens := map[string]float64{
		key(idA, "input"):         160, // 100 + 50 основной диалог, 10 субагент
		key(idA, "output"):        50,
		key(idA, "cacheRead"):     1000,
		key(idA, "cacheCreation"): 300,
		key(idB, "input"):         7,
		key(idB, "output"):        3,
		key(idB, "cacheRead"):     400,
	}

	// Итог за сессию: docs/queries.md, «Текущая сессия».
	eventually(t, "итог сессий по типам токенов",
		fmt.Sprintf(`sum by (session_id, type) (last_over_time(%s{%s}[5m]))`, tokens, sel),
		wantTokens)

	eventually(t, "стоимость по сессиям",
		fmt.Sprintf(`sum by (session_id) (last_over_time(%s{%s}[5m]))`, cost, sel),
		map[string]float64{"session_id=" + idA: 0.75, "session_id=" + idB: 0.01})

	// Итог за окно: все серии родились внутри окна, значит учитываются целиком,
	// включая первое значение, которое increase() потерял бы.
	eventually(t, "итог за окно по шаблону ΔW",
		fmt.Sprintf(`sum by (session_id) (
			last_over_time(%[1]s{%[2]s}[5m])
			- (last_over_time(%[1]s{%[2]s}[7d] offset 5m) or last_over_time(%[1]s{%[2]s}[5m]) * 0)
		)`, tokens, sel),
		map[string]float64{"session_id=" + idA: 1510, "session_id=" + idB: 410})

	eventually(t, "счётчик сессий",
		fmt.Sprintf(`sum(last_over_time(claude_code_session_count_total{%s}[5m]))`, sel),
		map[string]float64{"": 2})

	eventually(t, "активное время",
		fmt.Sprintf(`sum(last_over_time(claude_code_active_time_seconds_total{%s}[5m]))`, sel),
		map[string]float64{"": 4})

	// Приватность (ADR-0008): ни одного идентификатора пользователя в хранилище.
	all := series(t, "{"+sel+"}")
	if len(all) == 0 {
		t.Fatal("серии сессий не найдены")
	}
	forbidden := []string{"user_email", "user_account_uuid", "user_account_id", "user_id", "organization_id"}
	labelNames, metricNames := map[string]bool{}, map[string]bool{}
	for _, s := range all {
		metricNames[s["__name__"]] = true
		for name := range s {
			labelNames[name] = true
			if slices.Contains(forbidden, name) {
				t.Errorf("идентификатор %s дошёл до Prometheus в серии %s", name, s["__name__"])
			}
		}
		if s["job"] != synth.ServiceName {
			t.Errorf("метка job = %q, ожидалась %q (service.name источника)", s["job"], synth.ServiceName)
		}
	}
	names := make([]string, 0, len(labelNames))
	for n := range labelNames {
		names = append(names, n)
	}
	slices.Sort(names)
	t.Logf("метки серий в Prometheus: %s", strings.Join(names, ", "))
	mnames := make([]string, 0, len(metricNames))
	for n := range metricNames {
		mnames = append(mnames, n)
	}
	slices.Sort(mnames)
	t.Logf("имена метрик в Prometheus: %s", strings.Join(mnames, ", "))

	if temporality != synth.Cumulative {
		return
	}
	// Cumulative: живая сессия переэкспортирует точки каждый интервал, даже когда молчит.
	// На этом построено различие статусов idle и stale (docs/architecture.md).
	time.Sleep(4 * exportInterval)
	age, err := query(t, fmt.Sprintf(`time() - max(timestamp(%s{session_id="%s"}))`, tokens, idA))
	if err != nil {
		t.Fatal(err)
	}
	if got := age[""]; got > 3 {
		t.Errorf("молчащая живая сессия: возраст последней точки %.1f с, ожидалось меньше 3 с", got)
	}

	// После завершения процесса точки перестают обновляться.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Точное время остановки зависит от задержек конвейера, поэтому ждём,
	// пока возраст не перерастёт несколько интервалов: у живой сессии он не превышает одного-двух.
	var got float64
	for start := time.Now(); time.Since(start) < deadline; time.Sleep(500 * time.Millisecond) {
		age, err = query(t, fmt.Sprintf(`time() - max(timestamp(%s{session_id="%s"}))`, tokens, idB))
		if err != nil {
			t.Fatal(err)
		}
		if got = age[""]; got >= 4 {
			return
		}
	}
	t.Errorf("завершённая сессия: возраст последней точки %.1f с и не растёт", got)
}

func TestCumulativePipeline(t *testing.T) {
	testPipeline(t, synth.Cumulative, env(t, "SYNTH_OTLP_ENDPOINT"))
}

// Запасной путь из ADR-0001: delta у источника и deltatocumulative в коллекторе.
func TestDeltaFallbackPipeline(t *testing.T) {
	testPipeline(t, synth.Delta, env(t, "SYNTH_OTLP_DELTA_ENDPOINT"))
}

// --- overlay-api ---

// snapshotService — service.name, по которому тестовый overlay-api отбирает сессии
// (OVERLAY_JOB_REGEX в compose.e2e.yaml). Сессии остальных тестов он не видит.
const snapshotService = "claude-code-snapshot-test"

func getSnapshot(t *testing.T) (map[string]any, error) {
	t.Helper()
	resp, err := http.Get(env(t, "OVERLAY_API_URL") + "/api/v1/snapshot")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var snap map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// dig достаёт вложенное значение: dig(snap, "session", "tokens", "input").
func dig(v any, path ...string) any {
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

// snapshotWait ждёт до timeout, пока все перечисленные поля снимка примут ожидаемые значения.
// Возвращает последний снимок и описание расхождения; пустая строка — совпало.
func snapshotWait(t *testing.T, want map[string]any, timeout time.Duration) (map[string]any, string) {
	t.Helper()
	var snap map[string]any
	var err error
	var mismatch string
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(500 * time.Millisecond) {
		if snap, err = getSnapshot(t); err != nil {
			mismatch = err.Error()
			continue
		}
		mismatch = ""
		for path, w := range want {
			got := dig(snap, strings.Split(path, ".")...)
			gf, gok := got.(float64)
			wf, wok := w.(float64)
			if gok && wok && math.Abs(gf-wf) < 1e-9 {
				continue
			}
			if got != w {
				mismatch = fmt.Sprintf("%s = %v, ожидалось %v", path, got, w)
				break
			}
		}
		if mismatch == "" {
			return snap, ""
		}
	}
	return snap, mismatch
}

// snapshotEventually — то же, но расхождение по истечении срока завершает тест.
func snapshotEventually(t *testing.T, name string, want map[string]any) map[string]any {
	t.Helper()
	snap, mismatch := snapshotWait(t, want, deadline)
	if mismatch != "" {
		raw, _ := json.MarshalIndent(snap, "  ", "  ")
		t.Fatalf("%s: %s\n  снимок: %s", name, mismatch, raw)
	}
	return snap
}

// Сквозной путь до оверлея: synth → коллектор → Prometheus → overlay-api.
func TestSnapshot(t *testing.T) {
	endpoint := env(t, "SYNTH_OTLP_ENDPOINT")
	waitTCP(t, endpoint)
	ctx := context.Background()

	// До первых данных: явный статус, а не нули.
	snapshotEventually(t, "пустой стек", map[string]any{"status": "no_data", "session": nil, "today": nil, "rate": nil})

	run := fmt.Sprint(time.Now().UnixNano())
	idA, idB := "snap-a-"+run, "snap-b-"+run
	cfg := synth.Config{Endpoint: endpoint, Temporality: synth.Cumulative, Interval: exportInterval, ServiceName: snapshotService}
	a, b := runTwoSessions(t, cfg, idA, idB)
	defer a.Close(ctx)

	// Итоги по всем сессиям: A 1510 токенов (из них 1000 чтение кэша), B 410 (из них 400).
	snapshotEventually(t, "две живые сессии", map[string]any{
		"status":                   "ok",
		"active_sessions":          2.0,
		"today.tokens":             520.0,
		"today.cache_read_tokens":  1400.0,
		"today.cost_usd":           0.76,
		"today.approximate":        false,
		"window_5h.tokens":         520.0,
		"rate.tokens_per_minute":   104.0, // 520 за окно 5 минут
		"rate.cost_usd_per_hour":   9.12,  // 0.76 × 12
		"levels.cost_usd_per_hour": "warn",
	})

	// Активность в B делает её текущей сессией.
	time.Sleep(2 * time.Second)
	b.AddTokens(ctx, "output", sonnet, "main", 40)
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snap := snapshotEventually(t, "текущая сессия следует за активностью", map[string]any{
		"session.id":                    idB,
		"session.model":                 sonnet,
		"session.tokens.input":          7.0,
		"session.tokens.output":         43.0,
		"session.tokens.cache_read":     400.0,
		"session.tokens.cache_creation": 0.0,
		"session.cost_usd":              0.01,
		"session.cache_hit_ratio":       400.0 / 407.0,
		"today.tokens":                  560.0,
		"limits":                        nil,
		"session.context":               nil,
	})
	if age, _ := dig(snap, "data_age_seconds").(float64); age > 3 {
		t.Errorf("data_age_seconds = %.1f при живых сессиях", age)
	}
	if dur, ok := dig(snap, "session", "duration_seconds").(float64); !ok || dur <= 0 || dur > 120 {
		t.Errorf("session.duration_seconds = %v", dig(snap, "session", "duration_seconds"))
	}

	// Мост status line (ADR-0009): отчёт скрипта появляется в снимке.
	report := fmt.Sprintf(`{"session_id":%q,"model":"Synth","context":{"used_percentage":75,"window_size":200000},
		"rate_limits":{"five_hour":{"used_percentage":93,"resets_at":%d},"seven_day":{"used_percentage":41.5,"resets_at":%d}}}`,
		idB, time.Now().Add(time.Hour).Unix(), time.Now().Add(72*time.Hour).Unix())
	resp, err := http.Post(env(t, "OVERLAY_API_URL")+"/api/v1/statusline", "application/json", bytes.NewBufferString(report))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("отчёт status line: HTTP %d", resp.StatusCode)
	}
	snapshotEventually(t, "данные status line в снимке", map[string]any{
		"limits.five_hour.used_percent": 93.0,
		"limits.seven_day.used_percent": 41.5,
		"limits.stale":                  false,
		"session.context.used_percent":  75.0,
		"session.context.window_tokens": 200000.0,
		"levels.five_hour_percent":      "crit",
		"levels.context_percent":        "warn",
	})

	// Лимиты из ответа на /usage (ADR-0010) заменяют лимиты status line, включая лимит по модели.
	usage := fmt.Sprintf(`{"limits":[
		{"kind":"session","percent":38,"resets_at":%q},
		{"kind":"weekly_all","percent":7,"resets_at":%q},
		{"kind":"weekly_scoped","percent":10,"resets_at":%q,"scope":{"model":{"display_name":"Fable"},"surface":null}}]}`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		time.Now().Add(72*time.Hour).UTC().Format(time.RFC3339Nano),
		time.Now().Add(72*time.Hour).UTC().Format(time.RFC3339Nano))
	resp, err = http.Post(env(t, "OVERLAY_API_URL")+"/api/v1/usage", "application/json", bytes.NewBufferString(usage))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("отчёт /usage: HTTP %d", resp.StatusCode)
	}
	snap = snapshotEventually(t, "лимиты из /usage в снимке", map[string]any{
		"limits.source":                 "usage",
		"limits.five_hour.used_percent": 38.0,
		"limits.seven_day.used_percent": 7.0,
		"levels.five_hour_percent":      "ok",
	})
	if scoped, _ := dig(snap, "limits", "scoped").([]any); len(scoped) != 1 || dig(scoped[0], "label") != "Fable" || dig(scoped[0], "used_percent") != 10.0 {
		t.Errorf("limits.scoped = %v", dig(snap, "limits", "scoped"))
	}

	// Обе сессии завершены: статус idle, блок сессии пуст, итоги за день остаются.
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	snapshotEventually(t, "после завершения сессий", map[string]any{
		"status":          "idle",
		"active_sessions": 0.0,
		"session":         nil,
		"today.tokens":    560.0,
	})

	// Собственные метрики сервиса попадают в Prometheus: проценты лимитов сохраняются в истории.
	eventually(t, "лимит в Prometheus (последним пришёл отчёт /usage)",
		`cc_overlay_rate_limit_used_percent{window="five_hour"}`,
		map[string]float64{`__name__=cc_overlay_rate_limit_used_percent,instance=overlay-api:8787,job=overlay-api,window=five_hour`: 38})
}

// Заполнение контекста для сессий без status line (расширение VS Code), ADR-0011:
// событие api_request → метрика в коллекторе → оценка в снимке.
func TestContextFromEvents(t *testing.T) {
	endpoint := env(t, "SYNTH_OTLP_ENDPOINT")
	waitTCP(t, endpoint)
	ctx := context.Background()

	id := fmt.Sprintf("ctx-%d", time.Now().UnixNano())
	cfg := synth.Config{Endpoint: endpoint, Temporality: synth.Cumulative, Interval: exportInterval, ServiceName: snapshotService}
	s, err := synth.NewSession(ctx, id, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	// Первый запрос основного диалога: 2000 + 30000 + 8000 = 40000 токенов на входе.
	// query_source "sdk" — так основной диалог помечает расширение VS Code.
	s.APIRequest(ctx, sonnet, "sdk", 2000, 500, 30000, 8000)
	// Вспомогательный запрос и субагент контекст основного диалога не описывают.
	s.APIRequest(ctx, haiku, "auxiliary", 100, 20, 0, 0)
	s.APIRequest(ctx, sonnet, "subagent", 500, 20, 90000, 0)
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snapshotEventually(t, "оценка контекста по событию", map[string]any{
		"session.id":                    id,
		"session.context.source":        "telemetry",
		"session.context.approximate":   true,
		"session.context.tokens":        40000.0,
		"session.context.window_tokens": 200000.0,
		"session.context.used_percent":  20.0,
		"levels.context_percent":        "ok",
	})

	// Контекст вырос: метрика показывает последний запрос, а не сумму.
	// Запрос повторяется, если значение не дошло. Точка с более ранним временем, чем предыдущая,
	// отбрасывается Prometheus, а часы WSL иногда переводятся назад на секунду-другую;
	// в жизни такое значение исправляет следующий же запрос.
	want := map[string]any{
		"session.context.tokens":       150000.0,
		"session.context.used_percent": 75.0,
		"levels.context_percent":       "warn",
	}
	var mismatch string
	for attempt := 1; attempt <= 5; attempt++ {
		s.APIRequest(ctx, sonnet, "main", 1000, 0, 139000, 10000)
		if err := s.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		if _, mismatch = snapshotWait(t, want, 8*time.Second); mismatch == "" {
			break
		}
		t.Logf("попытка %d: %s", attempt, mismatch)
	}
	if mismatch != "" {
		t.Fatalf("контекст после следующего запроса: %s", mismatch)
	}

	// Приватность: метка user_email из события в метрику не попала.
	for _, labels := range series(t, fmt.Sprintf(`claude_code_context_tokens{session_id="%s"}`, id)) {
		for name := range labels {
			if strings.HasPrefix(name, "user_") || name == "organization_id" {
				t.Errorf("идентификатор %s в метрике контекста", name)
			}
		}
	}
}

// --- Loki ---

// lokiQuery выполняет метрический запрос LogQL и возвращает единственное значение.
func lokiQuery(t *testing.T, expr string) (float64, bool) {
	t.Helper()
	resp, err := http.Get(env(t, "LOKI_URL") + "/loki/api/v1/query?query=" + url.QueryEscape(expr))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("ответ Loki на %s: HTTP %d, %v", expr, resp.StatusCode, err)
	}
	if len(body.Data.Result) != 1 {
		return 0, false
	}
	var v float64
	fmt.Sscanf(body.Data.Result[0].Value[1].(string), "%g", &v)
	return v, true
}

// lokiLabels возвращает метки и structured metadata последних записей сессии.
func lokiLabels(t *testing.T, sessionID string) []map[string]string {
	t.Helper()
	q := fmt.Sprintf(`{service_name=%q} | session_id=%q`, snapshotService, sessionID)
	start := time.Now().Add(-time.Hour).UnixNano()
	resp, err := http.Get(fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&limit=100",
		env(t, "LOKI_URL"), url.QueryEscape(q), start))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, r := range body.Data.Result {
		out = append(out, r.Stream)
	}
	return out
}

// События: synth → коллектор → Loki → overlay-api (фаза 4, ADR-0005).
func TestEvents(t *testing.T) {
	endpoint := env(t, "SYNTH_OTLP_ENDPOINT")
	waitTCP(t, endpoint)
	ctx := context.Background()

	id := fmt.Sprintf("events-%d", time.Now().UnixNano())
	cfg := synth.Config{Endpoint: endpoint, Temporality: synth.Cumulative, Interval: exportInterval, ServiceName: snapshotService}
	s, err := synth.NewSession(ctx, id, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	s.APIRequestTimed(ctx, sonnet, "sdk", 10, 100, 1000, 0, 2000)
	s.APIError(ctx, sonnet, 529, 1)
	s.APIRequestTimed(ctx, sonnet, "subagent", 10, 100, 1000, 0, 60000) // не основной диалог: в латентность не входит
	s.APIRequestTimed(ctx, sonnet, "main", 20, 300, 2000, 500, 9000)
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	snap := snapshotEventually(t, "события в снимке", map[string]any{
		"session.id":                       id,
		"events.available":                 true,
		"events.last_request.model":        sonnet,
		"events.last_request.duration_ms":  9000.0,
		"events.last_request.ttft_ms":      4500.0,
		"events.last_request.input_tokens": 20.0,
		"events.errors.count":              1.0,
		"events.errors.last.status_code":   "529",
		"events.errors.last.attempt":       1.0,
		"levels.api_errors_15m":            "warn",
	})
	// Латентность считается по всем сессиям за час, а другие тесты тоже шлют запросы,
	// поэтому проверяется только то, что не зависит от их количества.
	if n, _ := dig(snap, "events", "latency", "requests").(float64); n < 2 {
		t.Errorf("events.latency.requests = %v, ожидалось не меньше 2", n)
	}
	p50, ok50 := dig(snap, "events", "latency", "p50_ms").(float64)
	p95, ok95 := dig(snap, "events", "latency", "p95_ms").(float64)
	if !ok50 || !ok95 || p95 < p50 || p95 > 9000 {
		t.Errorf("перцентили: p50=%v p95=%v; запрос субагента (60 с) не должен входить", p50, p95)
	}

	// Приватность (ADR-0008): ни идентификаторов, ни содержимого в Loki.
	entries := lokiLabels(t, id)
	if len(entries) == 0 {
		t.Fatal("записей сессии в Loki нет")
	}
	names := map[string]bool{}
	for _, labels := range entries {
		for name := range labels {
			names[name] = true
			if strings.HasPrefix(name, "user_") || name == "organization_id" || name == "tool_parameters" {
				t.Errorf("в Loki попал атрибут %s", name)
			}
		}
	}
	keys := make([]string, 0, len(names))
	for n := range names {
		keys = append(keys, n)
	}
	slices.Sort(keys)
	t.Logf("метки и structured metadata в Loki: %s", strings.Join(keys, ", "))
	for _, want := range []string{"service_name", "event_name", "session_id", "query_source", "duration_ms"} {
		if !names[want] {
			t.Errorf("в Loki нет поля %s, на котором построены запросы", want)
		}
	}
}

// Запросы всех панелей дашбордов выполняются без ошибок на настоящих Prometheus и Loki.
// Макросы Grafana заменяются характерными значениями.
func TestDashboardQueries(t *testing.T) {
	files, err := filepath.Glob("../../../grafana/provisioning/dashboards/json/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("дашборды не найдены: %v", err)
	}
	macros := strings.NewReplacer("$__range", "1h", "$__interval", "1h", "$__auto", "5m", "$__rate_interval", "5m")
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var dash struct {
			Panels []struct {
				Title      string `json:"title"`
				Datasource struct {
					UID string `json:"uid"`
				} `json:"datasource"`
				Targets []struct {
					Expr string `json:"expr"`
				} `json:"targets"`
			} `json:"panels"`
		}
		if err := json.Unmarshal(raw, &dash); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		checked := 0
		for _, p := range dash.Panels {
			for _, target := range p.Targets {
				if target.Expr == "" {
					continue
				}
				expr := macros.Replace(target.Expr)
				var u string
				switch p.Datasource.UID {
				case "prometheus":
					u = env(t, "PROM_URL") + "/api/v1/query?query=" + url.QueryEscape(expr)
				case "loki":
					// Запрос журнала (без агрегации) выполняется как query_range.
					u = env(t, "LOKI_URL") + "/loki/api/v1/query_range?limit=1&query=" + url.QueryEscape(expr)
				default:
					t.Errorf("%s / %s: неизвестный источник %q", filepath.Base(file), p.Title, p.Datasource.UID)
					continue
				}
				resp, err := http.Get(u)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"status":"success"`)) {
					t.Errorf("%s / %s: HTTP %d\n  запрос: %s\n  ответ: %.300s", filepath.Base(file), p.Title, resp.StatusCode, expr, body)
				}
				checked++
			}
		}
		t.Logf("%s: проверено запросов: %d", filepath.Base(file), checked)
	}
}
