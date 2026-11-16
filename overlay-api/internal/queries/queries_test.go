package queries

import (
	"strings"
	"testing"
	"time"
)

var b = Builder{JobRegex: "claude-code(-desktop)?", PipelineJob: "collector-export", Lookback: 7 * 24 * time.Hour}

func TestDeltaMatchesTestedTemplate(t *testing.T) {
	// Та же форма, что проверена promtool в prometheus/tests/totals_test.yml.
	want := `sum (last_over_time(M[1200s]) - (last_over_time(M[604800s] offset 1200s) or last_over_time(M[1200s]) * 0))`
	if got := b.delta("", "M", 20*time.Minute); got != want {
		t.Errorf("шаблон разошёлся с проверенным:\n получено:  %s\n ожидалось: %s", got, want)
	}
}

func TestSelectorsFilterByJob(t *testing.T) {
	for name, q := range map[string]string{
		"LastSampleTime":       b.LastSampleTime(),
		"SessionTotals":        b.SessionTotals(),
		"SessionTokensByType":  b.SessionTokensByType("x"),
		"SessionCost":          b.SessionCost("x"),
		"SessionActiveTime":    b.SessionActiveTime("x"),
		"SessionModelsRecent":  b.SessionModelsRecent("x", time.Minute),
		"SessionModelsTotal":   b.SessionModelsTotal("x"),
		"SessionStart":         b.SessionStart("x"),
		"SessionContextTokens": b.SessionContextTokens("x"),
		"SessionContextTime":   b.SessionContextTime("x"),
		"WindowTokens":         b.WindowTokens(time.Hour),
		"WindowCacheRead":      b.WindowCacheRead(time.Hour),
		"WindowCost":           b.WindowCost(time.Hour),
		"WindowResets":         b.WindowResets(time.Hour),
	} {
		if !strings.Contains(q, `job=~"claude-code(-desktop)?"`) {
			t.Errorf("%s не фильтрует по job: %s", name, q)
		}
	}
}

func TestSessionIDIsEscaped(t *testing.T) {
	q := b.SessionCost(`a"} or vector(1) or x{y="`)
	if !strings.Contains(q, `session_id="a\"} or vector(1) or x{y=\""`) {
		t.Errorf("идентификатор сессии не экранирован: %s", q)
	}
}

func TestWindowNeverZero(t *testing.T) {
	// Сразу после полуночи окно «сегодня» короче секунды; [0s] в PromQL недопустим.
	if !strings.Contains(b.WindowCost(200*time.Millisecond), "[1s]") {
		t.Error("окно должно округляться вверх до секунды")
	}
}
