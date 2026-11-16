# overlay-api: контракт

Сервис слушает `127.0.0.1:8787`. Все ответы — JSON в UTF-8. Источник правды для структуры снимка — `overlay-api/internal/snapshot/snapshot.go`.

| Метод и путь | Назначение |
|---|---|
| `GET /api/v1/snapshot` | текущий снимок |
| `GET /api/v1/stream` | поток снимков, Server-Sent Events |
| `POST /api/v1/statusline` | приём отчёта от скрипта status line |
| `GET /healthz` | процесс жив |
| `GET /metrics` | собственные метрики в формате Prometheus |

## Главное правило

**Отсутствие данных передаётся как `null`, а не как ноль.** Ноль означает, что значение известно и равно нулю. Оверлей обязан показывать `null` как «нет данных», а не как `0`.

## Снимок

```json
{
  "status": "ok",
  "generated_at": "2026-11-16T21:02:25.92+03:00",
  "data_age_seconds": 3.1,
  "active_sessions": 1,
  "session": {
    "id": "cf88bc03-18fa-43f0-929d-0739ff853b5c",
    "model": "claude-sonnet-5-5",
    "tokens": {"input": 1189, "output": 1914, "cache_read": 157887, "cache_creation": 35170},
    "cost_usd": 0.1755718,
    "active_seconds": 26.0,
    "started_at": "2026-11-16T20:25:25.74+03:00",
    "duration_seconds": 2220.2,
    "cache_hit_ratio": 0.8128,
    "context": {"used_percent": 18, "window_tokens": 200000, "age_seconds": 19.2, "stale": false}
  },
  "rate": {"tokens_per_minute": 0, "cost_usd_per_hour": 0},
  "today": {"seconds": 75738, "tokens": 38273, "cache_read_tokens": 157887, "cost_usd": 0.1755718, "approximate": false},
  "window_5h": {"seconds": 18000, "tokens": 38273, "cache_read_tokens": 157887, "cost_usd": 0.1755718, "approximate": false},
  "limits": {
    "five_hour": {"used_percent": 23.5, "resets_at": "2026-11-16T23:02:46+03:00", "resets_in_seconds": 7154.2},
    "seven_day": {"used_percent": 41.2, "resets_at": "2026-11-20T08:22:46+03:00", "resets_in_seconds": 299954.2},
    "age_seconds": 19.2,
    "stale": false
  },
  "levels": {"cost_usd_per_hour": "ok", "five_hour_percent": "ok", "seven_day_percent": "ok", "context_percent": "ok"}
}
```

### Поле `status`

| Значение | Когда | Что есть в снимке |
|---|---|---|
| `ok` | есть живая сессия: её последняя точка моложе двух интервалов экспорта | всё |
| `idle` | живых сессий нет, но история есть | итоги; `session` равен `null` |
| `stale` | Prometheus отвечает, но не получает метрики от коллектора | последние известные числа; `data_age_seconds` показывает их возраст |
| `no_data` | метрик Claude Code нет вообще | `session`, `rate`, `today`, `window_5h` равны `null` |
| `backend_down` | Prometheus недоступен | только `limits`, остальное `null` |

Завершённая сессия и сломанный экспорт из Claude Code выглядят одинаково: точки перестали приходить. Сервис считает это завершением сессии (`idle`). Статус `stale` означает поломку конвейера, которую видно со стороны: Prometheus не может забрать метрики у коллектора.

### Остальные поля

