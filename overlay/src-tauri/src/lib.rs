//! Оболочка окна оверлея.
//!
//! Страница (TypeScript) отвечает за содержимое; здесь только то, что умеет лишь нативное окно:
//! пропуск кликов, глобальные хоткеи, перемещение и сохранение позиции.
//!
//! Пропуск кликов в Tauri включается на всё окно целиком (ADR-0004), поэтому у окна два режима:
//! обычный — мышь проходит сквозь него; режим перемещения — окно ловит мышь и его можно тащить.

use std::sync::atomic::{AtomicBool, Ordering};

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager, WebviewWindow};
use tauri_plugin_global_shortcut::{Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutState};
use tauri_plugin_window_state::{AppHandleExt, StateFlags};

/// Имя события, которым оболочка сообщает странице о нажатии хоткея.
const COMMAND_EVENT: &str = "overlay-command";

/// Сохраняется только позиция: размер окна фиксирован, видимость после перезапуска всегда включена.
const SAVED_STATE: StateFlags = StateFlags::POSITION;

#[derive(Default)]
struct OverlayState {
    move_mode: AtomicBool,
    hidden: AtomicBool,
}

/// Команда странице. Поле `kind` различает варианты в JSON.
#[derive(Clone, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
enum Command {
    Move { on: bool },
    Compact,
    Opacity { delta: f64 },
}

// Хоткеи: Ctrl+Alt + клавиша. Сочетание редкое и не пересекается с VS Code и терминалом.
const MODS: Modifiers = Modifiers::CONTROL.union(Modifiers::ALT);

fn key(code: Code) -> Shortcut {
    Shortcut::new(Some(MODS), code)
}

fn main_window(app: &AppHandle) -> Option<WebviewWindow> {
    app.get_webview_window("main")
}

fn send(app: &AppHandle, command: Command) {
    // Ошибка отправки означает, что страница ещё не загрузилась; хоткей в этот момент просто не сработает.
    let _ = app.emit(COMMAND_EVENT, command);
}

fn toggle_move(app: &AppHandle) {
    let state = app.state::<OverlayState>();
    let on = !state.move_mode.fetch_xor(true, Ordering::SeqCst);
    if let Some(window) = main_window(app) {
        // В режиме перемещения окно должно ловить мышь, иначе его не за что тащить.
        let _ = window.set_ignore_cursor_events(!on);
    }
    if !on {
        // Позиция записывается при выходе из режима: если процесс потом завершат принудительно, она уже на диске.
        let _ = app.save_window_state(SAVED_STATE);
    }
    send(app, Command::Move { on });
}

fn toggle_hidden(app: &AppHandle) {
    let state = app.state::<OverlayState>();
    let hidden = !state.hidden.fetch_xor(true, Ordering::SeqCst);
    if let Some(window) = main_window(app) {
        let _ = if hidden { window.hide() } else { window.show() };
    }
}

fn on_shortcut(app: &AppHandle, shortcut: &Shortcut) {
    match shortcut.key {
        Code::KeyM => toggle_move(app),
        Code::KeyC => send(app, Command::Compact),
        Code::ArrowUp => send(app, Command::Opacity { delta: 0.1 }),
        Code::ArrowDown => send(app, Command::Opacity { delta: -0.1 }),
        Code::KeyH => toggle_hidden(app),
        Code::KeyQ => {
            let _ = app.save_window_state(SAVED_STATE);
            app.exit(0);
        }
        _ => {}
    }
}

pub fn run() {
    let shortcuts = [
        key(Code::KeyM),
        key(Code::KeyC),
        key(Code::ArrowUp),
        key(Code::ArrowDown),
        key(Code::KeyH),
        key(Code::KeyQ),
    ];

    tauri::Builder::default()
        .manage(OverlayState::default())
        .plugin(
            tauri_plugin_window_state::Builder::default()
                .with_state_flags(SAVED_STATE)
                .build(),
        )
        .plugin(
            tauri_plugin_global_shortcut::Builder::new()
                .with_handler(|app, shortcut, event| {
                    if event.state() == ShortcutState::Pressed {
                        on_shortcut(app, shortcut);
                    }
                })
                .build(),
        )
        .setup(move |app| {
            if let Some(window) = main_window(app.handle()) {
                window.set_ignore_cursor_events(true)?;
            }
            // Хоткей может быть занят другой программой. Оверлей при этом должен запуститься:
            // остальные сочетания продолжат работать.
            for shortcut in shortcuts {
                if let Err(err) = app.global_shortcut().register(shortcut) {
                    eprintln!("хоткей {shortcut:?} не зарегистрирован: {err}");
                }
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("не удалось запустить оверлей");
}
