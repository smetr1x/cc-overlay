package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != time.Second || cfg.ExportInterval != 5*time.Second {
		t.Errorf("интервалы по умолчанию: %v %v", cfg.PollInterval, cfg.ExportInterval)
	}
	if cfg.StaleAfter() != 10*time.Second {
		t.Errorf("StaleAfter = %v, ожидалось два интервала экспорта", cfg.StaleAfter())
	}
	if cfg.Location != time.UTC {
		t.Errorf("часовой пояс по умолчанию: %v", cfg.Location)
	}
}

func TestFileThenEnv(t *testing.T) {
	p := write(t, "poll_interval: 2s\nexport_interval: 30s\nthresholds:\n  context_percent: {warn: 50, crit: 80}\n")
	env := map[string]string{"OVERLAY_POLL_INTERVAL": "500ms", "TZ": "Europe/Moscow", "OVERLAY_CORS_ORIGINS": "http://a, http://b"}
	cfg, err := Load(p, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != 500*time.Millisecond {
		t.Errorf("окружение должно перекрывать файл: %v", cfg.PollInterval)
	}
	if cfg.ExportInterval != 30*time.Second {
		t.Errorf("значение из файла: %v", cfg.ExportInterval)
	}
	if cfg.Location.String() != "Europe/Moscow" {
		t.Errorf("часовой пояс: %v", cfg.Location)
	}
	if len(cfg.CORSOrigins) != 2 || cfg.CORSOrigins[1] != "http://b" {
		t.Errorf("origins: %q", cfg.CORSOrigins)
	}
	if cfg.ContextWindow("any-model") != 200000 {
		t.Errorf("окно контекста по умолчанию: %v", cfg.ContextWindow("any-model"))
	}
	if got := cfg.Thresholds["context_percent"].Level(60); got != "warn" {
		t.Errorf("уровень 60 при порогах 50/80: %s", got)
	}
}

func TestRejects(t *testing.T) {
	cases := map[string]string{
		"опечатка в ключе":          "pol_interval: 1s\n",
		"нулевой интервал":          "poll_interval: 0s\n",
		"звёздочка в CORS":          "cors_origins: ['*']\n",
		"кавычка в job_regex":       "job_regex: 'a\"b'\n",
		"неизвестный пояс":          "timezone: Mars/Olympus\n",
		"длительность без единиц":   "export_interval: '5'\n",
		"нулевое окно контекста":    "context_window_tokens: 0\n",
		"отрицательное окно модели": "context_windows: {m: -1}\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body), noEnv); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

func TestContextWindowPerModel(t *testing.T) {
	cfg, err := Load(write(t, "context_window_tokens: 150000\ncontext_windows:\n  big-model: 1000000\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextWindow("big-model") != 1000000 || cfg.ContextWindow("other") != 150000 {
		t.Errorf("окна: %v %v", cfg.ContextWindow("big-model"), cfg.ContextWindow("other"))
	}
}

func TestShippedConfigMatchesDefaults(t *testing.T) {
	// config.yaml в репозитории документирует значения по умолчанию; они не должны расходиться с кодом.
	shipped, err := Load("../../config.yaml", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := Load("", noEnv)
	if shipped.PollInterval != def.PollInterval || shipped.TotalsInterval != def.TotalsInterval ||
		shipped.ExportInterval != def.ExportInterval || shipped.BaselineLookback != def.BaselineLookback ||
		shipped.ContextWindowDefault != def.ContextWindowDefault ||
		shipped.JobRegex != def.JobRegex || shipped.PipelineJob != def.PipelineJob ||
		strings.Join(shipped.CORSOrigins, ",") != strings.Join(def.CORSOrigins, ",") {
		t.Errorf("config.yaml расходится со значениями по умолчанию:\n файл: %+v\n код:  %+v", shipped, def)
	}
	if len(shipped.Thresholds) == 0 {
		t.Error("в config.yaml нет порогов")
	}
}
