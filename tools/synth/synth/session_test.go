package synth

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collect возвращает метрики сессии по имени.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func TestSessionEmitsClaudeCodeShape(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	s, err := newSession("sess-1", ServiceName, reader)
	if err != nil {
		t.Fatal(err)
	}

	s.Start(ctx)
	s.AddTokens(ctx, "input", "model-a", "main", 100)
	s.AddTokens(ctx, "input", "model-a", "main", 50)
	s.AddTokens(ctx, "output", "model-a", "main", 20)
	s.AddCost(ctx, "model-a", "main", 0.5)
	s.AddActiveTime(ctx, "cli", 3)

	got := collect(t, reader)

	for name, unit := range map[string]string{
		"claude_code.token.usage":       "tokens",
		"claude_code.cost.usage":        "USD",
		"claude_code.session.count":     "",
		"claude_code.active_time.total": "s",
	} {
		m, ok := got[name]
		if !ok {
			t.Errorf("нет метрики %s", name)
			continue
		}
		if m.Unit != unit {
			t.Errorf("%s: единица %q, ожидалась %q", name, m.Unit, unit)
		}
	}

	sum, ok := got["claude_code.token.usage"].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("token.usage: тип %T, ожидался Sum[int64]", got["claude_code.token.usage"].Data)
	}
	if !sum.IsMonotonic {
		t.Error("token.usage должен быть монотонным счётчиком")
	}

	byType := map[string]int64{}
	for _, dp := range sum.DataPoints {
		typ, _ := dp.Attributes.Value(attribute.Key("type"))
		byType[typ.AsString()] += dp.Value

		if v, _ := dp.Attributes.Value("session.id"); v.AsString() != "sess-1" {
			t.Errorf("session.id = %q", v.AsString())
		}
		// Идентификаторы обязаны быть на точке: иначе тест их удаления в коллекторе ничего не проверяет.
		for _, key := range []attribute.Key{"user.email", "user.account_uuid", "user.account_id", "user.id", "organization.id"} {
			if !dp.Attributes.HasValue(key) {
				t.Errorf("на точке нет атрибута %s", key)
			}
		}
	}
	if byType["input"] != 150 || byType["output"] != 20 {
		t.Errorf("токены по типам: %v, ожидалось input=150 output=20", byType)
	}
}

func TestParseTemporality(t *testing.T) {
	for _, ok := range []string{"cumulative", "delta"} {
		if _, err := ParseTemporality(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if _, err := ParseTemporality("gauge"); err == nil {
		t.Error("ожидалась ошибка для неизвестного значения")
	}
}
