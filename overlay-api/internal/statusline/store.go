// Package statusline хранит данные Claude Code, которых нет в телеметрии OpenTelemetry:
// лимиты подписки и точное заполнение контекста. Источников два:
//
//   - скрипт status line (ADR-0009) — работает только в терминальном Claude Code;
//   - опрос команды /usage (ADR-0010) — работает при любом способе запуска, даёт только лимиты.
package statusline

import (
	"errors"
	"regexp"
	"sync"
	"time"
)

// Window — одно окно лимита в формате status line.
type Window struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at"` // секунды Unix
}

// Report — тело запроса от скрипта status line. Скрипт пересылает только эти поля:
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

func validPercent(p float64) bool { return p >= 0 && p <= 1000 }

// Validate проверяет отчёт. Проценты выше 100 допустимы: лимит можно превысить.
func (r Report) Validate() error {
	if !sessionIDPattern.MatchString(r.SessionID) {
		return errors.New("session_id: пусто или недопустимые символы")
	}
	if len(r.Model) > 128 {
		return errors.New("model: слишком длинное значение")
	}
	if r.Context != nil && r.Context.UsedPercentage != nil && !validPercent(*r.Context.UsedPercentage) {
		return errors.New("context.used_percentage вне диапазона")
	}
	if r.RateLimits != nil {
		for _, w := range []*Window{r.RateLimits.FiveHour, r.RateLimits.SevenDay} {
			if w != nil && w.UsedPercentage != nil && !validPercent(*w.UsedPercentage) {
				return errors.New("rate_limits: used_percentage вне диапазона")
			}
		}
	}
	return nil
}

// UsageReport — поле usage_report.rate_limits из ответа Claude Code на команду /usage.
// Строки классифицируются по kind, а не по подписям: так требует документация Agent SDK.
type UsageReport struct {
	Limits []UsageRow `json:"limits"`
}

// UsageRow — одна строка лимита.
type UsageRow struct {
	Kind     string  `json:"kind"` // session, weekly_all, weekly_scoped, …
	Percent  float64 `json:"percent"`
	ResetsAt *string `json:"resets_at"` // ISO 8601
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
		Surface *struct {
			DisplayName string `json:"display_name"`
		} `json:"surface"`
	} `json:"scope"`
}

const maxUsageRows = 32

// Validate проверяет отчёт /usage.
func (u UsageReport) Validate() error {
	if len(u.Limits) == 0 {
		return errors.New("limits: пустой список")
	}
	if len(u.Limits) > maxUsageRows {
		return errors.New("limits: слишком много строк")
	}
	for _, row := range u.Limits {
		if row.Kind == "" || len(row.Kind) > 64 {
			return errors.New("limits: некорректный kind")
		}
		if !validPercent(row.Percent) {
			return errors.New("limits: percent вне диапазона")
		}
		if row.ResetsAt != nil {
			if _, err := time.Parse(time.RFC3339Nano, *row.ResetsAt); err != nil {
				return errors.New("limits: resets_at не в формате ISO 8601")
			}
		}
	}
	return nil
}

// Limit — состояние одного окна лимита.
type Limit struct {
	UsedPercent float64
	ResetsAt    *time.Time
}

// ScopedLimit — недельный лимит, действующий для отдельной модели или поверхности.
type ScopedLimit struct {
	Label string
	Limit
}

// Limits — лимиты подписки по последнему отчёту.
type Limits struct {
	FiveHour, SevenDay *Limit
	Scoped             []ScopedLimit
	Source             string // "statusline" или "usage"
	ReceivedAt         time.Time
}

type sessionEntry struct {
	report Report
	at     time.Time
}

// Store держит последний отчёт по каждой сессии и последние лимиты. Только память:
// после рестарта значения появятся со следующим отчётом.
type Store struct {
	mu       sync.Mutex
	sessions map[string]sessionEntry
	limits   *Limits // лимиты общие для аккаунта, поэтому хранятся отдельно от сессий
}

const (
	maxSessions = 64
	sessionTTL  = 24 * time.Hour
)

func NewStore() *Store { return &Store{sessions: map[string]sessionEntry{}} }

func fromWindow(w *Window) *Limit {
	if w == nil || w.UsedPercentage == nil {
		return nil
	}
	l := &Limit{UsedPercent: *w.UsedPercentage}
	if w.ResetsAt != nil {
		t := time.Unix(int64(*w.ResetsAt), 0)
		l.ResetsAt = &t
	}
	return l
}

// Put сохраняет отчёт status line, полученный в момент now.
func (s *Store) Put(r Report, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[r.SessionID] = sessionEntry{report: r, at: now}
	if r.RateLimits != nil {
		five, seven := fromWindow(r.RateLimits.FiveHour), fromWindow(r.RateLimits.SevenDay)
		if five != nil || seven != nil {
			// Status line не сообщает лимиты по отдельным моделям; известные из /usage сохраняются.
			var scoped []ScopedLimit
			if s.limits != nil {
				scoped = s.limits.Scoped
			}
			s.limits = &Limits{FiveHour: five, SevenDay: seven, Scoped: scoped, Source: "statusline", ReceivedAt: now}
		}
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

// PutUsage сохраняет лимиты из отчёта /usage. Отчёт должен пройти Validate.
func (s *Store) PutUsage(u UsageReport, now time.Time) {
	l := &Limits{Source: "usage", ReceivedAt: now}
	for _, row := range u.Limits {
		lim := Limit{UsedPercent: row.Percent}
		if row.ResetsAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *row.ResetsAt); err == nil {
				lim.ResetsAt = &t
			}
		}
		switch row.Kind {
		case "session":
			l.FiveHour = &lim
		case "weekly_all":
			l.SevenDay = &lim
		case "weekly_scoped":
			label := "?"
			if row.Scope != nil {
				switch {
				case row.Scope.Model != nil:
					label = row.Scope.Model.DisplayName
				case row.Scope.Surface != nil:
					label = row.Scope.Surface.DisplayName
				}
			}
			if len(label) > 64 {
				label = label[:64]
			}
			l.Scoped = append(l.Scoped, ScopedLimit{Label: label, Limit: lim})
		}
		// Неизвестные kind пропускаются: сервер может добавлять новые.
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = l
}

// live возвращает лимит, если его окно ещё не сбросилось.
// После сброса старый процент неверен, а новый ещё не пришёл.
func live(l *Limit, now time.Time) *Limit {
	if l == nil || (l.ResetsAt != nil && !l.ResetsAt.After(now)) {
		return nil
	}
	return l
}

// Limits возвращает лимиты или nil, если их ещё не присылали либо все окна уже сбросились.
func (s *Store) Limits(now time.Time) *Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limits == nil {
		return nil
	}
	out := &Limits{
		FiveHour:   live(s.limits.FiveHour, now),
		SevenDay:   live(s.limits.SevenDay, now),
		Source:     s.limits.Source,
		ReceivedAt: s.limits.ReceivedAt,
	}
	for _, sc := range s.limits.Scoped {
		if live(&sc.Limit, now) != nil {
			out.Scoped = append(out.Scoped, sc)
		}
	}
	if out.FiveHour == nil && out.SevenDay == nil && len(out.Scoped) == 0 {
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
