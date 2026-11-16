# ТЗ: cc-overlay — живой мониторинг расхода Claude Code с оверлеем на экране

> Этот файл — техническое задание для агента (Claude Code). Положи его в корень пустого репозитория.
> Работа идёт по фазам. После каждой фазы — коммит, короткий отчёт и **остановка до моего подтверждения**.

---

## 0. Контекст

- **ОС:** Windows 11, все проекты живут в WSL2. Docker Engine установлен внутри WSL (не Docker Desktop).
- **Claude Code:** запускается внутри WSL, тариф Pro.
- **Кто я:** разработчик сервисов, расту в DevOps. Проект учебный: хочу, чтобы он был сделан «по-взрослому» (воспроизводимо, с конфигами как кодом, с объяснёнными решениями), а не просто работал.
- **Язык:** документация, комментарии в ADR и отчёты — по-русски. Код, конфиги, имена — как принято.

## 1. Цель

Оверлей в углу экрана в духе MSI Afterburner + RivaTuner: поверх всех окон, полупрозрачный, не мешает кликам, и в реальном времени показывает расход токенов Claude Code. Плюс история и дашборды в Grafana.

### Не цели
- Командный или облачный мониторинг (только одна машина, только localhost).
- Алерты в мессенджеры.
- Сбор текстов промптов, ответов и аргументов инструментов (приватность, см. п. 5).
- «Процент израсходованного лимита Pro»: Claude Code не отдаёт лимиты подписки через OTel. Не выдумывать этот показатель (подробнее в п. 4).

## 2. Источник данных: телеметрия Claude Code

> ⚠️ OTel-поддержка в Claude Code в статусе beta: имена и атрибуты могут меняться. Ниже то, что известно на момент написания ТЗ. **В фазе 0 сверь с актуальной документацией** (раздел «Monitoring usage» на code.claude.com). При любом расхождении источник истины — документация, а расхождение фиксируй в ADR.

### Включение (через блок `env` в `~/.claude/settings.json`, а не в `.claude/settings.json` проекта: там переменные экспортёра игнорируются)
```
CLAUDE_CODE_ENABLE_TELEMETRY=1
OTEL_METRICS_EXPORTER=otlp
OTEL_LOGS_EXPORTER=otlp            # без этого будут только метрики, без событий
OTEL_EXPORTER_OTLP_PROTOCOL=grpc
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
OTEL_METRIC_EXPORT_INTERVAL=5000   # по умолчанию 60000 — для оверлея слишком редко
OTEL_LOGS_EXPORT_INTERVAL=5000
```
Claude Code читает эти настройки **только при старте**: после изменений нужен перезапуск сессии.

### Метрики (префикс `claude_code.`)
| Метрика | Единицы | Ключевые атрибуты |
|---|---|---|
| `token.usage` | tokens | `type` (input / output / cacheRead / cacheCreation), `model`, `query_source` (main / subagent / auxiliary) |
| `cost.usage` | USD (оценка) | `model`, `query_source` |
| `session.count` | count | `start_type` |
| `active_time.total` | s | `type` (user / cli) |
| `lines_of_code.count` | count | `type` (added / removed) |
| `commit.count`, `pull_request.count` | count | — |
| `code_edit_tool.decision` | count | `tool_name`, `decision` |

На всех точках есть `session.id`. Для залогиненных пользователей также `user.email` и другие идентификаторы.
Ожидаемые имена в Prometheus: `claude_code_token_usage_tokens_total`, `claude_code_cost_usage_USD_total` и т. п. Проверь фактические имена.

### События (сигнал logs)
`claude_code.api_request` (модель, токены по типам, `cost_usd`, `duration_ms`), `claude_code.api_error`, `claude_code.tool_result`, `claude_code.tool_decision`, `claude_code.user_prompt` (без текста, если не включён `OTEL_LOG_USER_PROMPTS`). События одного промпта связаны общим `prompt.id`.

### Известные грабли (учесть в дизайне)
1. **Temporality = delta по умолчанию.** Prometheus-экспортёру нужен cumulative, поэтому перед ним ставится процессор `deltatocumulative` с увеличенным `max_stale` (например, 1h): дефолтные 5 минут теряют серии при простое.
2. **`session.id` не отключать.** Иначе параллельные сессии пишут в один delta-поток, и часть точек отбрасывается как out-of-order.
3. **`increase()` по короткоживущим сериям врёт** (экстраполяция и сбросы). Для итогов за сессию или период обоснуй выбор PromQL (`increase` / `max_over_time` / `last_over_time` и т. п.) и **покрой тестами на синтетике**.
4. **`cost.usage` — оценка** по API-ценам. На подписке Pro это условная «стоимость в долларах API», а не реальные списания. В UI подписать как `≈$`.
5. `user.email` лежит в атрибутах точек и событий, а не в resource. Удалять через `transform`, а не через `resource`-процессор.

