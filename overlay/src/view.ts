// Модель отображения: что именно показать по снимку. Чистая функция без DOM — её проверяют тесты.
// Отрисовка (render.ts) только переносит результат на страницу.

import * as fmt from "./format";
import type { Level, Limit, Snapshot } from "./types";

export type Tone = Level | "muted";

export interface Row {
  key: string;
  label: string;
  text: string;
  tone: Tone;
  /** Заполнение полосы, 0–100. Нет поля — полосы нет. */
  bar?: number;
  /** Вторая строка мелким шрифтом. */
  hint?: string;
}

export interface Banner {
  text: string;
  tone: "info" | "warn" | "crit";
}

export interface View {
  banner: Banner | null;
  /** Заголовок: модель текущей сессии и число остальных живых сессий. */
  title: string;
  rows: Row[];
}

export interface Options {
  /** Есть ли связь с overlay-api: события потока приходят. */
  connected: boolean;
  /** Компактный режим: только самое важное. */
  compact: boolean;
}

const COMPACT_KEYS = new Set(["context", "rate", "limit5h"]);

function banner(snap: Snapshot | null, connected: boolean): Banner | null {
  if (!connected || !snap) return { text: "бэкенд недоступен", tone: "crit" };
  switch (snap.status) {
    case "backend_down":
      return { text: "бэкенд недоступен: Prometheus", tone: "crit" };
    case "stale":
      return { text: `данные устарели · ${fmt.duration(snap.data_age_seconds)}`, tone: "warn" };
    case "no_data":
      return { text: "нет данных", tone: "info" };
    case "idle":
      return { text: "нет активной сессии", tone: "info" };
    case "ok":
      return null;
  }
}

function limitRow(key: string, label: string, limit: Limit, level: Level | undefined, stale: boolean, extra?: string): Row {
  const parts = [fmt.percent(limit.used_percent)];
  if (extra) parts.push(extra);
  const row: Row = {
    key,
    label,
    text: parts.join(" · "),
    tone: stale ? "muted" : (level ?? "ok"),
    bar: Math.min(100, Math.max(0, limit.used_percent)),
  };
  if (stale) row.hint = "значение устарело";
  else if (limit.resets_in_seconds !== null) row.hint = `сброс через ${fmt.duration(limit.resets_in_seconds)}`;
  return row;
}

export function buildView(snap: Snapshot | null, opts: Options): View {
  const view: View = { banner: banner(snap, opts.connected), title: "", rows: [] };
  if (!snap) return view;

  // Без связи последний снимок остаётся на экране, но погашенным: числа могут быть неактуальны.
  const dim = !opts.connected || snap.status === "stale";
  const tone = (t: Tone): Tone => (dim ? "muted" : t);
  const rows: Row[] = [];

  const s = snap.session;
  if (s) {
    const others = (snap.active_sessions ?? 1) - 1;
    view.title = fmt.model(s.model) + (others > 0 ? `  +${others}` : "");

    if (s.tokens) {
      const t = s.tokens;
      rows.push({
        key: "session",
        label: "сессия",
        text: `in ${fmt.tokens(t.input)} · out ${fmt.tokens(t.output)}`,
        tone: tone("ok"),
        hint: `кэш: чтение ${fmt.tokens(t.cache_read)}, запись ${fmt.tokens(t.cache_creation)} · попадание ${fmt.percent(
          s.cache_hit_ratio === null ? null : s.cache_hit_ratio * 100,
        )}`,
      });
    }
    rows.push({
      key: "cost",
      label: "стоимость",
      text: `${fmt.usd(s.cost_usd)} · ${fmt.duration(s.duration_seconds)}`,
      tone: tone("ok"),
    });

    const c = s.context;
    if (c) {
      const row: Row = {
        key: "context",
        label: "контекст",
        text: fmt.percent(c.used_percent, c.approximate),
        tone: c.stale ? "muted" : tone(snap.levels.context_percent ?? "ok"),
        bar: Math.min(100, Math.max(0, c.used_percent)),
      };
      if (c.tokens !== null && c.window_tokens !== null) {
        row.hint = `${fmt.tokens(c.tokens)} из ${fmt.tokens(c.window_tokens)}`;
      }
      rows.push(row);
    }
  }

  if (snap.rate) {
    rows.push({
      key: "rate",
      label: "скорость",
      text: `${fmt.tokens(snap.rate.tokens_per_minute)} ток/мин · ${fmt.usd(snap.rate.cost_usd_per_hour)}/ч`,
      tone: tone(snap.levels.cost_usd_per_hour ?? "ok"),
    });
  }

  if (snap.today) {
    const approx = snap.today.approximate ? "~" : "";
    rows.push({
      key: "today",
      label: "сегодня",
      text: `${approx}${fmt.tokens(snap.today.tokens)} ток · ${fmt.usd(snap.today.cost_usd)}`,
      tone: tone("ok"),
      hint: `чтение кэша ${fmt.tokens(snap.today.cache_read_tokens)}`,
    });
  }

  // Лимиты приходят не из Prometheus, поэтому при недоступном Prometheus они остаются яркими.
  const lim = snap.limits;
  const limitsDim = !opts.connected || (lim?.stale ?? false);
  if (lim?.five_hour) {
    rows.push(limitRow("limit5h", "лимит 5ч", lim.five_hour, snap.levels.five_hour_percent, limitsDim));
  } else if (snap.window_5h) {
    // Настоящего процента нет: показываем расход за 5 часов как ориентир, без полосы и без уровня.
    rows.push({
      key: "limit5h",
      label: "за 5ч",
      text: `${fmt.tokens(snap.window_5h.tokens)} ток · ${fmt.usd(snap.window_5h.cost_usd)}`,
      tone: "muted",
      hint: "ориентир, не официальный лимит",
    });
  }
  if (lim?.seven_day) {
    const scoped = (lim.scoped ?? []).map((sc) => `${sc.label} ${fmt.percent(sc.used_percent)}`).join(", ");
    rows.push(limitRow("limit7d", "лимит 7д", lim.seven_day, snap.levels.seven_day_percent, limitsDim, scoped || undefined));
  }

  view.rows = opts.compact ? rows.filter((r) => COMPACT_KEYS.has(r.key)) : rows;
  if (opts.compact) for (const r of view.rows) delete r.hint;
  return view;
}
