# Сверка раздела 2 ТЗ с документацией

Дата сверки: 2026-11-16. Телеметрия Claude Code — beta, поэтому при следующем обновлении Claude Code сверку стоит повторить.

Источники:

- [Monitoring usage](https://code.claude.com/docs/en/monitoring-usage) — переменные, метрики, события, атрибуты.
- [Status line](https://code.claude.com/docs/en/statusline) — JSON, который получает скрипт status line.
- [deltatocumulativeprocessor README](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/processor/deltatocumulativeprocessor/README.md)
- [prometheusexporter README](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/exporter/prometheusexporter/README.md)
- [Loki: ingesting OTLP](https://grafana.com/docs/loki/latest/send-data/otel/)

## Расхождения

| # | В ТЗ | По документации | Следствие для проекта |
|---|---|---|---|
| 1 | «Лимиты Pro недоступны, показатель не выдумывать» | Через OTel лимиты действительно не передаются. Но скрипт status line получает `rate_limits.five_hour.used_percentage`, `rate_limits.seven_day.used_percentage` и `resets_at` (только подписчики Pro/Max/Ultra, после первого ответа API), а также `context_window.used_percentage` | Реальный процент лимита и заполнение контекста доступны без приближений — [ADR-0009](adr/0009-statusline-bridge.md) |
| 2 | Temporality delta, перед Prometheus ставим `deltatocumulative` | Delta по умолчанию подтверждена. Есть переменная `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative` | Процессор можно не ставить — [ADR-0001](adr/0001-temporality.md) |
| 3 | Атрибуты `token.usage` и `cost.usage`: `type`, `model`, `query_source` | Дополнительно `speed`, `effort`, `agent.name`, `skill.name`, `plugin.name`, `marketplace.name`, `mcp_server.name`, `mcp_tool.name`. У `query_source` есть ещё значения `plugin_model_complete` и `plugin_model_classify` | Серий на одну сессию больше, чем предполагает ТЗ. Любой запрос обязан агрегировать через `sum by (...)` |
| 4 | `session.id` есть на всех точках | Команда `/clear` заменяет `session.id` на новый | После `/clear` «текущая сессия» начинается с нуля. Это норма, а не потеря данных |
| 5 | `OTEL_LOGS_EXPORT_INTERVAL=5000` нужно задать | 5000 мс — уже значение по умолчанию | Строку оставляем для явности |
| 6 | `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` | С v2.1.323 протокол по умолчанию — `http/protobuf`; для порта 4317 `grpc` нужно указывать явно | ТЗ корректно |
| 7 | Пять событий | Событий больше двадцати. Важные для нас: `api_refusal`, `api_retries_exhausted`, `assistant_response`, `compaction` (`pre_tokens`, `post_tokens`), `subagent_completed`. У `api_request` есть `cost_usd_micros`, `cache_creation_5m_tokens`, `cache_creation_1h_tokens`, `request_id`. Порядок событий задаёт `event.sequence` (счётчик на процесс, не на сессию) | `compaction` нужен для оценки контекста в фазе 4 |
| 8 | Четыре переменные приватности | Есть ещё `OTEL_LOG_TOOL_CONTENT` и `OTEL_LOG_MANAGED_SETTINGS`. `OTEL_LOG_ASSISTANT_RESPONSES`, если не задана, наследует значение `OTEL_LOG_USER_PROMPTS` | Список в README и runbook расширяем до шести |
| 9 | Вычищать `user.email` | На точках и событиях также `user.account_uuid`, `user.account_id`, `user.id`, `organization.id`. Первые два отключаются у источника: `OTEL_METRICS_INCLUDE_ACCOUNT_UUID=false` | Удаляем все пять — [ADR-0008](adr/0008-privacy.md) |
| 10 | Имена метрик в Prometheus «проверь» | В `prometheusexporter` опция `add_metric_suffixes` устарела и игнорируется, вместо неё `translation_strategy`. `resource_to_telemetry_conversion` устарела в пользу `resource_constant_labels` | Ожидаемые имена: `claude_code_token_usage_tokens_total`, `claude_code_cost_usage_USD_total`, `claude_code_active_time_total_seconds_total`, `claude_code_session_count_total`. Фактические фиксируются в фазе 1 |
| 11 | `session.count` с атрибутом `start_type` | Значения: `fresh`, `resume`, `continue`, `agents_view`. У `lines_of_code.count` есть `model`; у `code_edit_tool.decision` — `source` и `language` | Уточнение |
| 12 | «Настройки читаются только при старте» | Прямого утверждения в документации нет. Есть только описание того, что конфликтующие переменные удаляются «at startup» | Считаем допущением, проверяем в фазе 1 |
| 13 | `service.name` не упомянут | `claude-code` для терминала, `claude-code-desktop` для вкладки Code в Claude Desktop | Фильтры пишем как `service_name=~"claude-code.*"` |

## Подтверждено без изменений

- Переменные экспортёра в проектных `.claude/settings.json` и `.claude/settings.local.json` игнорируются; задавать их нужно в `~/.claude/settings.json` или в окружении.
- `OTEL_METRIC_EXPORT_INTERVAL` по умолчанию 60000 мс.
- `cost.usage` — приближение: «Cost metrics are approximations».
- `user.email` лежит в атрибутах точек и событий, а не в resource.
- События одного промпта связаны `prompt.id`. На метрики он не попадает.
- `OTEL_METRICS_INCLUDE_SESSION_ID` по умолчанию `true`.
- Resource-атрибуты: `service.name`, `service.version`, `os.type`, `os.version`, `host.arch`, `wsl.version`.

## Что проверить экспериментом в фазе 1

Документация на эти вопросы не отвечает, а от ответов зависит дизайн.

1. **Переэкспортирует ли SDK неизменившиеся cumulative-точки каждый интервал.** От этого зависит определение состояния «данные устарели» ([ADR-0001](adr/0001-temporality.md)).
2. **Сохраняется ли `session.id` при `claude --resume` и `--continue`.** Если сохраняется, счётчик новой серии начнётся с нуля под старым id — это сброс, который должен поймать страж `resets()` ([ADR-0007](adr/0007-totals-semantics.md)).
3. **Фактические имена метрик и меток** после `prometheusexporter` закреплённой версии.
4. **Нужен ли перезапуск Claude Code** после правки блока `env`.
5. **Работает ли проброс `127.0.0.1` из WSL в Windows** для портов, опубликованных Docker Engine внутри WSL.

Поведение `deltatocumulative` при out-of-order точках и рестарте коллектора README не описывает. Так как процессор исключён из основного пути, это проверяется только в рамках запасного сценария.
