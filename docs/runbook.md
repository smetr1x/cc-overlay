# Runbook: данных нет — что проверить

Начинай с одной команды: она показывает, что видит оверлей, и подсказывает, где искать.

```bash
make snapshot
```

Дальше — по полю, которое выглядит неправильно. Команды выполняются из корня репозитория в WSL.

## 1. Окно оверлея пишет «бэкенд недоступен»

Окно не получает поток от overlay-api дольше 5 секунд.

1. `make ps` — контейнер `overlay-api` запущен и `healthy`. Если стек остановлен — `make up`; окно переподключится само.
2. Из Windows: `curl.exe http://127.0.0.1:8787/healthz` → `{"status":"ok"}`. Если из WSL отвечает, а из Windows нет — сломался проброс портов WSL. Перезапусти WSL (`wsl --shutdown` в PowerShell, затем `make up`); если не помогло — `networkingMode=mirrored` в `.wslconfig` (глобальная настройка, см. [architecture.md](architecture.md)).
3. `curl -N http://127.0.0.1:8787/api/v1/stream` — раз в секунду должна приходить строка `data:`.

## 2. `status` в снимке

| Значение | Что значит | Что делать |
|---|---|---|
| `backend_down` | overlay-api не достучался до Prometheus | `make ps`, `make logs` — смотреть `prometheus` |
| `stale` | Prometheus работает, но не забирает метрики у коллектора | в http://127.0.0.1:9090/targets цель `collector-export` должна быть `UP`; иначе `make logs` → `otel-collector` |
| `no_data` | метрик Claude Code нет вообще | раздел 3 |
| `idle` | живых сессий нет, история есть | это нормально, пока Claude Code не запущен. Если запущен — раздел 3 |
| `ok` | всё работает | — |

## 3. Claude Code не присылает метрики

1. `curl -s http://127.0.0.1:13133/` → `Server available`: коллектор жив.
2. `make verify-real` — видит ли Prometheus метрики `claude_code_*`.
3. В `~/.claude/settings.json` есть блок `env` из README, в том числе `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative`. Переменные именно в пользовательском файле: в `.claude/settings.json` проекта они игнорируются.
4. Claude Code перезапущен после правки настроек и получил хотя бы один ответ.
5. Ошибки экспорта: `claude --debug-file /tmp/cc-debug.log`, затем `grep '3P telemetry' /tmp/cc-debug.log`.
6. `make test-e2e` проходит — значит, конвейер исправен, дело в настройках Claude Code.

## 4. `limits` равен `null` или `stale: true`

Лимиты подписки приходят двумя путями ([ADR-0010](adr/0010-usage-hook.md), [ADR-0009](adr/0009-statusline-bridge.md)).

1. `cat /tmp/cc-overlay-usage.last` — последний запуск хука `/usage`:
   - `http=204` — отчёт принят;
   - `http=000` — overlay-api недоступен;
   - `в ответе /usage нет лимитов` — вход не через подписку claude.ai, или изменился формат ответа;
   - `нет claude или jq` — установи `jq`, проверь путь к `claude`.
2. Время в файле не меняется — хук не вызывается. В `~/.claude/settings.json` должен быть ключ `hooks` из README; после правки нужен перезапуск Claude Code.
3. `cat /tmp/cc-overlay-statusline.last` — то же для status line (работает только в терминальном Claude Code).
4. `stale: true` — источник не обновлялся дольше 10 минут: в это время Claude Code не использовался.

## 5. `session.context` равен `null`

1. В терминале — status line подключён (раздел 4, пункт 3)?
2. В расширении VS Code значение берётся из событий ([ADR-0011](adr/0011-context-from-events.md)): в `env` должна быть `OTEL_LOGS_EXPORTER=otlp`.
3. Метрика есть: `curl -s 'http://127.0.0.1:9090/api/v1/query?query=claude_code_context_tokens'`. Пусто — событий `api_request` с `query_source` `main` или `sdk` не было; появится после следующего запроса к модели.

## 6. `events.available: false` или в окне «Loki недоступен»

1. `curl -s http://127.0.0.1:3100/ready` → `ready`. Сразу после старта Loki около 15 секунд отвечает 503 — это нормально.
2. `make logs` → `loki`. Частая причина — нехватка памяти: `docker stats` покажет, упирается ли Loki в лимит 192 МБ.
3. Остальной оверлей от Loki не зависит: метрики, лимиты и контекст продолжают работать.

## 7. События не доходят до Loki

1. `curl -s -G http://127.0.0.1:3100/loki/api/v1/label/service_name/values` — в списке есть `claude-code`.
2. Коллектор принимает события: метрика `otelcol_receiver_accepted_log_records` в Prometheus растёт.
3. Коллектор отправляет их: `otelcol_exporter_sent_log_records{exporter="otlp_http/loki"}` растёт; при сбоях появляется `otelcol_exporter_send_failed_log_records`.
4. Посмотреть сами события: временно добавить в `collector/config.yaml` экспортёр `debug` с `verbosity: detailed` в конвейер `logs`, перезапустить (`make restart`) и смотреть `make logs`. Не забыть убрать: в лог попадут атрибуты событий.

## 8. Дашборд Grafana пуст

1. Grafana запущена: `make up-history`, http://127.0.0.1:3000. Пароль — в `.env` (`GRAFANA_ADMIN_PASSWORD`).
2. Connections → Data sources: у Prometheus и Loki «Test» проходит.
3. Панели по дням строятся из суточных итогов: за первые сутки работы стека столбец один.
4. Запросы всех панелей проверяет `make test-e2e` (тест `TestDashboardQueries`): если он зелёный, а панель пустая — данных за выбранный период действительно нет.

## Полезные адреса

| Адрес | Что там |
|---|---|
| http://127.0.0.1:8787/api/v1/snapshot | то, что видит оверлей |
| http://127.0.0.1:9090/targets | состояние целей сбора Prometheus |
| http://127.0.0.1:3100/ready | готовность Loki |
| http://127.0.0.1:13133/ | здоровье коллектора |
| http://127.0.0.1:3000/d/cc-overlay | дашборд Grafana |
