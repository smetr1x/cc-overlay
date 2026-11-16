package snapshot

import (
	"cmp"
	"slices"
	"time"
)

// tracker определяет, в какой сессии была последняя активность.
// В PromQL это выразить нельзя: cumulative-точки приходят каждый интервал,
// даже когда значение не меняется, и время точки об активности не говорит.
type tracker struct {
	primed bool
	seen   map[string]trackedSession
}

type trackedSession struct {
	total      float64
	lastChange time.Time
	// Когда сессия впервые замечена; нулевое значение — она уже шла при старте сервиса.
	firstSeen time.Time
}

func newTracker() *tracker { return &tracker{seen: map[string]trackedSession{}} }

// observe учитывает текущие суммы токенов по сессиям.
func (t *tracker) observe(totals map[string]float64, now time.Time) {
	for id, total := range totals {
		prev, known := t.seen[id]
		switch {
		case !known && t.primed:
			// Сессия появилась, пока сервис работал: она и есть самая свежая.
			t.seen[id] = trackedSession{total: total, lastChange: now, firstSeen: now}
		case !known:
			// Первый опрос после старта: когда сессия была активна, неизвестно.
			t.seen[id] = trackedSession{total: total}
		case total != prev.total:
			prev.total, prev.lastChange = total, now
			t.seen[id] = prev
		}
	}
	for id := range t.seen {
		if _, ok := totals[id]; !ok {
			delete(t.seen, id)
		}
	}
	t.primed = true
}

// pick выбирает текущую сессию среди кандидатов: по времени последней активности,
// при равенстве — с большим итогом, затем по идентификатору, чтобы выбор не «прыгал».
func (t *tracker) pick(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	return slices.MaxFunc(candidates, func(a, b string) int {
		sa, sb := t.seen[a], t.seen[b]
		if c := sa.lastChange.Compare(sb.lastChange); c != 0 {
			return c
		}
		if c := cmp.Compare(sa.total, sb.total); c != 0 {
			return c
		}
		return cmp.Compare(b, a)
	})
}
