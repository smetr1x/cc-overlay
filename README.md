# cc-overlay

Живой мониторинг расхода Claude Code: оверлей поверх окон Windows и история в Grafana. Всё работает локально, наружу ничего не уходит.

Состояние: готова фаза 1 — пайплайн метрик. Оверлея пока нет. Техническое задание — [SPEC.md](SPEC.md), архитектура — [docs/architecture.md](docs/architecture.md), решения — [docs/adr/](docs/adr/).

## Быстрый старт

Нужны Docker Engine с Compose и `make`. Go, Node и прочее ставить не нужно: тесты и линтеры идут в контейнерах.

```bash
make up            # коллектор и Prometheus; при первом запуске создаёт .env
# добавь блок env в ~/.claude/settings.json (ниже) и перезапусти Claude Code
make verify-real   # проверить, что метрики настоящей сессии дошли
make up-history    # по желанию: Grafana на http://127.0.0.1:3000 (admin, пароль из .env)
```

### Блок `env` для `~/.claude/settings.json`

Именно в пользовательском файле: в `.claude/settings.json` проекта переменные экспортёра игнорируются. Если ключ `env` уже есть, добавь строки в него.

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4317",
    "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "cumulative",
    "OTEL_METRIC_EXPORT_INTERVAL": "5000",
    "OTEL_METRICS_INCLUDE_ACCOUNT_UUID": "false"
  }
}
```

Что здесь важно:

- `…TEMPORALITY_PREFERENCE=cumulative` — на этом построены все итоги ([ADR-0001](docs/adr/0001-temporality.md)). Без неё метрики в основной конвейер попадут некорректно.
- `OTEL_METRIC_EXPORT_INTERVAL=5000` — по умолчанию 60 секунд, для оверлея слишком редко.
- `OTEL_LOGS_EXPORTER` пока не задаётся: события понадобятся в фазе 4.
- Не задавай `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT`, `OTEL_LOG_RAW_API_BODIES`, `OTEL_LOG_MANAGED_SETTINGS`: они включают сбор содержимого.

Настройки читаются при старте Claude Code, после правки нужен перезапуск сессии.

## Команды

| Команда | Что делает |
|---|---|
| `make up` / `make up-history` | запустить ядро / ядро с Grafana |
| `make down` | остановить; данные в томах сохраняются |
| `make logs`, `make ps` | логи и состояние контейнеров |
| `make check` | проверить конфиги: compose, коллектор, Prometheus, YAML, Dockerfile |
| `make test` | тесты семантики PromQL и unit-тесты генератора |
| `make test-e2e` | сквозной тест на синтетике в отдельном compose-проекте |
| `make verify-real` | проверить данные настоящей сессии Claude Code |
| `make synth` | минута синтетики в рабочий стек, для просмотра графиков |

`make test-e2e` рабочие данные не трогает: у него свои тома и сеть. `make synth`, наоборот, пишет в рабочий Prometheus; его серии помечены `job="claude-code-synth"`.

## Порты

Все опубликованы только на `127.0.0.1`.

| Порт | Сервис |
|---|---|
| 4317 | коллектор, приём OTLP gRPC |
| 13133 | коллектор, health check |
| 9090 | Prometheus |
| 3000 | Grafana (профиль `history`) |

## Данных нет — что проверить

1. `make ps` — оба контейнера запущены, Prometheus в состоянии healthy.
2. `curl -s http://127.0.0.1:13133/` — коллектор отвечает `Server available`.
3. В Prometheus на странице Status → Targets цель `collector-export` в состоянии UP.
4. Claude Code перезапущен после правки настроек и получил хотя бы один ответ: метрики появляются только после первого запроса к API.
5. `claude --debug-file /tmp/cc-debug.log`, затем `grep '3P telemetry' /tmp/cc-debug.log` — ошибки экспорта.
6. `make logs` — ошибки коллектора.
7. `make test-e2e` проходит, значит конвейер исправен и дело в настройках Claude Code.

Ошибка `all predefined address pools have been fully subnetted` при запуске означает, что Docker не смог выбрать подсеть сам; обычно причина — VPN. В `compose.yaml` подсеть задана явно, при конфликте поменяй `CC_OVERLAY_SUBNET` в `.env`.

## Структура

```
collector/config.yaml        конвейеры коллектора
prometheus/prometheus.yml    цели и интервалы сбора
prometheus/tests/            тесты семантики итогов (promtool)
grafana/provisioning/        источники данных как код
tools/synth/                 генератор синтетической телеметрии и e2e-тесты
docs/                        архитектура, ADR, запросы, план
```
