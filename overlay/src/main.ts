import { render } from "./render";
import { connect } from "./stream";
import type { Snapshot } from "./types";
import { buildView } from "./view";

const root = document.getElementById("overlay");
if (!root) throw new Error("нет элемента #overlay");

const state: { snapshot: Snapshot | null; connected: boolean; compact: boolean } = {
  snapshot: null,
  connected: false,
  compact: localStorage.getItem("compact") === "1",
};

function redraw(): void {
  document.body.classList.toggle("compact", state.compact);
  render(root!, buildView(state.snapshot, { connected: state.connected, compact: state.compact }));
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

// Переключение режима клавишей нужно при просмотре в браузере; в окне оверлея клики и клавиши
// проходят сквозь него, там режим переключает глобальный хоткей.
window.addEventListener("keydown", (event) => {
  if (event.key === "c") {
    state.compact = !state.compact;
    localStorage.setItem("compact", state.compact ? "1" : "0");
    redraw();
  }
});

redraw();
