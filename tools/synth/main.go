// Команда synth шлёт в коллектор синтетическую телеметрию «как Claude Code»,
// не расходуя настоящие токены. Нужна, чтобы посмотреть на живые графики
// и проверить конвейер вручную; автоматическая проверка — в каталоге e2e.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"cc-overlay/tools/synth/synth"
)

func main() {
	endpoint := flag.String("endpoint", "localhost:4317", "приёмник OTLP gRPC, host:port")
	temporality := flag.String("temporality", "cumulative", "cumulative или delta")
	sessions := flag.Int("sessions", 2, "число параллельных сессий")
	duration := flag.Duration("duration", time.Minute, "сколько работать")
	interval := flag.Duration("interval", 5*time.Second, "интервал экспорта")
	flag.Parse()

	temp, err := synth.ParseTemporality(*temporality)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	cfg := synth.Config{Endpoint: *endpoint, Temporality: temp, Interval: *interval}
	runID := time.Now().Unix()

	var wg sync.WaitGroup
	for i := range *sessions {
		id := fmt.Sprintf("synth-%d-%d", runID, i)
		s, err := synth.NewSession(ctx, id, cfg)
		if err != nil {
			log.Fatalf("сессия %s: %v", id, err)
		}
		log.Printf("сессия %s: %s, экспорт каждые %s в %s", id, temp, *interval, *endpoint)
		wg.Add(1)
		go func() {
			defer wg.Done()
			simulate(ctx, s)
			// Контекст уже отменён, поэтому остаток выгружаем с новым.
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.Close(closeCtx); err != nil {
				log.Printf("сессия %s: закрытие: %v", s.ID, err)
			}
		}()
	}
	wg.Wait()
}

// simulate изображает диалог: раз в несколько секунд «запрос к API»
// с правдоподобным соотношением типов токенов.
func simulate(ctx context.Context, s *synth.Session) {
	const model = "claude-synth-1"
	s.Start(ctx)
	// Первый запрос создаёт кэш, последующие в основном читают его.
	s.AddTokens(ctx, "cacheCreation", model, "main", 12_000+rand.Int64N(4_000))

	for {
		pause := time.Duration(2+rand.IntN(5)) * time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}

		in, out := 50+rand.Int64N(400), 100+rand.Int64N(900)
		cacheRead := 10_000 + rand.Int64N(20_000)
		s.AddTokens(ctx, "input", model, "main", in)
		s.AddTokens(ctx, "output", model, "main", out)
		s.AddTokens(ctx, "cacheRead", model, "main", cacheRead)
		// Цены условные: важен порядок величины, а не точность.
		s.AddCost(ctx, model, "main", float64(in)*3e-6+float64(out)*15e-6+float64(cacheRead)*0.3e-6)
		s.AddActiveTime(ctx, "cli", pause.Seconds())
	}
}
