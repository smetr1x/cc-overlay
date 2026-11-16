# Тулчейны

Состояние машины проверено 2026-11-16.

## Windows: для сборки оверлея

| Что | Состояние | Как поставить |
|---|---|---|
| Visual Studio Build Tools 2022, workload «Desktop development with C++» (MSVC и Windows 11 SDK) | нет | `winget install Microsoft.VisualStudio.2022.BuildTools`, затем в установщике отметить workload. Занимает 6–8 ГБ |
| Rust через rustup, тулчейн `stable-x86_64-pc-windows-msvc` | нет | `winget install Rustlang.Rustup`, затем `rustup default stable-msvc` |
| WebView2 Runtime | есть, входит в Windows 11 | — |
| Node.js LTS с npm | есть | — |
| Git for Windows | есть | — |

После установки нужен новый терминал. Проверка: `rustc -vV` показывает `host: x86_64-pc-windows-msvc`, `cargo --version` и `node -v` отвечают.

Не требуется:

- **VBScript** — нужен только для MSI-пакета; оверлей распространяется как NSIS-установщик или отдельный `.exe`.
- **WSL-сборка Windows-бинарника** — кросс-компиляция Tauri из Linux официально не поддерживается.

Замечания:

- На диске C: свободно около 45 ГБ из-за заполнения на 91 %. Build Tools и каталог `target` Rust вместе займут 10–15 ГБ.
- Путь к профилю содержит кириллицу. Rust и MSVC с этим обычно справляются, но при странных ошибках линковки первым делом стоит перенести каталог сборки: `setx CARGO_TARGET_DIR C:\build\cc-overlay`.
- Собирать нужно из каталога на диске Windows, а не через `\\wsl$`: сборка по сетевому пути в разы медленнее.

## WSL: для стека

| Что | Состояние | Примечание |
|---|---|---|
| Docker Engine и Compose | есть: 29.1.3, v5.0.0 | — |
| git, make, curl | есть | — |
| jq | нет | `sudo apt install jq`; нужен для проверок в `make` и для скрипта status line |
| Go | нет | не ставим: сборка и тесты идут в контейнере |
| Node.js | нет | не нужен в WSL |

Линтеры и валидаторы (`yamllint`, `hadolint`, `promtool`, `otelcol-contrib validate`) запускаются из закреплённых образов, отдельно ставить их не нужно.

## Расположение репозитория

Сейчас репозиторий лежит в `/mnt/c/Users/…/work/cc-overlay`, то есть на диске Windows, доступном из WSL по протоколу 9p. Для Docker и git это медленно, права всех файлов читаются как `777`, а отслеживание изменений файлов не работает.

Принято решение перенести рабочую копию в `~/work/cc-overlay` внутри WSL, а оверлей собирать из отдельного клона на диске Windows. Перенос выполняет владелец:

```bash
mkdir -p ~/work && git clone /mnt/c/Users/Андрей/work/cc-overlay ~/work/cc-overlay
```

После появления удалённого репозитория оба клона синхронизируются через него.
