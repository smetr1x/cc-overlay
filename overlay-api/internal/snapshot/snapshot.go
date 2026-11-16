// Package snapshot собирает снимок показателей оверлея из Prometheus и данных status line.
package snapshot

import "time"

// Status — состояние данных в целом (docs/architecture.md, «Состояния оверлея»).
type Status string

const (
	StatusOK          Status = "ok"           // есть живая сессия
	StatusIdle        Status = "idle"         // живых сессий нет, история есть
	StatusStale       Status = "stale"        // конвейер метрик не работает, числа устарели
	StatusNoData      Status = "no_data"      // данных Claude Code нет вообще
	StatusBackendDown Status = "backend_down" // Prometheus недоступен
)

// Statuses — все значения, для метрики cc_overlay_snapshot_status.
var Statuses = []Status{StatusOK, StatusIdle, StatusStale, StatusNoData, StatusBackendDown}

// Snapshot — контракт между overlay-api и оверлеем.
//
// Правило контракта: отсутствие данных передаётся как null, а не как ноль.
// Ноль означает, что значение известно и равно нулю.
type Snapshot struct {
	Status      Status    `json:"status"`
	GeneratedAt time.Time `json:"generated_at"`
	// Возраст самой свежей точки метрик Claude Code.
	DataAgeSeconds *float64 `json:"data_age_seconds"`
	// Число живых сессий; null, если Prometheus недоступен.
	ActiveSessions *int     `json:"active_sessions"`
	Session        *Session `json:"session"`
	Rate           *Rate    `json:"rate"`
	Today          *Window  `json:"today"`
	Window5h       *Window  `json:"window_5h"`
	Limits         *Limits  `json:"limits"`
	// Уровни ok / warn / crit по порогам из конфига; ключи — имена порогов.
	Levels map[string]string `json:"levels"`
}

// Session — текущая сессия: живая, с самой поздней активностью.
type Session struct {
	ID              string     `json:"id"`
	Model           *string    `json:"model"`
	Tokens          *Tokens    `json:"tokens"`
	CostUSD         *float64   `json:"cost_usd"` // оценка по ценам API, в интерфейсе подписывается «≈$»
	ActiveSeconds   *float64   `json:"active_seconds"`
	StartedAt       *time.Time `json:"started_at"`
	DurationSeconds *float64   `json:"duration_seconds"`
	// cacheRead / (input + cacheRead + cacheCreation)
	CacheHitRatio *float64 `json:"cache_hit_ratio"`
	Context       *Context `json:"context"`
}

// Tokens — итог сессии по типам. Тип, которого у сессии ещё не было, равен нулю: это известное значение.
type Tokens struct {
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	CacheRead     float64 `json:"cache_read"`
	CacheCreation float64 `json:"cache_creation"`
}

// Context — заполнение окна контекста по данным status line.
type Context struct {
	UsedPercent  float64  `json:"used_percent"`
	WindowTokens *float64 `json:"window_tokens"`
	AgeSeconds   float64  `json:"age_seconds"`
	Stale        bool     `json:"stale"`
}

// Rate — скорость расхода за последние 5 минут по всем сессиям.
type Rate struct {
	TokensPerMinute *float64 `json:"tokens_per_minute"` // без чтения кэша
	CostUSDPerHour  *float64 `json:"cost_usd_per_hour"`
}

// Window — итог за окно времени по всем сессиям.
type Window struct {
	Seconds         int      `json:"seconds"`
	Tokens          *float64 `json:"tokens"` // input + output + cacheCreation
	CacheReadTokens *float64 `json:"cache_read_tokens"`
	CostUSD         *float64 `json:"cost_usd"`
	// true, если внутри окна сбрасывался счётчик и итог неточен (ADR-0007).
	Approximate bool `json:"approximate"`
}

// Limit — одно окно лимита подписки.
type Limit struct {
	UsedPercent     float64    `json:"used_percent"`
	ResetsAt        *time.Time `json:"resets_at"`
	ResetsInSeconds *float64   `json:"resets_in_seconds"`
}

// Limits — лимиты подписки по данным status line.
type Limits struct {
	FiveHour   *Limit  `json:"five_hour"`
	SevenDay   *Limit  `json:"seven_day"`
	AgeSeconds float64 `json:"age_seconds"`
	// true, если status line давно не обновлялся: значение могло измениться.
	Stale bool `json:"stale"`
}
