#!/usr/bin/env bash
# Status line для Claude Code: печатает короткую строку в терминале и передаёт
# в overlay-api лимиты подписки и заполнение контекста (ADR-0009).
#
# Подключение — ключ statusLine в ~/.claude/settings.json, см. README.
# Нужны jq и curl. Адрес сервиса меняется переменной CC_OVERLAY_API.
#
# Claude Code передаёт на stdin JSON с путями, именем репозитория и прочим.
# Наружу уходят только поля, перечисленные в фильтре ниже.

api="${CC_OVERLAY_API:-http://127.0.0.1:8787}"
input=$(cat)

if ! command -v jq >/dev/null 2>&1; then
  echo "cc-overlay: установи jq (sudo apt install jq)"
  exit 0
fi

payload=$(jq -c '{
  session_id,
  model: .model.display_name,
  cost_usd: .cost.total_cost_usd,
  context: {
    used_percentage: .context_window.used_percentage,
    window_size: .context_window.context_window_size
  },
  rate_limits: {
    five_hour: .rate_limits.five_hour,
    seven_day: .rate_limits.seven_day
  }
}' <<<"$input" 2>/dev/null)

# Строка должна появляться мгновенно, поэтому на отправку отводится 0,3 с,
# а её неудача (сервис остановлен) ни на что не влияет.
if [[ -n "$payload" ]]; then
  curl -s -o /dev/null -m 0.3 -H 'Content-Type: application/json' \
    -d "$payload" "$api/api/v1/statusline" || true
fi

# Поля, которых ещё нет (лимиты появляются после первого ответа API), просто пропускаются.
jq -r '
  def pct: if . == null then empty else "\(. | round)%" end;
  [
    (.model.display_name // empty),
    (.context_window.used_percentage | pct | "ctx \(.)"),
    (.rate_limits.five_hour.used_percentage | pct | "5h \(.)"),
    (.rate_limits.seven_day.used_percentage | pct | "7d \(.)")
  ] | join(" · ")
' <<<"$input" 2>/dev/null || echo "cc-overlay"
