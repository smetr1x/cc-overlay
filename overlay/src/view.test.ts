import { describe, expect, it } from "vitest";
import type { Snapshot } from "./types";
import { buildView, type Row, type View } from "./view";

// Снимок настоящей сессии расширения VS Code (2026-11-16), числа из `make snapshot`.
function okSnapshot(): Snapshot {
  return {
    status: "ok",
    generated_at: "2026-11-16T22:57:11+03:00",
    data_age_seconds: 1.85,
    active_sessions: 1,
    session: {
      id: "7f7b2bcf-35b9-4d6c-9484-e692fbc54113",
      model: "claude-sonnet-5-5",
      tokens: { input: 1252, output: 459, cache_read: 25553, cache_creation: 13602 },
      cost_usd: 0.0574738,
      active_seconds: 4.31,
      started_at: "2026-11-16T22:56:56+03:00",
      duration_seconds: 900,
      cache_hit_ratio: 0.6323904274011929,
      context: {
        used_percent: 25.647,
        tokens: 51294,
        window_tokens: 200000,
        source: "telemetry",
        approximate: true,
        age_seconds: 12,
        stale: false,
      },
    },
    rate: { tokens_per_minute: 3062.6, cost_usd_per_hour: 0.6896856 },
    today: { seconds: 82624, tokens: 120621, cache_read_tokens: 361345, cost_usd: 0.5182505, approximate: false },
    window_5h: { seconds: 18000, tokens: 120621, cache_read_tokens: 361345, cost_usd: 0.5182505, approximate: false },
    limits: {
      five_hour: { used_percent: 67, resets_at: "2026-11-17T00:10:00Z", resets_in_seconds: 15169 },
      seven_day: { used_percent: 11, resets_at: "2026-11-22T09:59:59Z", resets_in_seconds: 482568 },
      scoped: [{ label: "Fable", used_percent: 18, resets_at: null, resets_in_seconds: null }],
      source: "usage",
      age_seconds: 27,
      stale: false,
    },
    events: {
      available: true,
      last_request: {
        at: "2026-11-16T22:57:04+03:00",
        age_seconds: 7,
        model: "claude-sonnet-5-5",
        duration_ms: 6604,
        ttft_ms: 5042,
        input_tokens: 4,
        output_tokens: 229,
        cache_read_tokens: 29813,
        cache_creation_tokens: 9383,
        cost_usd: 0.0428113,
      },
      errors: { window_seconds: 900, count: 0, last: null },
      latency: { window_seconds: 3600, requests: 12, p50_ms: 4100, p95_ms: 9800 },
    },
    levels: { cost_usd_per_hour: "ok", five_hour_percent: "ok", seven_day_percent: "ok", context_percent: "ok", api_errors_15m: "ok" },
  };
}

const live = { connected: true, compact: false };

function row(view: View, key: string): Row {
  const found = view.rows.find((r) => r.key === key);
  if (!found) throw new Error(`нет строки ${key}; есть: ${view.rows.map((r) => r.key).join(", ")}`);
  return found;
}

describe("обычная работа", () => {
  const view = buildView(okSnapshot(), live);

  it("не показывает баннер и выводит модель в заголовке", () => {
    expect(view.banner).toBeNull();
    expect(view.title).toBe("sonnet 5.5");
  });

  it("выводит показатели сессии", () => {
    expect(row(view, "session").text).toBe("in 1.3k · out 459");
    expect(row(view, "session").hint).toBe("кэш 25.6k / 13.6k · 63%");
    expect(row(view, "cost").text).toBe("≈$0.06 · 15м");
    expect(row(view, "rate").text).toBe("3.1k ток/мин · ≈$0.69/ч");
    expect(row(view, "today").text).toBe("121k ток · ≈$0.52");
  });

  it("помечает оценку контекста тильдой и показывает токены", () => {
    const c = row(view, "context");
    expect(c.text).toBe("~26%");
    expect(c.bar).toBeCloseTo(25.647);
    expect(c.hint).toBe("51.3k из 200k");
  });

  it("выводит лимиты подписки с временем до сброса", () => {
    expect(row(view, "limit5h")).toMatchObject({ label: "лимит 5ч", text: "67%", bar: 67, tone: "ok", hint: "сброс через 4ч 12м" });
    expect(row(view, "limit7d").text).toBe("11% · Fable 18%");
  });

  it("показывает число остальных живых сессий", () => {
    const snap = okSnapshot();
    snap.active_sessions = 3;
    expect(buildView(snap, live).title).toBe("sonnet 5.5  +2");
  });
});