## 3. Целевая архитектура (можно оспорить в ADR)

```
Claude Code (WSL)
   │ OTLP gRPC :4317
   ▼
otel-collector (contrib)
   ├─ metrics: memory_limiter → transform(drop email) → deltatocumulative → batch → prometheus exporter :8889
   └─ logs:    memory_limiter → transform(drop email) → batch → Loki (OTLP)          [фаза 4]
        ▲ scrape
Prometheus (retention 30d) ──► Grafana (provisioning: datasources + dashboards as code)
   ▲
   │ HTTP API (PromQL / LogQL)
overlay-api (WSL, контейнер) ── /api/v1/snapshot (JSON), /api/v1/stream (SSE), /healthz, /metrics
   ▲ http://localhost:<port>
overlay (нативное окно Windows): always-on-top, прозрачный, click-through
```

Всё, кроме окна оверлея, живёт в `docker compose` внутри WSL. Оверлей — Windows-приложение, которое ходит на `localhost` (проброс портов WSL2 → Windows). Если проброс не работает, предложи вариант (например, `networkingMode=mirrored` в `.wslconfig`), но **не меняй глобальные настройки WSL без моего согласия**.

### Решения по умолчанию (подтверди или оспорь в фазе 0)
| Вопрос | По умолчанию | Альтернативы для сравнения |
|---|---|---|
| Язык overlay-api | Go (один бинарник, маленький образ) | Python + FastAPI |
| Стек оверлея | Tauri (лёгкий, прозрачность и click-through из коробки) | Electron, PySide6, Rainmeter, Grafana kiosk + PowerToys Always On Top |
| Хранилище событий | Loki (нативный OTLP-приём) | файл/SQLite в overlay-api, вообще без событий |
| Транспорт в оверлей | SSE | polling, WebSocket |

## 4. Что показывает оверлей

**MVP (только из метрик):**
- Текущая сессия: токены по типам (in / out / cache read / cache write), `≈$`, модель, длительность.
- Скорость: токены в минуту и `≈$/ч` за последние 5 минут.
- Cache hit ratio = cacheRead / (input + cacheRead + cacheCreation).
- Сегодня: всего токенов и `≈$`.
- Скользящее окно 5 часов: токены и `≈$`. Подпись: «ориентир, не официальный лимит».

**Фаза 4 (из событий):**
- Последний API-запрос: модель, токены, латентность, ошибки.
- Примерное заполнение контекста по последнему main-запросу (input + cache tokens). Пометить как приближение и обосновать формулу в ADR.

**Поведение:**
- Явные состояния «нет данных», «бэкенд недоступен», «данные устарели» (нет обновлений дольше двух интервалов экспорта). **Никогда не показывать нули вместо отсутствия данных.**
- Цветовые пороги (например, `≈$/ч`) настраиваются в конфиге, а не в коде.
- Задержка от экспорта до экрана — не больше 2 секунд.
- Хоткей «режим перемещения», позиция и прозрачность сохраняются между запусками.
- Опционально: компактный и развёрнутый режимы.

## 5. Нефункциональные требования

- **Воспроизводимость:** `make up / down / logs / check / test`. Версии всех образов закреплены, никаких `latest`.
- **Секреты:** пароль Grafana и прочее только в `.env` (он в `.gitignore`), в репозитории лежит `.env.example`. Никаких секретов в коде и compose-файлах.
- **Сеть:** все порты публикуются только на `127.0.0.1`.
- **Приватность:** `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_RAW_API_BODIES` выключены. `user.email` вычищается в коллекторе.
- **Ресурсы:** весь стек укладывается примерно в 500 МБ RAM. Оверлей лёгкий (цель — до 100 МБ).
- **Наблюдаемость самого стека:** health_check у коллектора, собственные метрики коллектора и overlay-api в Prometheus.
- **Валидация:** `make check` валидирует конфиг коллектора (`otelcol-contrib validate`), Prometheus (`promtool check config`), линтит YAML и Dockerfile.
- **Тесты без расхода реальных токенов:**
  - unit-тесты overlay-api с фейковым Prometheus;
  - e2e smoke: синтетические OTLP-метрики (`telemetrygen` или свой скрипт, имитирующий delta-счётчики Claude Code с несколькими сессиями) → проверка, что `/api/v1/snapshot` отдаёт ожидаемые числа.