| Поле | Смысл |
|---|---|
| `data_age_seconds` | возраст самой свежей точки метрик Claude Code |
| `active_sessions` | число живых сессий; оверлей показывает «+N» при значении больше 1 |
| `session` | живая сессия с самой поздней активностью. После `/clear` это новая сессия с нуля |
| `session.model` | модель основного диалога; вспомогательные запросы её не определяют |
| `session.tokens` | итог по типам. Тип, которого у сессии ещё не было, равен `0` — это известное значение |
| `session.cost_usd` | оценка по ценам API, не списания по подписке. В интерфейсе подписывается `≈$` |
| `session.active_seconds` | активное время по метрике Claude Code |
| `session.duration_seconds` | время от первой точки сессии |
| `session.cache_hit_ratio` | `cache_read / (input + cache_read + cache_creation)` |
| `session.context` | заполнение контекста по данным status line; `null`, если скрипт не подключён |
| `rate` | скорость за последние 5 минут по всем сессиям; токены — без чтения кэша |
| `today` | итог с локальной полуночи (`CC_OVERLAY_TZ`); `seconds` — длина окна |
| `window_5h` | итог за скользящие 5 часов. Ориентир, не официальный лимит |
| `*.tokens` в окнах | input, output и cache_creation вместе; чтение кэша — отдельно в `cache_read_tokens` |
| `*.approximate` | `true`, если внутри окна сбрасывался счётчик и итог неточен |
| `limits` | лимиты подписки по данным status line; `null`, если скрипт не подключён или оба окна уже сбросились |
| `limits.stale`, `context.stale` | `true`, если status line не обновлялся дольше `statusline_max_age`: значение могло измениться |
| `levels` | уровень `ok`, `warn` или `crit` по порогам из `overlay-api/config.yaml`. Ключа нет, если нет значения |

### Частота обновления

Блок `session`, `rate`, `status` и `limits` пересчитываются каждую секунду (`poll_interval`). Итоги `today` и `window_5h` — раз в 10 секунд (`totals_interval`): их запросы читают часы истории.

## Поток

`GET /api/v1/stream` отдаёт `text/event-stream`. Каждое событие — полный снимок в поле `data`, без имени события:

```
retry: 2000

data: {"status":"ok", ...}

data: {"status":"ok", ...}
```

Первое событие приходит сразу после подключения, дальше — по одному на каждый опрос, то есть раз в секунду. Отдельного пульса нет: если событий нет дольше 5 секунд, клиент считает соединение потерянным.

Проверка из терминала: `curl -N http://127.0.0.1:8787/api/v1/stream`.

## CORS

`snapshot` и `stream` отдают `Access-Control-Allow-Origin` только для origin из списка `cors_origins`. По умолчанию это окно Tauri: `http://tauri.localhost` и `tauri://localhost`. Значение `*` отвергается при запуске. Запросы без заголовка `Origin` (curl) проходят всегда: порт доступен только с этой машины.

## Приём status line

`POST /api/v1/statusline`, `Content-Type: application/json`, тело до 16 КБ:

```json
{
  "session_id": "cf88bc03-18fa-43f0-929d-0739ff853b5c",
  "model": "Sonnet",
  "cost_usd": 0.17,
  "context": {"used_percentage": 18, "window_size": 200000},
  "rate_limits": {
    "five_hour": {"used_percentage": 23.5, "resets_at": 1794852166},
    "seven_day": {"used_percentage": 41.2, "resets_at": 1795094966}
  }
}
```

Обязателен только `session_id`. Ответ `204` при успехе; `400` при ошибке в теле, `415` при другом типе содержимого, `403` при наличии заголовка `Origin` — запросы из браузера не принимаются.

Значения хранятся только в памяти и пропадают при перезапуске сервиса до следующего обновления status line. Лимиты общие для аккаунта и берутся из последнего отчёта любой сессии; контекст — из отчёта текущей сессии.

## Собственные метрики

| Метрика | Смысл |
|---|---|
| `cc_overlay_snapshot_build_duration_seconds` | время сборки снимка |
| `cc_overlay_prometheus_query_errors_total` | неудачные запросы к Prometheus |
| `cc_overlay_snapshot_status{status}` | 1 для текущего статуса |
| `cc_overlay_sse_clients` | подключённые клиенты потока |
| `cc_overlay_rate_limit_used_percent{window}` | процент лимита из status line; так он попадает в историю |
| `cc_overlay_context_used_percent` | заполнение контекста текущей сессии |
| `cc_overlay_statusline_reports_total{result}` | принятые и отклонённые отчёты |
