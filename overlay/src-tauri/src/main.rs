// В релизной сборке не открывать консольное окно рядом с оверлеем.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    cc_overlay_lib::run()
}