- **CI (GitHub Actions):** линт, `make check`, тесты, сборка образов.
- **Документация:**
  - `README.md`: быстрый старт за ≤ 5 команд, включая готовый блок `env` для `~/.claude/settings.json`;
  - `docs/architecture.md` с mermaid-диаграммой;
  - `docs/adr/NNNN-*.md`: каждое значимое решение с контекстом, альтернативами и **почему так**;
  - `docs/runbook.md`: «данных нет — что проверить по шагам».

## 6. Предлагаемая структура репозитория
```
.
├── SPEC.md
├── README.md
├── Makefile
├── compose.yaml
├── .env.example
├── collector/config.yaml
├── prometheus/prometheus.yml
├── grafana/provisioning/{datasources,dashboards}/
├── loki/                      # фаза 4
├── overlay-api/               # сервис + тесты + Dockerfile
├── overlay/                   # Windows-приложение
├── tools/synth/               # генератор синтетической телеметрии
├── docs/{architecture.md,runbook.md,adr/}
└── .github/workflows/ci.yaml
```

## 7. Фазы и критерии приёмки

### Фаза 0 — Дизайн (без кода)
- Сверь п. 2 с актуальной документацией Claude Code и OTel Collector.
- Напиши `docs/architecture.md` и ADR по таблице «решений по умолчанию».
- Для каждого показателя оверлея (п. 4) — конкретный PromQL/LogQL с обоснованием.
- План фаз 1–5 с оценкой сложности.
- **Список вопросов ко мне одним блоком, с вариантами ответа.** Потом стоп.

### Фаза 1 — Пайплайн метрик
- compose: collector + Prometheus + Grafana, provisioning datasource.
- `tools/synth` генерирует delta-метрики «как Claude Code» для двух параллельных сессий.
- **DoD:** `make up && make test-e2e` проходит. После реальной сессии Claude Code в Prometheus видны `claude_code_token_usage_*` с разбивкой по `type` и `session_id`.

### Фаза 2 — overlay-api
- `/api/v1/snapshot`, `/api/v1/stream` (SSE), `/healthz`, `/metrics`; конфиг через env/файл.
- **DoD:** unit-тесты и e2e на синтетике зелёные; на «нет данных» и «устарело» API отдаёт явный статус, а не нули.

### Фаза 3 — Оверлей MVP
- Окно в углу, always-on-top, click-through, хоткей перемещения, сохранение позиции; показатели MVP из п. 4.
- Инструкция по сборке и запуску на Windows (какие тулчейны нужны — перечислить заранее, в фазе 0).
- **DoD:** при работе Claude Code цифры меняются в пределах ~2 с после экспорта; при остановке стека оверлей показывает «бэкенд недоступен».

### Фаза 4 — События и история
- Loki + логи коллектора, «последний запрос», ошибки API, приближение заполнения контекста.
- Grafana-дашборд «как код»: расход по дням и моделям, cache hit ratio, латентность запросов, топ сессий.

### Фаза 5 — Опционально
- Status line Claude Code (быстрый текстовый индикатор прямо в терминале; сверить формат входных данных с документацией).
- `windows_exporter`: CPU / RAM / GPU в том же оверлее (дух Afterburner).
- Автозапуск оверлея, трей-иконка.

## 8. Правила работы для агента

1. **Сначала дизайн, потом код.** Не перескакивай фазы. В конце каждой фазы — коммит (conventional commits), отчёт в 5–10 строк и стоп.
2. **Разрушительные и глобальные действия — только с подтверждением:** `docker system prune`, удаление volumes, `rm -rf` вне репозитория, изменение `~/.claude/settings.json`, `.wslconfig`, системных настроек Windows. Вместо выполнения покажи diff или команду.
3. **Никаких секретов в коде.**
4. **Факты о телеметрии Claude Code проверяй по документации**, а не по памяти: это beta.
5. **Объясняй решения:** проект учебный. В ADR пиши «почему», какие альтернативы рассматривались и какой принцип стоит за выбором.
6. **Экономь контекст:** не читай большие логи целиком, используй `grep`, `tail`, `jq`; для проверок пиши маленькие скрипты.
7. Если требование ТЗ кажется неверным или неоптимальным — **скажи прямо** и предложи альтернативу, а не исполняй молча.