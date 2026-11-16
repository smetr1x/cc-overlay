#!/usr/bin/env bash
# Проверка, что телеметрия настоящего Claude Code дошла до Prometheus.
# Запуск: make verify-real (стек должен быть поднят, а Claude Code — перезапущен
# после правки ~/.claude/settings.json и получить хотя бы один ответ).
#
# Запросы выполняет promtool внутри контейнера Prometheus: на хосте ничего ставить не нужно.
set -euo pipefail

JOB='job=~"claude-code(-desktop)?"'
TOKENS='claude_code_token_usage_tokens_total'

q() { docker compose exec -T prometheus promtool query instant http://localhost:9090 "$1"; }

section() { printf '\n== %s\n' "$1"; }

section "1. Имена метрик Claude Code (пусто — данные не дошли, см. README, «Данных нет»)"
names=$(q "count by (__name__) ({__name__=~\"claude_code_.+\", $JOB})")
if [[ -z "$names" ]]; then
  echo "НЕТ ДАННЫХ"
  exit 1
fi
echo "$names"

section "2. Метки серий токенов (не должно быть user_email, user_id, user_account_*, organization_id)"
docker compose exec -T prometheus promtool query series \
  --match="${TOKENS}{$JOB}" http://localhost:9090 | head -5

section "3. Идентификаторы пользователя в хранилище (ожидается пусто)"
leak=$(q "count({$JOB, user_email!=\"\"} or {$JOB, user_id!=\"\"} or {$JOB, user_account_uuid!=\"\"} or {$JOB, user_account_id!=\"\"} or {$JOB, organization_id!=\"\"})")
if [[ -n "$leak" ]]; then
  echo "УТЕЧКА: $leak"
  exit 1
fi
echo "чисто"

section "4. Токены по сессиям и типам за последний час"
q "sum by (session_id, type) (last_over_time(${TOKENS}{$JOB}[1h]))"

section "5. Возраст последней точки по сессиям, секунды"
echo "Эксперимент ADR-0001: оставь сессию молчать на минуту и запусти ещё раз."
echo "Если возраст остаётся меньше 10 — cumulative-точки переэкспортируются, статус stale определён корректно."
q "time() - max by (session_id) (timestamp(${TOKENS}{$JOB}))"

section "6. Сбросы счётчиков за час (ожидается пусто; непусто после --resume — сообщи)"
q "sum by (session_id) (resets(${TOKENS}{$JOB}[1h])) > 0" || true
