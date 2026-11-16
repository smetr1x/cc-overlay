// Package config загружает настройки overlay-api: значения по умолчанию,
// поверх них файл YAML, поверх него переменные окружения.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Threshold — пороги одного показателя.
type Threshold struct {
	Warn float64 `yaml:"warn"`
	Crit float64 `yaml:"crit"`
}

// Level возвращает уровень значения: ok, warn или crit.
func (t Threshold) Level(v float64) string {
	switch {
	case v >= t.Crit:
		return "crit"
	case v >= t.Warn:
		return "warn"
	}
	return "ok"
}

// Config — итоговые настройки сервиса.
type Config struct {
	Listen           string
	PrometheusURL    string
	JobRegex         string
	PipelineJob      string
	PollInterval     time.Duration
	TotalsInterval   time.Duration
	ExportInterval   time.Duration
	QueryTimeout     time.Duration
	BaselineLookback time.Duration
	StatuslineMaxAge time.Duration
	Location         *time.Location
	CORSOrigins      []string
	Thresholds       map[string]Threshold
}

// StaleAfter — возраст последней точки, после которого сессия не считается живой.
func (c Config) StaleAfter() time.Duration { return 2 * c.ExportInterval }

// file повторяет структуру YAML. Указатели отличают «не задано» от нулевого значения.
type file struct {
	Listen           *string              `yaml:"listen"`
	PrometheusURL    *string              `yaml:"prometheus_url"`
	JobRegex         *string              `yaml:"job_regex"`
	PipelineJob      *string              `yaml:"pipeline_job"`
	PollInterval     *string              `yaml:"poll_interval"`
	TotalsInterval   *string              `yaml:"totals_interval"`
	ExportInterval   *string              `yaml:"export_interval"`
	QueryTimeout     *string              `yaml:"query_timeout"`
	BaselineLookback *string              `yaml:"baseline_lookback"`
	StatuslineMaxAge *string              `yaml:"statusline_max_age"`
	Timezone         *string              `yaml:"timezone"`
	CORSOrigins      []string             `yaml:"cors_origins"`
	Thresholds       map[string]Threshold `yaml:"thresholds"`
}

// Load читает настройки. path может быть пустым — тогда файл не используется.
// getenv передаётся явно, чтобы тесты не зависели от окружения процесса.
func Load(path string, getenv func(string) string) (Config, error) {
	// Значения по умолчанию в виде строк: дальше они проходят тот же разбор, что и пользовательские.
	listen, promURL := ":8787", "http://prometheus:9090"
	jobRegex, pipelineJob := "claude-code(-desktop)?", "collector-export"
	poll, totals, export := "1s", "10s", "5s"
	queryTimeout, lookback, slMaxAge := "3s", "168h", "10m"
	tz := ""
	f := file{
		Listen: &listen, PrometheusURL: &promURL, JobRegex: &jobRegex, PipelineJob: &pipelineJob,
		PollInterval: &poll, TotalsInterval: &totals, ExportInterval: &export,
		QueryTimeout: &queryTimeout, BaselineLookback: &lookback, StatuslineMaxAge: &slMaxAge,
		Timezone:    &tz,
		CORSOrigins: []string{"http://tauri.localhost", "tauri://localhost"},
		Thresholds:  map[string]Threshold{},
	}

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("файл настроек: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)                                             // опечатка в имени параметра — ошибка, а не молча проигнорированная строка
		if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) { // io.EOF — пустой файл
			return Config{}, fmt.Errorf("файл настроек %s: %w", path, err)
		}
	}

	for env, dst := range map[string]*string{
		"OVERLAY_LISTEN":             f.Listen,
		"OVERLAY_PROMETHEUS_URL":     f.PrometheusURL,
		"OVERLAY_JOB_REGEX":          f.JobRegex,
		"OVERLAY_POLL_INTERVAL":      f.PollInterval,
		"OVERLAY_TOTALS_INTERVAL":    f.TotalsInterval,
		"OVERLAY_EXPORT_INTERVAL":    f.ExportInterval,
		"OVERLAY_STATUSLINE_MAX_AGE": f.StatuslineMaxAge,
		"TZ":                         f.Timezone,
	} {
		if v := getenv(env); v != "" {
			*dst = v
		}
	}
	if v := getenv("OVERLAY_CORS_ORIGINS"); v != "" {
		f.CORSOrigins = strings.Split(v, ",")
	}

	cfg := Config{
		Listen:        *f.Listen,
		PrometheusURL: strings.TrimRight(*f.PrometheusURL, "/"),
		JobRegex:      *f.JobRegex,
		PipelineJob:   *f.PipelineJob,
		Thresholds:    f.Thresholds,
	}
	for name, d := range map[string]struct {
		src string
		dst *time.Duration
	}{
		"poll_interval":      {*f.PollInterval, &cfg.PollInterval},
		"totals_interval":    {*f.TotalsInterval, &cfg.TotalsInterval},
		"export_interval":    {*f.ExportInterval, &cfg.ExportInterval},
		"query_timeout":      {*f.QueryTimeout, &cfg.QueryTimeout},
		"baseline_lookback":  {*f.BaselineLookback, &cfg.BaselineLookback},
		"statusline_max_age": {*f.StatuslineMaxAge, &cfg.StatuslineMaxAge},
	} {
		v, err := time.ParseDuration(d.src)
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("%s: %q — ожидается положительная длительность, например 5s", name, d.src)
		}
		*d.dst = v
	}

	// Значения подставляются в PromQL внутри двойных кавычек.
	for name, v := range map[string]string{"job_regex": cfg.JobRegex, "pipeline_job": cfg.PipelineJob} {
		if v == "" || strings.ContainsAny(v, "\"\\\n") {
			return Config{}, fmt.Errorf("%s: пустое значение или недопустимые символы", name)
		}
	}

	loc, err := time.LoadLocation(*f.Timezone)
	if err != nil {
		return Config{}, fmt.Errorf("timezone %q: %w", *f.Timezone, err)
	}
	cfg.Location = loc

	for _, o := range f.CORSOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			return Config{}, fmt.Errorf("cors_origins: \"*\" запрещён, перечисли origin явно")
		}
		if o != "" {
			cfg.CORSOrigins = append(cfg.CORSOrigins, o)
		}
	}
	return cfg, nil
}
