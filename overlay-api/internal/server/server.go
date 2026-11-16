// Package server — HTTP-интерфейс overlay-api.
package server

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"cc-overlay/overlay-api/internal/metrics"
	"cc-overlay/overlay-api/internal/statusline"
)

// Server связывает маршруты с хранилищами.
type Server struct {
	Hub         *Hub
	Statusline  *statusline.Store
	Metrics     *metrics.Metrics
	CORSOrigins []string
	Now         func() time.Time
}

const maxReportBytes = 16 << 10

// Handler возвращает маршрутизатор сервиса.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/snapshot", s.cors(http.HandlerFunc(s.snapshot)))
	mux.Handle("GET /api/v1/stream", s.cors(http.HandlerFunc(s.stream)))
	// Без CORS: браузерным страницам писать сюда незачем, пишет только скрипт status line.
	mux.HandleFunc("POST /api/v1/statusline", s.report)
	mux.HandleFunc("POST /api/v1/usage", s.usage)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.Metrics.Registry, promhttp.HandlerOpts{}))
	return mux
}

// cors разрешает чтение только страницам с origin из списка (ADR-0006).
// Запросы без заголовка Origin (curl, сам оверлей вне браузера) проходят как обычно.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && slices.Contains(s.CORSOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) snapshot(w http.ResponseWriter, _ *http.Request) {
	latest := s.Hub.Latest()
	if latest == nil {
		http.Error(w, "первый снимок ещё не готов", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(latest)
}

// stream отдаёт Server-Sent Events: каждое событие — полный снимок.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := s.Hub.Subscribe()
	defer unsubscribe()
	s.Metrics.SSEClients.Inc()
	defer s.Metrics.SSEClients.Dec()

	send := func(snapshot []byte) bool {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", snapshot); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	// Сколько ждать перед переподключением, и сразу последний снимок, чтобы клиент не ждал опроса.
	fmt.Fprint(w, "retry: 2000\n\n")
	if latest := s.Hub.Latest(); latest != nil && !send(latest) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case snapshot := <-ch:
			if !send(snapshot) {
				return
			}
		}
	}
}

// accept — общая часть приёма отчётов: проверки запроса, разбор, проверка содержимого, учёт в метрике.
// store вызывается только для корректного отчёта.
func accept[T interface{ Validate() error }](s *Server, source string, w http.ResponseWriter, r *http.Request, store func(T)) {
	reject := func(code int, msg string) {
		s.Metrics.Reports.WithLabelValues(source, "rejected").Inc()
		http.Error(w, msg, code)
	}
	// Страница в браузере может отправить POST на localhost без разрешения CORS.
	// Требование application/json делает такой запрос «непростым», а Origin выдаёт браузер.
	if r.Header.Get("Origin") != "" {
		reject(http.StatusForbidden, "запросы из браузера не принимаются")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		reject(http.StatusUnsupportedMediaType, "ожидается Content-Type: application/json")
		return
	}
	var body T
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBytes)).Decode(&body); err != nil {
		reject(http.StatusBadRequest, "тело запроса не разобрано")
		return
	}
	if err := body.Validate(); err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}
	store(body)
	s.Metrics.Reports.WithLabelValues(source, "accepted").Inc()
	w.WriteHeader(http.StatusNoContent)
}

// report принимает отчёт скрипта status line.
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	accept(s, "statusline", w, r, func(rep statusline.Report) { s.Statusline.Put(rep, s.Now()) })
}

// usage принимает лимиты подписки из ответа Claude Code на команду /usage.
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	accept(s, "usage", w, r, func(u statusline.UsageReport) { s.Statusline.PutUsage(u, s.Now()) })
}

// healthz отвечает, что процесс жив. Состояние данных — в поле status снимка.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}
