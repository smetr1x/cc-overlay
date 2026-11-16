// Команда overlay-api считает показатели оверлея по данным Prometheus
// и отдаёт их снимком и потоком SSE. Контракт — docs/api.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // в образе нет базы часовых поясов, она встраивается в бинарник

	"cc-overlay/overlay-api/internal/config"
	"cc-overlay/overlay-api/internal/metrics"
	"cc-overlay/overlay-api/internal/prom"
	"cc-overlay/overlay-api/internal/server"
	"cc-overlay/overlay-api/internal/snapshot"
	"cc-overlay/overlay-api/internal/statusline"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "проверить работающий сервис и выйти (для healthcheck контейнера)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load(os.Getenv("OVERLAY_CONFIG"), os.Getenv)
	if err != nil {
		log.Error("настройки", "error", err)
		os.Exit(2)
	}
	if *healthcheck {
		os.Exit(runHealthcheck(cfg.Listen))
	}
	if err := run(cfg, log); err != nil {
		log.Error("сервис остановлен с ошибкой", "error", err)
		os.Exit(1)
	}
}

// runHealthcheck нужен, потому что в образе нет ни shell, ни curl.
func runHealthcheck(listen string) int {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 1
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m := metrics.New()
	store := statusline.NewStore()
	hub := server.NewHub()
	builder := snapshot.NewBuilder(cfg, prom.New(cfg.PrometheusURL), store, time.Now, m.QueryErrors.Inc)

	var lastStatus snapshot.Status
	publish := func() {
		start := time.Now()
		snap := builder.Build(ctx)
		m.BuildDuration.Observe(time.Since(start).Seconds())

		for _, st := range snapshot.Statuses {
			v := 0.0
			if st == snap.Status {
				v = 1
			}
			m.Status.WithLabelValues(string(st)).Set(v)
		}
		var five, seven, contextPct *float64
		if snap.Limits != nil {
			if l := snap.Limits.FiveHour; l != nil {
				five = &l.UsedPercent
			}
			if l := snap.Limits.SevenDay; l != nil {
				seven = &l.UsedPercent
			}
		}
		if snap.Session != nil && snap.Session.Context != nil {
			contextPct = &snap.Session.Context.UsedPercent
		}
		m.SetRateLimit("five_hour", five)
		m.SetRateLimit("seven_day", seven)
		m.SetContext(contextPct)

		if snap.Status != lastStatus {
			log.Info("статус изменился", "from", lastStatus, "to", snap.Status)
			lastStatus = snap.Status
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			log.Error("снимок не сериализован", "error", err)
			return
		}
		hub.Publish(raw)
	}

	// Первый снимок до начала приёма запросов: /snapshot никогда не отвечает пустотой.
	publish()
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()

	srv := &server.Server{Hub: hub, Statusline: store, Metrics: m, CORSOrigins: cfg.CORSOrigins, Now: time.Now}
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// WriteTimeout не задаётся: поток SSE живёт сколько угодно.
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	log.Info("overlay-api запущен", "listen", cfg.Listen, "prometheus", cfg.PrometheusURL,
		"poll_interval", cfg.PollInterval.String(), "timezone", cfg.Location.String())

	select {
	case err := <-errCh:
		return fmt.Errorf("http: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Открытые потоки SSE сами не завершатся, поэтому по истечении срока соединения закрываются.
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return httpServer.Close()
}
