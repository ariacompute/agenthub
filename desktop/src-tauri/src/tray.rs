//! The tray icon: the one piece of native UI the shell owns.
//!
//! Four entries — open the console, change the connection, about, quit — in
//! whichever of the 13 languages the user picked. The menu is rebuilt whenever
//! that language changes.

use tauri::menu::{Menu, MenuEvent, MenuItem};
use tauri::{AppHandle, Manager};

use crate::config::AppConfig;
use crate::i18n;

pub const TRAY_ID: &str = "main";

const ID_OPEN: &str = "shell.open";
const ID_SETTINGS: &str = "shell.settings";
const ID_ABOUT: &str = "shell.about";
const ID_QUIT: &str = "shell.quit";

pub fn build(app: &AppHandle, config: &AppConfig) -> tauri::Result<()> {
    let menu = menu(app, config)?;
    match app.tray_by_id(TRAY_ID) {
        Some(tray) => tray.set_menu(Some(menu))?,
        None => {
            let icon = app.default_window_icon().cloned();
            let mut builder = tauri::tray::TrayIconBuilder::with_id(TRAY_ID).menu(&menu);
            if let Some(icon) = icon {
                builder = builder.icon(icon);
            }
            let _ = builder.build(app)?;
        }
    }
    Ok(())
}

/// Re-labels the menu, which is what a language change has to do.
pub fn refresh(app: &AppHandle, config: &AppConfig) {
    if let Ok(menu) = menu(app, config) {
        if let Some(tray) = app.tray_by_id(TRAY_ID) {
            let _ = tray.set_menu(Some(menu));
        }
    }
}

fn menu(app: &AppHandle, config: &AppConfig) -> tauri::Result<Menu<tauri::Wry>> {
    let lang = config.lang();
    let t = |key: &str| i18n::translate(lang, key);
    Menu::with_items(
        app,
        &[
            &MenuItem::with_id(app, ID_OPEN, t("trayOpen"), true, None::<&str>)?,
            &MenuItem::with_id(app, ID_SETTINGS, t("traySettings"), true, None::<&str>)?,
            &MenuItem::with_id(app, ID_ABOUT, t("trayAbout"), true, None::<&str>)?,
            &MenuItem::with_id(app, ID_QUIT, t("trayQuit"), true, None::<&str>)?,
        ],
    )
}

pub fn on_menu_event(app: &AppHandle, event: MenuEvent) {
    match event.id().as_ref() {
        ID_OPEN => open(app, "/"),
        ID_SETTINGS => open(app, "/__shell/connect"),
        ID_ABOUT => open(app, "/__shell/about"),
        ID_QUIT => app.exit(0),
        _ => {}
    }
}

fn open(app: &AppHandle, path: &str) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    if let Some(base) = app.try_state::<crate::ShellContext>() {
        let url = base.proxy_url.read().map(|url| url.clone()).unwrap_or_default();
        if !url.is_empty() {
            if let Ok(parsed) = url::Url::parse(&format!("{url}{path}")) {
                let _ = window.navigate(parsed);
            }
        }
    }
    let _ = window.show();
    let _ = window.set_focus();
}
