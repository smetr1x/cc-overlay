// Форматирование чисел для узкого окна оверлея.
// Любая функция на null, undefined и NaN возвращает прочерк: ноль вместо отсутствия данных показывать нельзя.

export const NO_VALUE = "—";

type Num = number | null | undefined;

function missing(v: Num): v is null | undefined {
  return v === null || v === undefined || !Number.isFinite(v);
}

/** Токены: 999, 1.2k, 25.6k, 121k, 1.2M. */
export function tokens(v: Num): string {
  if (missing(v)) return NO_VALUE;
  const abs = Math.abs(v);
  if (abs < 1000) return String(Math.round(v));
  if (abs < 99_950) return `${(v / 1000).toFixed(1)}k`;
  if (abs < 999_500) return `${Math.round(v / 1000)}k`;
  return `${(v / 1_000_000).toFixed(1)}M`;
}

/** Оценка стоимости. Знак «≈» обязателен: это цены API, а не списания по подписке. */
export function usd(v: Num): string {
  if (missing(v)) return NO_VALUE;
  if (v > 0 && v < 0.005) return "≈$<0.01";
  return `≈$${v.toFixed(v < 100 ? 2 : 0)}`;
}

/** Процент без дробной части; approx добавляет «~» для оценок. */
export function percent(v: Num, approx = false): string {
  if (missing(v)) return NO_VALUE;
  return `${approx ? "~" : ""}${Math.round(v)}%`;
}

/** Длительность: 45с, 15м, 2ч 05м, 3д 4ч. */
export function duration(seconds: Num): string {
  if (missing(seconds)) return NO_VALUE;
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}с`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}м`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}ч ${String(m % 60).padStart(2, "0")}м`;
  return `${Math.floor(h / 24)}д ${h % 24}ч`;
}

/** Миллисекунды: 850мс, 6.6с, 95с. */
export function millis(v: Num): string {
  if (missing(v)) return NO_VALUE;
  if (v < 1000) return `${Math.round(v)}мс`;
  if (v < 10_000) return `${(v / 1000).toFixed(1)}с`;
  return `${Math.round(v / 1000)}с`;
}

/** Короткое имя модели: claude-sonnet-5-5 → sonnet 5.5. */
export function model(id: string | null | undefined): string {
  if (!id) return NO_VALUE;
  return id.replace(/^claude-/, "").replace(/-(\d+)-(\d+)(?=$|\[)/, " $1.$2");
}
