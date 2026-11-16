// Получение снимков: первый — обычным запросом, дальше — поток SSE (ADR-0006).

import { API_BASE, SILENCE_TIMEOUT_MS } from "./config";
import type { Snapshot } from "./types";

export interface Handlers {
  onSnapshot(snapshot: Snapshot): void;
  onConnection(connected: boolean): void;
}

export function connect(handlers: Handlers): () => void {
  let silence: ReturnType<typeof setTimeout> | undefined;
  let connected = false;

  const setConnected = (value: boolean) => {
    if (value !== connected) {
      connected = value;
      handlers.onConnection(value);
    }
  };

  // Соединение может висеть открытым и при этом молчать (сервис завис, сеть пропала без разрыва).
  // Поэтому о потере связи судим по тишине, а не только по событию ошибки.
  const heard = () => {
    setConnected(true);
    clearTimeout(silence);
    silence = setTimeout(() => setConnected(false), SILENCE_TIMEOUT_MS);
  };

  const accept = (raw: string) => {
    try {
      handlers.onSnapshot(JSON.parse(raw) as Snapshot);
      heard();
    } catch {
      // Повреждённое событие пропускаем: следующее придёт через секунду и будет полным.
    }
  };

  // Первый снимок сразу, не дожидаясь установки потока.
  fetch(`${API_BASE}/api/v1/snapshot`)
    .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
    .then(accept)
    .catch(() => setConnected(false));

  // EventSource переподключается сам; сервис подсказывает интервал полем retry.
  const source = new EventSource(`${API_BASE}/api/v1/stream`);
  source.onmessage = (event) => accept(event.data as string);
  source.onerror = () => setConnected(false);

  return () => {
    clearTimeout(silence);
    source.close();
  };
}
