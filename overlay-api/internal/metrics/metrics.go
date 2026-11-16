// Package metrics — собственные метрики overlay-api для Prometheus.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// Metrics собирает показатели работы сервиса и значения status line,
// которые иначе существовали бы только в памяти и не попадали в историю.
type Metrics struct {
	Registry *prometheus.Registry

	BuildDuration prometheus.Histogram
	QueryErrors   prometheus.Counter
	Status        *prometheus.GaugeVec
	SSEClients    prometheus.Gauge
	RateLimit     *prometheus.GaugeVec
	Context       prometheus.Gauge
	Reports       *prometheus.CounterVec
}

func New() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		BuildDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "cc_overlay_snapshot_build_duration_seconds",
			Help:    "Время сборки одного снимка, включая запросы к Prometheus.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}),
		QueryErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cc_overlay_prometheus_query_errors_total",
			Help: "Неудачные запросы к Prometheus.",
		}),
		Status: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cc_overlay_snapshot_status",
			Help: "1 для текущего статуса снимка, 0 для остальных.",
		}, []string{"status"}),
		SSEClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cc_overlay_sse_clients",
			Help: "Подключённые клиенты потока SSE.",
		}),
		RateLimit: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cc_overlay_rate_limit_used_percent",
			Help: "Израсходованная доля лимита подписки по данным status line.",
		}, []string{"window"}),
		Context: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cc_overlay_context_used_percent",
			Help: "Заполнение окна контекста текущей сессии по данным status line.",
		}),
		Reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cc_overlay_reports_total",
			Help: "Принятые и отклонённые отчёты по источнику: statusline или usage.",
		}, []string{"source", "result"}),
	}
	m.Registry.MustRegister(m.BuildDuration, m.QueryErrors, m.Status, m.SSEClients, m.RateLimit, m.Reports)
	return m
}

// SetContext публикует заполнение контекста; nil снимает метрику,
// чтобы в истории не оставалось значение уже несуществующей сессии.
func (m *Metrics) SetContext(v *float64) {
	if v == nil {
		m.Registry.Unregister(m.Context)
		return
	}
	// Повторная регистрация уже зарегистрированной метрики возвращает ошибку, это ожидаемо.
	_ = m.Registry.Register(m.Context)
	m.Context.Set(*v)
}

// SetRateLimit публикует процент лимита окна; nil снимает серию.
func (m *Metrics) SetRateLimit(window string, v *float64) {
	if v == nil {
		m.RateLimit.DeleteLabelValues(window)
		return
	}
	m.RateLimit.WithLabelValues(window).Set(*v)
}
