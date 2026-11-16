import { defineConfig } from "vite";

// В режиме разработки страница ходит к overlay-api через прокси Vite: запросы идут с того же
// origin, и разрешение CORS не требуется. В собранном приложении адрес сервиса задан явно (src/config.ts).
export default defineConfig({
  clearScreen: false,
  server: {
    host: process.env.OVERLAY_DEV_HOST ?? "127.0.0.1",
    port: 1420,
    strictPort: true,
    // На диске Windows, смонтированном в WSL, события файловой системы не приходят: только опрос.
    watch: process.env.OVERLAY_DEV_POLL ? { usePolling: true, interval: 500 } : undefined,
    proxy: {
      "/api": {
        target: process.env.OVERLAY_API_PROXY ?? "http://127.0.0.1:8787",
        changeOrigin: true,
      },
    },
  },
  build: {
    target: "es2022",
  },
});
