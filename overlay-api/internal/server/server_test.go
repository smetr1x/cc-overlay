package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cc-overlay/overlay-api/internal/metrics"
	"cc-overlay/overlay-api/internal/statusline"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func newServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{
		Hub: NewHub(), Statusline: statusline.NewStore(), Metrics: metrics.New(),
		CORSOrigins: []string{"http://tauri.localhost"},
		Now:         func() time.Time { return t0 },
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func do(t *testing.T, method, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestSnapshot(t *testing.T) {
	s, ts := newServer(t)
	if resp := do(t, "GET", ts.URL+"/api/v1/snapshot", "", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("до первого снимка: %d", resp.StatusCode)
	}
	s.Hub.Publish([]byte(`{"status":"ok"}`))
	resp := do(t, "GET", ts.URL+"/api/v1/snapshot", "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("снимок: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestCORSAllowlist(t *testing.T) {
	s, ts := newServer(t)
	s.Hub.Publish([]byte(`{}`))
	for origin, want := range map[string]string{
		"http://tauri.localhost": "http://tauri.localhost",
		"https://evil.example":   "",
		"":                       "",
	} {
		h := map[string]string{}
		if origin != "" {
			h["Origin"] = origin
		}
		resp := do(t, "GET", ts.URL+"/api/v1/snapshot", "", h)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %q: Access-Control-Allow-Origin = %q, ожидалось %q", origin, got, want)
		}
	}
}

func TestStatuslineReport(t *testing.T) {
	s, ts := newServer(t)
	url := ts.URL + "/api/v1/statusline"
	jsonH := map[string]string{"Content-Type": "application/json"}
	good := `{"session_id":"abc","rate_limits":{"five_hour":{"used_percentage":23.5}}}`

	if resp := do(t, "POST", url, good, jsonH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("корректный отчёт: %d", resp.StatusCode)
	}
	if l := s.Statusline.Limits(t0); l == nil || l.FiveHour.UsedPercent != 23.5 {
		t.Errorf("отчёт не сохранён: %+v", l)
	}

	cases := []struct {
		name, body string
		headers    map[string]string
		want       int
	}{
		{"запрос из браузера", good, map[string]string{"Content-Type": "application/json", "Origin": "http://tauri.localhost"}, 403},
		{"форма вместо JSON", good, map[string]string{"Content-Type": "text/plain"}, 415},
		{"битый JSON", `{"session_id":`, jsonH, 400},
		{"плохой session_id", `{"session_id":"a b"}`, jsonH, 400},
		{"слишком большое тело", `{"session_id":"a","model":"` + strings.Repeat("x", maxReportBytes) + `"}`, jsonH, 400},
	}
	for _, c := range cases {
		if resp := do(t, "POST", url, c.body, c.headers); resp.StatusCode != c.want {
			t.Errorf("%s: %d, ожидалось %d", c.name, resp.StatusCode, c.want)
		}
	}
	if resp := do(t, "GET", url, "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET на statusline: %d", resp.StatusCode)
	}
}

func TestStreamSendsLatestThenUpdates(t *testing.T) {
	s, ts := newServer(t)
	s.Hub.Publish([]byte(`{"n":1}`))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type: %s", ct)
	}

	lines := bufio.NewScanner(resp.Body)
	next := func() string {
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				return data
			}
		}
		t.Fatalf("поток оборвался: %v", lines.Err())
		return ""
	}
	if got := next(); got != `{"n":1}` {
		t.Errorf("первым должен прийти последний снимок: %s", got)
	}

	// Подписка уже активна: дождаться её и опубликовать обновление.
	for s.Hub.Subscribers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	s.Hub.Publish([]byte(`{"n":2}`))
	if got := next(); got != `{"n":2}` {
		t.Errorf("обновление: %s", got)
	}
}

func TestHubDropsStaleSnapshotForSlowSubscriber(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	defer cancel()
	h.Publish([]byte("1"))
	h.Publish([]byte("2")) // подписчик не успел прочитать первый
	if got := string(<-ch); got != "2" {
		t.Errorf("медленный подписчик должен получить самый свежий снимок: %s", got)
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	_, ts := newServer(t)
	if resp := do(t, "GET", ts.URL+"/healthz", "", nil); resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	if resp := do(t, "GET", ts.URL+"/metrics", "", nil); resp.StatusCode != 200 {
		t.Errorf("metrics: %d", resp.StatusCode)
	}
}
