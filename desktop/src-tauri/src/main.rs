// A release build must not open a console window behind the app on Windows;
// a debug build must, because that is where panics are read.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

// Everything the desktop app does is in the webview: the local connect page
// hands the window to the instance's own origin (desktop/ui/connect.js), and
// from there the instance's web application runs exactly as it does in a
// browser. There are no `#[tauri::command]`s, so there is no `lib.rs` and no
// `generate_handler!` — the Tauri template's split exists to let a mobile
// entry point replace `main`, and this app targets desktop only (ROADMAP
// Phase 4: Windows, macOS, Linux).
//
// ponytail: add lib.rs the day an iOS or Android target is real, not before.

fn main() {
    tauri::Builder::default()
        .run(tauri::generate_context!())
        .expect("the Hamlaneh desktop shell failed to start");
}