describe("уровни", () => {
  it("красят строки по порогам из снимка", () => {
    const snap = okSnapshot();
    snap.levels = { cost_usd_per_hour: "warn", five_hour_percent: "crit", context_percent: "warn" };
    const view = buildView(snap, live);
    expect(row(view, "rate").tone).toBe("warn");
    expect(row(view, "limit5h").tone).toBe("crit");
    expect(row(view, "context").tone).toBe("warn");
    // Для недельного лимита уровня в снимке нет: строка остаётся обычной.
    expect(row(view, "limit7d").tone).toBe("ok");
  });

  it("полоса не выходит за 100% при превышении лимита", () => {
    const snap = okSnapshot();
    snap.limits!.five_hour!.used_percent = 104;
    const r = row(buildView(snap, live), "limit5h");
    expect(r.bar).toBe(100);
    expect(r.text).toBe("104%");
  });
});

describe("состояния", () => {
  it("нет связи с сервисом и снимка ещё не было", () => {
    const view = buildView(null, { connected: false, compact: false });
    expect(view.banner).toEqual({ text: "бэкенд недоступен", tone: "crit" });
    expect(view.rows).toEqual([]);
  });

  it("связь потеряна: последний снимок остаётся, но погашен", () => {
    const view = buildView(okSnapshot(), { connected: false, compact: false });
    expect(view.banner?.text).toBe("бэкенд недоступен");
    expect(view.rows.length).toBeGreaterThan(0);
    expect(view.rows.every((r) => r.tone === "muted")).toBe(true);
  });

  it("Prometheus недоступен: остаются только лимиты, и они не погашены", () => {
    const snap = okSnapshot();
    Object.assign(snap, { status: "backend_down", session: null, rate: null, today: null, window_5h: null, active_sessions: null });
    const view = buildView(snap, live);
    expect(view.banner).toEqual({ text: "бэкенд недоступен: Prometheus", tone: "crit" });
    expect(view.rows.map((r) => r.key)).toEqual(["request", "latency", "limit5h", "limit7d"]);
    expect(row(view, "limit5h").tone).toBe("ok");
  });

  it("данные устарели: возраст в баннере, числа погашены", () => {
    const snap = okSnapshot();
    snap.status = "stale";
    snap.data_age_seconds = 95;
    const view = buildView(snap, live);
    expect(view.banner).toEqual({ text: "данные устарели · 1м", tone: "warn" });
    expect(row(view, "rate").tone).toBe("muted");
  });

  it("нет данных: баннер и ни одной строки с нулями", () => {
    const snap = okSnapshot();
    Object.assign(snap, { status: "no_data", session: null, rate: null, today: null, window_5h: null, limits: null, events: null, levels: {} });
    const view = buildView(snap, live);
    expect(view.banner).toEqual({ text: "нет данных", tone: "info" });
    expect(view.rows).toEqual([]);
  });

  it("нет активной сессии: итоги и лимиты остаются", () => {
    const snap = okSnapshot();
    Object.assign(snap, { status: "idle", session: null, active_sessions: 0 });
    const view = buildView(snap, live);
    expect(view.banner?.text).toBe("нет активной сессии");
    expect(view.title).toBe("");
    expect(view.rows.map((r) => r.key)).toEqual(["request", "latency", "rate", "today", "limit5h", "limit7d"]);
  });
});

