import { render } from "./render";
import { fitWindowTo, onShellCommand } from "./shell";
import { connect } from "./stream";
import type { Snapshot } from "./types";
import { buildView } from "./view";

const root = document.getElementById("overlay");
if (!root) throw new Error("нет элемента #overlay");

// Настройки вида переживают перезапуск: в окне оверлея localStorage хранится в профиле WebView2.
const stored = {
  compact: localStorage.getItem("compact") === "1",
  opacity: clampOpacity(Number(localStorage.getItem("opacity") ?? "1")),
};

const state: { snapshot: Snapshot | null; connected: boolean; compact: boolean; opacity: number; move: boolean } = {
  snapshot: null,
  connected: false,
  compact: stored.compact,
  opacity: stored.opacity,
  move: false,
};

function clampOpacity(value: number): number {
  if (!Number.isFinite(value)) return 1;
  // Нижняя граница не даёт сделать оверлей невидимым и потерять его.
  return Math.min(1, Math.max(0.2, Math.round(value * 10) / 10));
}

function redraw(): void {
  document.body.classList.toggle("compact", state.compact);
  document.body.classList.toggle("move-mode", state.move);
  document.documentElement.style.setProperty("--overlay-opacity", String(state.opacity));
  render(root!, buildView(state.snapshot, { connected: state.connected, compact: state.compact }));
}

function toggleCompact(): void {
  state.compact = !state.compact;
  localStorage.setItem("compact", state.compact ? "1" : "0");
  redraw();
}

connect({
  onSnapshot(snapshot) {
    state.snapshot = snapshot;
    redraw();
  },
  onConnection(connected) {
    state.connected = connected;
    redraw();
  },
});

fitWindowTo(root);

// Команды глобальных хоткеев из оболочки окна.
onShellCommand((command) => {
  switch (command.kind) {
    case "move":
      state.move = command.on;
      break;
    case "compact":
      toggleCompact();
      return;
    case "opacity":
      state.opacity = clampOpacity(state.opacity + command.delta);
      localStorage.setItem("opacity", String(state.opacity));
      break;
  }
  redraw();
});

// В браузере хоткеев оболочки нет, режим переключается клавишей.
// В окне оверлея клавиши до страницы не доходят: мышь и клавиатура проходят сквозь него.
window.addEventListener("keydown", (event) => {
  if (event.key === "c") toggleCompact();
});

redraw();
