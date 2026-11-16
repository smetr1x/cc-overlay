package statusline

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func parse(t *testing.T, body string) Report {
	t.Helper()
	var r Report
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLimitsAndContext(t *testing.T) {
	s := NewStore()
	reset5h := t0.Add(2 * time.Hour).Unix()
	s.Put(parse(t, fmt.Sprintf(`{"session_id":"a","model":"Sonnet",
		"context":{"used_percentage":8,"window_size":200000},
		"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":%d},"seven_day":{"used_percentage":41.2}}}`, reset5h)), t0)

	l := s.Limits(t0.Add(time.Minute))
	if l == nil || l.FiveHour == nil || l.FiveHour.UsedPercent != 23.5 || l.FiveHour.ResetsAt.Unix() != reset5h {
		t.Fatalf("лимит 5 часов: %+v", l)
	}
	if l.SevenDay == nil || l.SevenDay.UsedPercent != 41.2 || l.SevenDay.ResetsAt != nil {
		t.Errorf("лимит 7 дней: %+v", l.SevenDay)
	}
	c := s.Context("a")
	if c == nil || c.UsedPercent != 8 || *c.WindowSize != 200000 {
		t.Errorf("контекст: %+v", c)
	}
	if s.Context("unknown") != nil {
		t.Error("для неизвестной сессии контекста быть не должно")
	}

	// После сброса окна старый процент неверен и не отдаётся.
	l = s.Limits(t0.Add(3 * time.Hour))
	if l == nil || l.FiveHour != nil || l.SevenDay == nil {
		t.Errorf("после сброса 5-часового окна: %+v", l)
	}
}

func TestLimitsSurviveReportWithoutThem(t *testing.T) {
	s := NewStore()
	s.Put(parse(t, `{"session_id":"a","rate_limits":{"five_hour":{"used_percentage":10}}}`), t0)
	// Новая сессия до первого ответа API лимитов не присылает; известные значения остаются.
	s.Put(parse(t, `{"session_id":"b","context":{"used_percentage":null}}`), t0.Add(time.Minute))

	if l := s.Limits(t0.Add(2 * time.Minute)); l == nil || l.FiveHour.UsedPercent != 10 || !l.ReceivedAt.Equal(t0) {
		t.Errorf("лимиты: %+v", l)
	}
	if s.Context("b") != nil {
		t.Error("null в used_percentage — это отсутствие данных, а не ноль")
	}
	if NewStore().Limits(t0) != nil {
		t.Error("пустое хранилище должно отдавать nil")
	}
}

func TestUsageReport(t *testing.T) {
	// Форма ответа Claude Code 2.1.312 на `claude -p "/usage"`.
	body := `{"limits":[
		{"kind":"session","group":"session","percent":38,"resets_at":"2026-11-17T14:09:59.624090+00:00","scope":null,"severity":"normal","is_active":true},
		{"kind":"weekly_all","group":"weekly","percent":7,"resets_at":"2026-11-22T09:59:59.624110+00:00","scope":null},
		{"kind":"weekly_scoped","group":"weekly","percent":10,"resets_at":"2026-11-22T09:59:59.624442+00:00","scope":{"model":{"display_name":"Fable"},"surface":null}},
		{"kind":"something_new","percent":1}],
		"extra_usage":{"is_enabled":false}}`
	var u UsageReport
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatal(err)
	}
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	s := NewStore()
	s.PutUsage(u, t0)

	l := s.Limits(t0.Add(time.Minute))
	if l == nil || l.Source != "usage" {
		t.Fatalf("лимиты: %+v", l)
	}
	if l.FiveHour == nil || l.FiveHour.UsedPercent != 38 || l.FiveHour.ResetsAt.UTC().Hour() != 14 {
		t.Errorf("5 часов (kind=session): %+v", l.FiveHour)
	}
	if l.SevenDay == nil || l.SevenDay.UsedPercent != 7 {
		t.Errorf("неделя (kind=weekly_all): %+v", l.SevenDay)
	}
	if len(l.Scoped) != 1 || l.Scoped[0].Label != "Fable" || l.Scoped[0].UsedPercent != 10 {
		t.Errorf("лимит по модели: %+v", l.Scoped)
	}

	// Пятичасовое окно сбросилось: его процент больше не отдаётся, недельные остаются.
	l = s.Limits(t0.Add(3 * time.Hour))
	if l == nil || l.FiveHour != nil || l.SevenDay == nil || len(l.Scoped) != 1 {
		t.Errorf("после сброса окна: %+v", l)
	}

	// Status line обновляет общие лимиты и не стирает лимит по модели, которого сам не знает.
	s.Put(parse(t, `{"session_id":"a","rate_limits":{"five_hour":{"used_percentage":40}}}`), t0.Add(time.Minute))
	l = s.Limits(t0.Add(2 * time.Minute))
	if l.Source != "statusline" || l.FiveHour.UsedPercent != 40 || len(l.Scoped) != 1 {
		t.Errorf("после отчёта status line: %+v", l)
	}

	for name, bad := range map[string]string{
		"пусто":              `{"limits":[]}`,
		"нет kind":           `{"limits":[{"percent":1}]}`,
		"процент":            `{"limits":[{"kind":"session","percent":-5}]}`,
		"дата":               `{"limits":[{"kind":"session","percent":5,"resets_at":"завтра"}]}`,
		"null вместо списка": `{"limits":null}`,
	} {
		var u UsageReport
		if err := json.Unmarshal([]byte(bad), &u); err == nil && u.Validate() == nil {
			t.Errorf("%s: должно быть отклонено", name)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := []string{
		`{"session_id":""}`,
		`{"session_id":"a b"}`,
		`{"session_id":"a","context":{"used_percentage":-1}}`,
		`{"session_id":"a","rate_limits":{"five_hour":{"used_percentage":1e9}}}`,
	}
	for _, body := range bad {
		if parse(t, body).Validate() == nil {
			t.Errorf("должно быть отклонено: %s", body)
		}
	}
	if err := parse(t, `{"session_id":"cf88bc03-18fa-43f0-929d-0739ff853b5c","rate_limits":{"five_hour":{"used_percentage":104}}}`).Validate(); err != nil {
		t.Errorf("превышение лимита допустимо: %v", err)
	}
}

func TestStoreIsBounded(t *testing.T) {
	s := NewStore()
	for i := range 200 {
		s.Put(Report{SessionID: fmt.Sprintf("s%d", i)}, t0.Add(time.Duration(i)*time.Second))
	}
	if n := len(s.sessions); n != maxSessions {
		t.Errorf("в хранилище %d сессий, предел %d", n, maxSessions)
	}
	if _, ok := s.sessions["s199"]; !ok {
		t.Error("вытесняться должны самые старые")
	}
}
