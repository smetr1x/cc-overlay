// Package statusline хранит данные, которые скрипт status line Claude Code
// присылает в overlay-api: лимиты подписки и заполнение контекста (ADR-0009).
// Через OpenTelemetry эти значения не передаются.
package statusline

import (
	"errors"
	"regexp"
	"sync"
	"time"
)

// Window — одно окно лимита, как его отдаёт Claude Code.
type Window struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at"` // секунды Unix
}

// Report — тело запроса от скрипта. Скрипт пересылает только эти поля:
// пути, имена репозиториев и прочее содержимое JSON status line сюда не попадает.
type Report struct {
	SessionID string   `json:"session_id"`
	Model     string   `json:"model"`
	CostUSD   *float64 `json:"cost_usd"`
	Context   *struct {
		UsedPercentage *float64 `json:"used_percentage"`
		WindowSize     *float64 `json:"window_size"`
	} `json:"context"`
	RateLimits *struct {
		FiveHour *Window `json:"five_hour"`
		SevenDay *Window `json:"seven_day"`
	} `json:"rate_limits"`
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func validPercent(p *float64) bool { return p == nil || (*p >= 0 && *p <= 1000) }

// Validate проверяет отчёт. Проценты выше 100 допустимы: лимит можно превысить.
func (r Report) Validate() error {
	if !sessionIDPattern.MatchString(r.SessionID) {
		return errors.New("session_id: пусто или недопустимые символы")
	}
	if len(r.Model) > 128 {
		return errors.New("model: слишком длинное значение")
	}
	if r.Context != nil && !validPercent(r.Context.UsedPercentage) {
		return errors.New("context.used_percentage вне диапазона")
	}
	if r.RateLimits != nil {
		for _, w := range []*Window{r.RateLimits.FiveHour, r.RateLimits.SevenDay} {
			if w != nil && !validPercent(w.UsedPercentage) {
				return errors.New("rate_limits: used_percentage вне диапазона")
			}
		}
	}
	return nil
}

type entry struct {
	report Report
	at     time.Time
}

// Store держит последний отчёт по каждой сессии. Только память: после рестарта
// значения появятся со следующим обновлением status line.
type Store struct {
	mu       sync.Mutex
	sessions map[string]entry
	limits   *entry // последний отчёт, в котором были лимиты: они общие для аккаунта
}

const (
	maxSessions = 64
	sessionTTL  = 24 * time.Hour
)

func NewStore() *Store { return &Store{sessions: map[string]entry{}} }

// Put сохраняет отчёт, полученный в момент now.
func (s *Store) Put(r Report, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e := entry{report: r, at: now}
	s.sessions[r.SessionID] = e
	if r.RateLimits != nil && (r.RateLimits.FiveHour != nil || r.RateLimits.SevenDay != nil) {
		s.limits = &e
	}

	// Хранилище ограничено: сюда может писать любой локальный процесс.
	for id, old := range s.sessions {
		if now.Sub(old.at) > sessionTTL {
			delete(s.sessions, id)
		}
	}
	for len(s.sessions) > maxSessions {
		oldestID, oldestAt := "", now
		for id, old := range s.sessions {
			if !old.at.After(oldestAt) {
				oldestID, oldestAt = id, old.at
			}
		}
		delete(s.sessions, oldestID)
	}
}

// Limit — состояние одного окна лимита.
type Limit struct {
	UsedPercent float64
	ResetsAt    *time.Time
}

// Limits — лимиты подписки по последнему отчёту.
type Limits struct {
	FiveHour, SevenDay *Limit
	ReceivedAt         time.Time
}

func toLimit(w *Window, now time.Time) *Limit {
	if w == nil || w.UsedPercentage == nil {
		return nil
	}
	l := &Limit{UsedPercent: *w.UsedPercentage}
	if w.ResetsAt != nil {
		t := time.Unix(int64(*w.ResetsAt), 0)
		// Окно уже сбросилось, а новое значение ещё не пришло: старый процент неверен.
		if !t.After(now) {
			return nil
		}
		l.ResetsAt = &t
	}
	return l
}

// Limits возвращает лимиты или nil, если их ещё не присылали либо оба окна уже сбросились.
func (s *Store) Limits(now time.Time) *Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limits == nil {
		return nil
	}
	rl := s.limits.report.RateLimits
	out := &Limits{
		FiveHour:   toLimit(rl.FiveHour, now),
		SevenDay:   toLimit(rl.SevenDay, now),
		ReceivedAt: s.limits.at,
	}
	if out.FiveHour == nil && out.SevenDay == nil {
		return nil
	}
	return out
}

// Context — заполнение окна контекста сессии.
type Context struct {
	UsedPercent float64
	WindowSize  *float64
	ReceivedAt  time.Time
}

// Context возвращает заполнение контекста сессии или nil, если данных нет.
func (s *Store) Context(sessionID string) *Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[sessionID]
	if !ok || e.report.Context == nil || e.report.Context.UsedPercentage == nil {
		return nil
	}
	return &Context{
		UsedPercent: *e.report.Context.UsedPercentage,
		WindowSize:  e.report.Context.WindowSize,
		ReceivedAt:  e.at,
	}
}
