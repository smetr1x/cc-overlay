// Адрес overlay-api. В разработке запросы идут через прокси Vite (тот же origin),
// в собранном приложении — напрямую. 127.0.0.1, а не localhost: имя localhost в Windows
// может разрешиться в ::1, который WSL не пробрасывает (docs/architecture.md).
export const API_BASE: string =
  import.meta.env.VITE_API_BASE ?? (import.meta.env.DEV ? "" : "http://127.0.0.1:8787");

/** Нет событий дольше этого срока — связь считается потерянной. Сервис шлёт снимок раз в секунду. */
export const SILENCE_TIMEOUT_MS = 5000;
