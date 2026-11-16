// Package queries строит PromQL для показателей оверлея.
// Обоснование каждого запроса — docs/queries.md и ADR-0007;
// шаблон «итог за окно» покрыт тестами в prometheus/tests/totals_test.yml.
package queries

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	tokens = "claude_code_token_usage_tokens_total"
	cost   = "claude_code_cost_usage_USD_total"
	active = "claude_code_active_time_seconds_total"
	// Не метрика Claude Code: её строит коллектор из событий api_request (ADR-0011).
	contextTokens = "claude_code_context_tokens"
)

// Builder хранит параметры, общие для всех запросов.
type Builder struct {
	JobRegex    string        // какие значения метки job считать Claude Code
	PipelineJob string        // задание scrape экспортёра коллектора
	Lookback    time.Duration // глубина поиска значения до начала окна
}

func dur(d time.Duration) string { return strconv.Itoa(max(1, int(d.Seconds()))) + "s" }

// selector собирает `metric{job=~"…", …}`.
func (b Builder) selector(metric string, matchers ...string) string {
	all := append([]string{fmt.Sprintf(`job=~"%s"`, b.JobRegex)}, matchers...)
	return metric + "{" + strings.Join(all, ", ") + "}"
}

// session возвращает matcher по идентификатору сессии.
// Идентификатор приходит из телеметрии, поэтому экранируется как строка PromQL.
func session(id string) string { return "session_id=" + strconv.Quote(id) }

// delta — шаблон «итог за окно»: значение в конце окна минус последнее значение до его начала;
// для серии, родившейся в окне, вычитается ноль.
func (b Builder) delta(by, sel string, window time.Duration) string {
	w, l := dur(window), dur(b.Lookback)
	return fmt.Sprintf("sum%s (last_over_time(%s[%s]) - (last_over_time(%s[%s] offset %s) or last_over_time(%s[%s]) * 0))",
		by, sel, w, sel, l, w, sel, w)
}

// PipelineUp: 1, если Prometheus успешно забирает метрики у коллектора.
func (b Builder) PipelineUp() string { return fmt.Sprintf(`up{job="%s"}`, b.PipelineJob) }

// LastSampleTime: время последней точки по каждой сессии. Требует send_timestamps в коллекторе.
func (b Builder) LastSampleTime() string {
	return "max by (session_id) (timestamp(" + b.selector(tokens) + "))"
}

// SessionTotals: суммарные токены по каждой сессии; по изменению этого числа определяется активность.
func (b Builder) SessionTotals() string {
	return "sum by (session_id) (" + b.selector(tokens) + ")"
}

// SessionTokensByType: итог сессии по типам токенов. Значение cumulative-счётчика уже и есть итог.
func (b Builder) SessionTokensByType(id string) string {
	return "sum by (type) (" + b.selector(tokens, session(id)) + ")"
}

// SessionCost: оценка стоимости сессии.
func (b Builder) SessionCost(id string) string {
	return "sum(" + b.selector(cost, session(id)) + ")"
}

// SessionActiveTime: активное время сессии в секундах.
func (b Builder) SessionActiveTime(id string) string {
	return "sum(" + b.selector(active, session(id)) + ")"
}

// SessionModelsRecent: токены основного диалога по моделям за окно.
func (b Builder) SessionModelsRecent(id string, window time.Duration) string {
	return b.delta(" by (model)", b.selector(tokens, session(id), `query_source="main"`), window)
}

// SessionModelsTotal: токены основного диалога по моделям за всю сессию.
func (b Builder) SessionModelsTotal(id string) string {
	return "sum by (model) (" + b.selector(tokens, session(id), `query_source="main"`) + ")"
}

// SessionStart: время первой точки сессии. Подзапрос дорогой, результат кэшируется вызывающим.
func (b Builder) SessionStart(id string) string {
	return "min(min_over_time(timestamp(" + b.selector(tokens, session(id)) + ")[24h:30s]))"
}

// SessionContextTokens: входные токены последнего запроса основного диалога сессии.
func (b Builder) SessionContextTokens(id string) string {
	return "max(" + b.selector(contextTokens, session(id)) + ")"
}

// SessionContextTime: когда был этот запрос.
func (b Builder) SessionContextTime(id string) string {
	return "max(timestamp(" + b.selector(contextTokens, session(id)) + "))"
}

// WindowTokens: токены за окно без чтения кэша.
func (b Builder) WindowTokens(window time.Duration) string {
	return b.delta("", b.selector(tokens, `type!="cacheRead"`), window)
}

// WindowCacheRead: токены чтения кэша за окно.
func (b Builder) WindowCacheRead(window time.Duration) string {
	return b.delta("", b.selector(tokens, `type="cacheRead"`), window)
}

// WindowCost: оценка стоимости за окно.
func (b Builder) WindowCost(window time.Duration) string {
	return b.delta("", b.selector(cost), window)
}

// WindowResets: число сбросов счётчиков за окно. Больше нуля — итог окна неточен.
func (b Builder) WindowResets(window time.Duration) string {
	return "sum(resets(" + b.selector(tokens) + "[" + dur(window) + "]))"
}
