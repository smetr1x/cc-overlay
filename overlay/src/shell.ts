// Связь с нативной оболочкой окна (src-tauri). В браузере оболочки нет, и модуль ничего не делает.

import { LogicalSize } from "@tauri-apps/api/dpi";
import { listen } from "@tauri-apps/api/event";
import { getCurrentWindow } from "@tauri-apps/api/window";

export type ShellCommand =
  | { kind: "move"; on: boolean }
  | { kind: "compact" }
  | { kind: "opacity"; delta: number };

export function inShell(): boolean {
  return "__TAURI_INTERNALS__" in window;
}

/**
 * Подгоняет окно под размер панели. Иначе прозрачная часть окна либо обрезает текст,
 * либо остаётся пустой, а в режиме перемещения рамка охватывает лишнее.
 */
export function fitWindowTo(panel: HTMLElement): void {
  if (!inShell()) return;
  const win = getCurrentWindow();
  let last = "";
  new ResizeObserver(() => {
    const rect = panel.getBoundingClientRect();
    const size = `${Math.ceil(rect.width)}x${Math.ceil(rect.height)}`;
    if (size === last || rect.width === 0) return;
    last = size;
    void win.setSize(new LogicalSize(Math.ceil(rect.width), Math.ceil(rect.height)));
  }).observe(panel);
}

/** Подписывается на команды оболочки: их вызывают глобальные хоткеи. */
export function onShellCommand(handler: (command: ShellCommand) => void): void {
  if (!inShell()) return;
  void listen<ShellCommand>("overlay-command", (event) => handler(event.payload));
}
