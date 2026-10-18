//! The shell's own endpoints: `/__shell/*`.
//!
//! They exist so the connection page can talk to the Rust side without any IPC
//! permissions at all — it is a plain same-origin page talking to the loopback
//! proxy, which is already the only thing the window is allowed to reach.

use std::time::Duration;

use axum::body::to_bytes;
use axum::extract::Request;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use serde::Deserialize;
use serde_json::json;
use tauri::Manager;

use crate::config::{normalize_server_url, UrlError};
use crate::proxy::{forward, static_files, ProxyState};

const PROBE_TIMEOUT: Duration = Duration::from_secs(15);
const MAX_BODY: usize = 64 * 1024;

#[derive(Debug, Deserialize)]
struct ConfigPayload {
    server_url: Option<String>,
    accept_invalid_certs: Option<bool>,
    lang: Option<String>,
}

pub async fn handle(state: &ProxyState, req: Request) -> Response {
    let method = req.method().as_str().to_ascii_uppercase();
    let path = req.uri().path().to_string();
    match (method.as_str(), path.as_str()) {
        ("GET", "/__shell" | "/__shell/" | "/__shell/connect") => page(state, "connect").await,
        ("GET", "/__shell/about") => page(state, "about").await,
        ("GET", "/__shell/config") => read_config(state).await,
        ("POST", "/__shell/config") => save_config(state, req).await,
        ("POST", "/__shell/test") => test_connection(req).await,
        ("POST", "/__shell/hide") => hide_window(state),
        _ => (
            StatusCode::NOT_FOUND,
            axum::Json(json!({ "code": "not_found" })),
        )
            .into_response(),
    }
}

async fn page(state: &ProxyState, name: &str) -> Response {
    // Clone and drop the guard before rendering: keeping it would block a
    // connection save that is in flight.
    let config = state.config.read().await.clone();
    static_files::shell_page(name, &config)
}

async fn read_config(state: &ProxyState) -> Response {
    let config = state.config.read().await;
    axum::Json(json!({
        "serverUrl": config.server_url,
        "acceptInvalidCerts": config.accept_invalid_certs,
        "lang": config.lang(),
    }))
    .into_response()
}

async fn save_config(state: &ProxyState, req: Request) -> Response {
    let Some(payload) = decode(req).await else {
        return failure("invalid_json", StatusCode::BAD_REQUEST);
    };
    let mut config = state.config.write().await;
    if let Some(raw) = payload.server_url {
        let url = if raw.trim().is_empty() {
            String::new()
        } else {
            match normalize_server_url(&raw) {
                Ok(url) => url,
                Err(_) => return failure("invalid", StatusCode::BAD_REQUEST),
            }
        };
        config.server_url = url;
    }
    if let Some(insecure) = payload.accept_invalid_certs {
        config.accept_invalid_certs = insecure;
    }
    if let Some(lang) = payload.lang {
        config.lang = lang;
    }
    let snapshot = config.clone();
    drop(config);

    if let Err(err) = state.store.save(&snapshot) {
        eprintln!("agenthub desktop: cannot save the connection: {err}");
        return failure("not_saved", StatusCode::INTERNAL_SERVER_ERROR);
    }
    // The tray menu is written in the user's language, so it has to follow.
    if let Some(app) = &state.app {
        crate::tray::refresh(app, &snapshot);
    }
    axum::Json(json!({ "ok": true })).into_response()
}

async fn test_connection(req: Request) -> Response {
    let Some(payload) = decode(req).await else {
        return failure("invalid_json", StatusCode::BAD_REQUEST);
    };
    let base = match normalize_server_url(payload.server_url.as_deref().unwrap_or("")) {
        Ok(base) => base,
        Err(UrlError::Empty) => return failure("empty", StatusCode::BAD_REQUEST),
        Err(UrlError::Invalid) => return failure("invalid", StatusCode::BAD_REQUEST),
    };
    let insecure = payload.accept_invalid_certs.unwrap_or(false);
    let client = match reqwest::Client::builder()
        .danger_accept_invalid_certs(insecure)
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(crate::proxy::CONNECT_TIMEOUT)
        .timeout(PROBE_TIMEOUT)
        .build()
    {
        Ok(client) => client,
        Err(_) => return failure("unexpected", StatusCode::INTERNAL_SERVER_ERROR),
    };
    match client.get(format!("{base}/healthz")).send().await {
        // Any HTTP answer at all means the address was right: /healthz is the
        // one endpoint every deployment serves, including gated ones.
        Ok(resp) => axum::Json(json!({ "ok": true, "status": resp.status().as_u16() })).into_response(),
        Err(err) => {
            let reason = forward::classify(&err);
            eprintln!("agenthub desktop: the connection test failed ({reason})");
            failure(reason, StatusCode::OK)
        }
    }
}

fn hide_window(state: &ProxyState) -> Response {
    if let Some(app) = &state.app {
        if let Some(window) = app.get_webview_window("main") {
            let _ = window.hide();
        }
    }
    axum::Json(json!({ "ok": true })).into_response()
}

async fn decode(req: Request) -> Option<ConfigPayload> {
    let bytes = to_bytes(req.into_body(), MAX_BODY).await.ok()?;
    serde_json::from_slice(&bytes).ok()
}

fn failure(reason: &str, status: StatusCode) -> Response {
    (status, axum::Json(json!({ "ok": false, "reason": reason }))).into_response()
}
