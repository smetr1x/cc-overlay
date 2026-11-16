// Контракт overlay-api: docs/api.md. Источник правды — overlay-api/internal/snapshot/snapshot.go.
//
// Главное правило контракта: null означает «нет данных», ноль — известное значение.
// Поэтому почти все числа здесь могут быть null, и интерфейс обязан это различать.

export type Status = "ok" | "idle" | "stale" | "no_data" | "backend_down";
export type Level = "ok" | "warn" | "crit";

export interface Tokens {
  input: number;
  output: number;
  cache_read: number;
  cache_creation: number;
}

export interface Context {
  used_percent: number;
  tokens: number | null;
  window_tokens: number | null;
  source: "statusline" | "telemetry";
  approximate: boolean;
  age_seconds: number;
  stale: boolean;
}

export interface Session {
  id: string;
  model: string | null;
  tokens: Tokens | null;
  cost_usd: number | null;
  active_seconds: number | null;
  started_at: string | null;
  duration_seconds: number | null;
  cache_hit_ratio: number | null;
  context: Context | null;
}

export interface Rate {
  tokens_per_minute: number | null;
  cost_usd_per_hour: number | null;
}

export interface Window {
  seconds: number;
  tokens: number | null;
  cache_read_tokens: number | null;
  cost_usd: number | null;
  approximate: boolean;
}

export interface Limit {
  used_percent: number;
  resets_at: string | null;
  resets_in_seconds: number | null;
}

export interface ScopedLimit extends Limit {
  label: string;
}

export interface Limits {
  five_hour: Limit | null;
  seven_day: Limit | null;
  scoped: ScopedLimit[] | null;
  source: "statusline" | "usage";
  age_seconds: number;
  stale: boolean;
}

export interface Snapshot {
  status: Status;
  generated_at: string;
  data_age_seconds: number | null;
  active_sessions: number | null;
  session: Session | null;
  rate: Rate | null;
  today: Window | null;
  window_5h: Window | null;
  limits: Limits | null;
  levels: Partial<Record<"cost_usd_per_hour" | "five_hour_percent" | "seven_day_percent" | "context_percent", Level>>;
}
