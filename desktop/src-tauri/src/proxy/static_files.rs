//! Static responses: the console bundle, and the two pages the shell owns.

use std::collections::BTreeMap;

use axum::body::Body;
use axum::http::header::{CACHE_CONTROL, CONTENT_TYPE};
use axum::http::{Response as HttpResponse, StatusCode};
use axum::response::{IntoResponse, Response};
use rust_embed::{EmbeddedFile, RustEmbed};
use serde_json::json;

use crate::i18n;

/// Marker the Rust side replaces with the boot object for `shell.html`.
pub const BOOT_MARKER: &str = "/*__SHELL_BOOT__*/";

/// The console, built by `web` into `backend/static` and embedded here so the
/// app is a single artefact with no bundled directory to lose.
///
/// The path is relative to this crate; `build.rs` creates the directory when it
/// is missing so a checkout that has not run the web build still compiles.
#[derive(RustEmbed)]
#[folder = "../../backend/static"]
pub struct Console;

/// The connection page and the about page.
#[derive(RustEmbed)]
#[folder = "assets"]
struct Shell;

/// Serves a console path, falling back to `index.html` for anything that is not
/// a file — the same rule as the Go server's `staticOrIndex`, so Vue's router
/// owns client-side paths here exactly as it does on the server.
pub fn serve_console(path: &str, lang: &str) -> Response {
    let rel = path.trim_start_matches('/');
    let rel = if rel.is_empty() { "index.html" } else { rel };
    if let Some(file) = Console::get(rel) {
        return file_response(rel, &file);
    }
    match Console::get("index.html") {
        Some(file) => file_response("index.html", &file),
        None => console_missing(lang),
    }
}

/// Renders `shell.html` with the shell's copy injected, so the page carries no
/// translations of its own and can never drift from Rust.
pub fn shell_page(page: &str, config: &crate::config::AppConfig) -> Response {
    let Some(file) = Shell::get("shell.html") else {
        return (StatusCode::INTERNAL_SERVER_ERROR, "the shell page is missing").into_response();
    };
    let lang = config.lang();
    let tables: BTreeMap<&str, BTreeMap<&str, &str>> = i18n::LANGUAGES
        .iter()
        .map(|(code, _)| (*code, i18n::block_for(code).into_iter().collect()))
        .collect();
    let dirs: BTreeMap<&str, &str> = i18n::LANGUAGES
        .iter()
        .map(|(code, _)| (*code, if i18n::is_rtl(code) { "rtl" } else { "ltr" }))
        .collect();
    let boot = json!({
        "page": page,
        "lang": lang,
        "version": crate::VERSION,
        "commit": crate::COMMIT,
        "serverUrl": config.server_url,
        "insecure": config.accept_invalid_certs,
        "configured": config.is_configured(),
        "languages": i18n::LANGUAGES,
        "dirs": dirs,
        "tables": tables,
    });
    let html = String::from_utf8_lossy(&file.data).replace(
        BOOT_MARKER,
        &format!("window.__SHELL__ = {boot};"),
    );
    HttpResponse::builder()
        .status(StatusCode::OK)
        .header(CONTENT_TYPE, "text/html; charset=utf-8")
        .header(CACHE_CONTROL, "no-cache")
        .body(Body::from(html.into_bytes()))
        .unwrap_or_else(|_| StatusCode::INTERNAL_SERVER_ERROR.into_response())
}

fn file_response(rel: &str, file: &EmbeddedFile) -> Response {
    // Hashed assets never change; everything else (index.html first of all) is
    // revalidated so a new build shows up on the next start.
    let cache = if rel.starts_with("assets/") {
        "public, max-age=31536000, immutable"
    } else {
        "no-cache"
    };
    HttpResponse::builder()
        .status(StatusCode::OK)
        .header(CONTENT_TYPE, file.metadata.mimetype())
        .header(CACHE_CONTROL, cache)
        .body(Body::from(file.data.to_vec()))
        .unwrap_or_else(|_| StatusCode::INTERNAL_SERVER_ERROR.into_response())
}

fn console_missing(lang: &str) -> Response {
    (
        StatusCode::SERVICE_UNAVAILABLE,
        [(CONTENT_TYPE, "text/plain; charset=utf-8")],
        i18n::translate(lang, "consoleMissing"),
    )
        .into_response()
}
