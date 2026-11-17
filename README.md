# cc-overlay

Живой мониторинг расхода Claude Code: оверлей поверх окон Windows и история в Grafana. Всё работает локально, наружу ничего не уходит.

Состояние: готовы пайплайн метрик, overlay-api, окно оверлея, события в Loki и дашборд Grafana (фазы 1–4). Если что-то не показывается — [docs/runbook.md](docs/runbook.md). Техническое задание — [SPEC.md](SPEC.md), архитектура — [docs/architecture.md](docs/architecture.md), решения — [docs/adr/](docs/adr/).

## Быстрый старт

Нужны Docker Engine с Compose и `make`. Go, Node и прочее ставить не нужно: тесты и линтеры идут в контейнерах.

```bash
make up            # коллектор, Prometheus, Loki и overlay-api; при первом запуске создаёт .env
# добавь настройки в ~/.claude/settings.json (ниже) и перезапусти Claude Code
make verify-real   # проверить, что метрики настоящей сессии дошли
make snapshot      # то, что увидит оверлей
make up-history    # по желанию: Grafana с дашбордом, http://127.0.0.1:3000/d/cc-overlay (admin, пароль из .env)
```

### Настройки Claude Code: `~/.claude/settings.json`

Именно в пользовательском файле: в `.claude/settings.json` проекта переменные экспортёра игнорируются. Ниже три ключа; если какой-то уже есть, добавь строки в него. Путь к репозиторию подставь свой.

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4317",
    "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "cumulative",
    "OTEL_METRIC_EXPORT_INTERVAL": "5000",
    "OTEL_METRICS_INCLUDE_ACCOUNT_UUID": "false"
  },
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "bash /путь/к/cc-overlay/hooks/cc-overlay-usage.sh", "async": true}]}
    ],
    "PostToolBatch": [
      {"hooks": [{"type": "command", "command": "bash /путь/к/cc-overlay/hooks/cc-overlay-usage.sh", "async": true}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "bash /путь/к/cc-overlay/hooks/cc-overlay-usage.sh", "async": true}]}
    ]
  },
  "statusLine": {
    "type": "command",
    "command": "bash /путь/к/cc-overlay/statusline/cc-overlay-statusline.sh"
  }
}
```

После правки перезапусти Claude Code: настройки читаются при старте сессии. Для скриптов нужны `jq` и `curl` (`sudo apt install jq`).

Что делает каждый ключ:

| Ключ | Что даёт оверлею | Где работает |
|---|---|---|
| `env` | токены, стоимость, скорость, итоги; оценку заполнения контекста | терминал и расширение VS Code |
| `hooks` | процент 5-часового и недельного лимита подписки | терминал и расширение VS Code |
| `statusLine` | точное заполнение контекста и строку `Sonnet · ctx 18% · 5h 24% · 7d 41%` в терминале | только терминал |

Подробности по переменным:

- `…TEMPORALITY_PREFERENCE=cumulative` — на этом построены все итоги ([ADR-0001](docs/adr/0001-temporality.md)).
- `OTEL_METRIC_EXPORT_INTERVAL=5000` — по умолчанию 60 секунд, для оверлея слишком редко.
- `OTEL_LOGS_EXPORTER=otlp` — события нужны для оценки заполнения контекста. Они нигде не хранятся: коллектор берёт из них одно число ([ADR-0011](docs/adr/0011-context-from-events.md)).
- Не задавай `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT`, `OTEL_LOG_RAW_API_BODIES`, `OTEL_LOG_MANAGED_SETTINGS`: они включают сбор содержимого.

Про лимиты. Через OpenTelemetry они не передаются. Хук раз в минуту, пока ты работаешь, спрашивает их командой `claude -p "/usage"`; она не обращается к модели и токены не тратит ([ADR-0010](docs/adr/0010-usage-hook.md)). Хук запускается в фоне и ничего не печатает.

Про контекст. В расширении VS Code точного значения нет, оверлей показывает оценку: токены последнего запроса, делённые на размер окна из `overlay-api/config.yaml` (200 000 по умолчанию). Если работаешь с окном в миллион токенов, добавь модель в `context_windows`, иначе процент будет завышен.

Диагностика скриптов: `cat /tmp/cc-overlay-usage.last /tmp/cc-overlay-statusline.last` — время последнего запуска и код ответа сервиса (`204` — принято).

## Окно оверлея

Сборка нужна один раз и после изменений в `overlay/`. Нужны Rust с тулчейном MSVC и Visual Studio Build Tools с компонентом C++ ([docs/windows-toolchain.md](docs/windows-toolchain.md)).

```bash
make overlay-build   # первая сборка 4–5 минут, следующие быстрее
make overlay-run
```

Результат — один файл `overlay/src-tauri/target/release/cc-overlay.exe`, его можно запускать напрямую из Windows. Окно появляется в левом верхнем углу, поверх всех окон, и пропускает клики сквозь себя. Управление — глобальными хоткеями:

| Хоткей | Действие |
|---|---|
| `Ctrl+Alt+M` | режим перемещения: окно ловит мышь, его можно перетащить. Повторное нажатие закрепляет и сохраняет позицию |
| `Ctrl+Alt+C` | компактный режим: контекст, скорость, лимит 5 часов |
| `Ctrl+Alt+↑` / `Ctrl+Alt+↓` | прозрачность |
| `Ctrl+Alt+H` | спрятать или показать |
| `Ctrl+Alt+Q` | закрыть |

Позиция, прозрачность и режим сохраняются между запусками. Если стек остановлен, окно показывает «бэкенд недоступен» и само переподключается, когда стек поднимется.

## Команды

| Команда | Что делает |
|---|---|
| `make up` / `make up-history` | запустить ядро / ядро с Grafana |
| `make down` | остановить; данные в томах сохраняются |
| `make restart` | перезапустить после правки конфигов |
| `make snapshot` | текущий снимок overlay-api |
| `make overlay-build` | собрать окно оверлея на Windows (из WSL через `cmd.exe`) |
| `make overlay-run` | запустить собранное окно |
| `make overlay-dev` | интерфейс оверлея в браузере, http://127.0.0.1:1420; клавиша `c` — компактный режим |
| `make logs`, `make ps` | логи и состояние контейнеров |
| `make check` | проверить конфиги: compose, коллектор, Prometheus, YAML, Dockerfile |
| `make test` | тесты семантики PromQL, unit-тесты Go, проверка типов, тесты и сборка фронтенда |
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
| 8787 | overlay-api: снимок, поток, приём status line ([docs/api.md](docs/api.md)) |
| 1420 | интерфейс оверлея в режиме разработки (`make overlay-dev`) |
| 3100 | Loki |
| 3000 | Grafana (профиль `history`) |

## Данных нет — что проверить

Пошагово по каждому полю снимка — в [docs/runbook.md](docs/runbook.md). Первая команда всегда `make snapshot`: поле `status` подскажет, где искать.

Ошибка `all predefined address pools have been fully subnetted` при запуске означает, что Docker не смог выбрать подсеть сам; обычно причина — VPN. В `compose.yaml` подсеть задана явно, при конфликте поменяй `CC_OVERLAY_SUBNET` в `.env`.

## Структура

```
collector/config.yaml        конвейеры коллектора
prometheus/prometheus.yml    цели и интервалы сбора
prometheus/tests/            тесты семантики итогов (promtool)
grafana/provisioning/        источники данных и дашборд как код
loki/config.yaml             хранилище событий
overlay-api/                 сервис показателей: снимок, SSE, приём status line
overlay/                     окно оверлея: интерфейс (TypeScript, Vite) и оболочка Tauri (src-tauri)
statusline/                  скрипт status line для Claude Code
hooks/                       хук Claude Code: лимиты подписки через /usage
tools/synth/                 генератор синтетической телеметрии и e2e-тесты
docs/                        архитектура, ADR, запросы, план
```
