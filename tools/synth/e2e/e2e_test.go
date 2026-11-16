//go:build e2e

// Сквозные тесты конвейера: синтетические сессии → коллектор → Prometheus.
// Запускаются внутри тестового compose-проекта: make test-e2e.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
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
	time.Sleep(5 * exportInterval)
	age, err = query(t, fmt.Sprintf(`time() - max(timestamp(%s{session_id="%s"}))`, tokens, idB))
	if err != nil {
		t.Fatal(err)
	}
	if got := age[""]; got < 4 {
		t.Errorf("завершённая сессия: возраст последней точки %.1f с, ожидалось не меньше 4 с", got)
	}
}

func TestCumulativePipeline(t *testing.T) {
	testPipeline(t, synth.Cumulative, env(t, "SYNTH_OTLP_ENDPOINT"))
}

// Запасной путь из ADR-0001: delta у источника и deltatocumulative в коллекторе.
func TestDeltaFallbackPipeline(t *testing.T) {
	testPipeline(t, synth.Delta, env(t, "SYNTH_OTLP_DELTA_ENDPOINT"))
}
