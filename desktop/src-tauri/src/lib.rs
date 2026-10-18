//! The agenthub desktop shell.
//!
//! It carries no backend of its own: it opens the existing agenthub console and
//! keeps it pointed at a server the user chooses. The interesting parts are the
//! loopback proxy (which makes the console's relative URLs, cookies and SSE
//! work against a remote server), the connection configuration, and the
//! 13-language copy for the few pieces of UI the shell owns.

use std::sync::{Arc, RwLock as StdRwLock};

use tauri::Manager;

pub mod config;
pub mod i18n;
pub mod proxy;
pub mod tray;

/// Stamped in by the release workflow so an installer reports the tag it came
/// from; `unknown` for a local build.
pub const VERSION: &str = env!("CARGO_PKG_VERSION");
pub const COMMIT: &str = match option_env!("AGENTHUB_COMMIT") {
    Some(commit) => commit,
    None => "unknown",
};

/// What the tray needs and the proxy fills in once it is listening.
pub struct ShellContext {
    pub config: config::SharedConfig,
    pub proxy_url: Arc<StdRwLock<String>>,
}

pub fn run() {
    tauri::Builder::default()
        .setup(|app| {
            let handle = app.handle().clone();

            let dir = app
                .path()
                .app_config_dir()
                .unwrap_or_else(|_| std::env::temp_dir().join("agenthub-desktop"));
            let store = Arc::new(config::ConfigStore::new(dir));
            let initial = store.load();

            let shared = Arc::new(tokio::sync::RwLock::new(initial.clone()));
            let proxy_url = Arc::new(StdRwLock::new(String::new()));
            app.manage(ShellContext { config: shared.clone(), proxy_url: proxy_url.clone() });

            tray::build(&handle, &initial)?;
            app.on_menu_event(tray::on_menu_event);

            // Closing the window parks the app in the tray; only the tray's own
            // Quit ends it.
            if let Some(window) = app.get_webview_window("main") {
                let parked = window.clone();
                window.on_window_event(move |event| {
                    if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                        api.prevent_close();
                        let _ = parked.hide();
                    }
                });
            }

            // The window stays hidden until the proxy answers: the first thing
            // it shows is the console (or the connection page), never a blank
            // frame.
            let started = shared.clone();
            let persisted = store.clone();
            let shell = handle.clone();
            tauri::async_runtime::spawn(async move {
                match proxy::start(started, persisted, shell.clone()).await {
                    Ok(proxy) => {
                        if let Ok(mut url) = proxy_url.write() {
                            *url = proxy.url.clone();
                        }
                        if let Some(window) = shell.get_webview_window("main") {
                            if let Ok(url) = url::Url::parse(&proxy.url) {
                                let _ = window.navigate(url);
                            }
                            let _ = window.show();
                        }
                    }
                    Err(err) => eprintln!("agenthub desktop: {err}"),
                }
            });

            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("agenthub desktop: cannot start the app");
}
