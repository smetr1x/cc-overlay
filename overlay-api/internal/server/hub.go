package server

import "sync"

// Hub раздаёт последний снимок подписчикам потока SSE.
type Hub struct {
	mu     sync.Mutex
	latest []byte
	subs   map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

// Publish сохраняет снимок и рассылает его. Медленный подписчик не тормозит остальных:
// непрочитанный снимок заменяется новым, потому что каждый снимок полный.
func (h *Hub) Publish(snapshot []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = snapshot
	for ch := range h.subs {
		select {
		case <-ch:
		default:
		}
		ch <- snapshot
	}
}

// Latest возвращает последний снимок или nil, если его ещё нет.
func (h *Hub) Latest() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latest
}

// Subscribe возвращает канал снимков и функцию отписки.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Subscribers возвращает число подписчиков.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
