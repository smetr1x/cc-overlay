#!/usr/bin/env bash
# Хук Claude Code: узнаёт лимиты подписки и передаёт их в overlay-api (ADR-0010).
#
# Зачем: процент 5-часового и недельного лимита через OpenTelemetry не передаётся,
# а status line работает только в терминале. Команда /usage в неинтерактивном режиме
# (`claude -p "/usage"`) отдаёт те же числа, что и диалог Usage, при любом способе запуска:
# она не обращается к модели и не расходует токены.
#
# Подключение — ключ hooks в ~/.claude/settings.json, см. README. Нужны jq и curl.
# Хук запускается с "async": true и ничего не печатает, поэтому работу Claude не задерживает
# и в контекст модели ничего не добавляет.
#
# Переменные:
#   CC_OVERLAY_API             адрес overlay-api, по умолчанию http://127.0.0.1:8787
#   CC_OVERLAY_USAGE_INTERVAL  минимальный интервал между опросами в секундах, по умолчанию 60
#   CC_OVERLAY_CLAUDE_BIN      путь к claude, если его нет в PATH

# Вложенный вызов `claude -p` сам запустил бы этот хук: защита от рекурсии.
[[ -n "${CC_OVERLAY_NESTED:-}" ]] && exit 0

exec </dev/null >/dev/null 2>&1

api="${CC_OVERLAY_API:-http://127.0.0.1:8787}"
interval="${CC_OVERLAY_USAGE_INTERVAL:-60}"
state="${TMPDIR:-/tmp}"
stamp="$state/cc-overlay-usage.stamp"
trace="$state/cc-overlay-usage.last"

# События идут часто (каждая пачка вызовов инструментов), лимит меняется медленно.
now=$(date +%s)
last=$(cat "$stamp" 2>/dev/null || echo 0)
(( now - last < interval )) && exit 0
echo "$now" > "$stamp"

note() { printf '%s %s\n' "$(date '+%F %T')" "$1" > "$trace"; }

claude_bin="${CC_OVERLAY_CLAUDE_BIN:-$(command -v claude || echo "$HOME/.local/bin/claude")}"
if [[ ! -x "$claude_bin" ]] || ! command -v jq >/dev/null; then
  note "нет claude или jq (claude: $claude_bin)"
  exit 0
fi

# Вложенный запуск не должен оставлять следов: без хуков, без телеметрии (иначе каждая проверка
# создавала бы в Prometheus новую «сессию»), без записи в историю сессий, из нейтрального каталога.
cd "$state" || exit 0
out=$(CC_OVERLAY_NESTED=1 timeout 20 "$claude_bin" -p "/usage" \
  --output-format stream-json --verbose --no-session-persistence \
  --settings '{"disableAllHooks":true,"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"0","OTEL_METRICS_EXPORTER":"none","OTEL_LOGS_EXPORTER":"none"}}')

# Из ответа берётся только блок лимитов. Он есть лишь при входе через подписку claude.ai.
payload=$(jq -c 'select(.type == "assistant") | .usage_report.rate_limits | select(.limits != null) | {limits}' <<<"$out" | head -n 1)
if [[ -z "$payload" ]]; then
  note "в ответе /usage нет лимитов"
  exit 0
fi

code=$(curl -s -o /dev/null -w '%{http_code}' -m 2 --noproxy '*' \
  -H 'Content-Type: application/json' -d "$payload" "$api/api/v1/usage") || true
# 204 — принято, 000 — сервис недоступен.
note "http=${code:--}"