describe("неполные данные", () => {
  it("пустые поля сессии показываются прочерком, а не нулём", () => {
    const snap = okSnapshot();
    Object.assign(snap.session!, { tokens: null, cost_usd: null, duration_seconds: null, model: null, context: null });
    const view = buildView(snap, live);
    expect(view.title).toBe("—");
    expect(view.rows.some((r) => r.key === "session")).toBe(false);
    expect(view.rows.some((r) => r.key === "context")).toBe(false);
    expect(row(view, "cost").text).toBe("— · —");
    // Ноль как самостоятельное число («0%», «0с»), а не цифра внутри «5.0с».
    expect(JSON.stringify(view)).not.toMatch(/(^|[^\d.])0(\.0+)?[%кkс]/);
  });

  it("без настоящего лимита показывает расход за 5 часов как ориентир", () => {
    const snap = okSnapshot();
    snap.limits = null;
    const r = row(buildView(snap, live), "limit5h");
    expect(r).toMatchObject({ label: "за 5ч", text: "121k ток · ≈$0.52", tone: "muted", hint: "ориентир, не официальный лимит" });
    expect(r.bar).toBeUndefined();
  });

  it("устаревший лимит и устаревший контекст погашены", () => {
    const snap = okSnapshot();
    snap.limits!.stale = true;
    snap.session!.context!.stale = true;
    const view = buildView(snap, live);
    expect(row(view, "limit5h")).toMatchObject({ tone: "muted", hint: "значение устарело" });
    expect(row(view, "context").tone).toBe("muted");
  });

  it("неточный итог за день помечен тильдой", () => {
    const snap = okSnapshot();
    snap.today!.approximate = true;
    expect(row(buildView(snap, live), "today").text).toBe("~121k ток · ≈$0.52");
  });
});

describe("события", () => {
  it("последний запрос и задержка", () => {
    const view = buildView(okSnapshot(), live);
    expect(row(view, "request")).toMatchObject({ text: "6.6с · ttft 5.0с", hint: "7с назад · sonnet 5.5" });
    expect(row(view, "latency")).toMatchObject({ text: "p50 4.1с · p95 9.8с", hint: "12 запр. за 1ч 00м" });
  });

  it("ноль ошибок строки не занимает", () => {
    expect(buildView(okSnapshot(), live).rows.some((r) => r.key === "errors")).toBe(false);
  });

  it("ошибки API видны и окрашены", () => {
    const snap = okSnapshot();
    snap.events!.errors = {
      window_seconds: 900,
      count: 3,
      last: { at: "", age_seconds: 125, status_code: "529", model: null, attempt: 1 },
    };
    snap.levels.api_errors_15m = "crit";
    const r = row(buildView(snap, live), "errors");
    expect(r).toMatchObject({ label: "ошибки API", text: "3 за 15м · 529", tone: "crit", hint: "последняя 2м назад" });
    // В компактном режиме ошибки остаются: это то, что нельзя пропустить.
    expect(buildView(snap, { connected: true, compact: true }).rows.map((x) => x.key)).toContain("errors");
  });

  it("Loki недоступен — явная погашенная строка", () => {
    const snap = okSnapshot();
    snap.events = { available: false, last_request: null, errors: null, latency: null };
    const view = buildView(snap, live);
    expect(row(view, "events")).toMatchObject({ text: "Loki недоступен", tone: "muted" });
    expect(view.rows.some((r) => r.key === "request" || r.key === "latency")).toBe(false);
    expect(view.banner).toBeNull();
  });

  it("Loki не настроен — строк событий нет", () => {
    const snap = okSnapshot();
    snap.events = null;
    const keys = buildView(snap, live).rows.map((r) => r.key);
    expect(keys).not.toContain("events");
    expect(keys).not.toContain("request");
  });

  it("без запросов за час строки задержки нет", () => {
    const snap = okSnapshot();
    snap.events!.latency = { window_seconds: 3600, requests: 0, p50_ms: null, p95_ms: null };
    expect(buildView(snap, live).rows.some((r) => r.key === "latency")).toBe(false);
  });
});

describe("компактный режим", () => {
  it("оставляет контекст, скорость и лимит без подсказок", () => {
    const view = buildView(okSnapshot(), { connected: true, compact: true });
    expect(view.rows.map((r) => r.key)).toEqual(["context", "rate", "limit5h"]);
    expect(view.rows.every((r) => r.hint === undefined)).toBe(true);
  });
});
