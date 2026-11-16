/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Адрес overlay-api; переопределяет значение по умолчанию из config.ts. */
  readonly VITE_API_BASE?: string;
}
