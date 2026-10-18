//! The loopback reverse proxy.
//!
//! Why a proxy at all: the console is written for the web. Every request it
//! makes is a relative URL (`/api/...`, `BASE = ''` in `web/src/lib/api.js`) and
//! every request carries `credentials: 'include'`, so the session cookie has to
//! live on the origin the page came from. Serving the console from a
//! `tauri://`-style custom protocol would break both — and Tauri v2 custom
//! protocol responses are a single buffered byte vector, which cannot carry a
//! server-sent event stream at all.
//!
//! So the shell serves the console from `http://127.0.0.1:<random port>`, where
//! anything the console asks for is forwarded to the server the user configured.
//! Relative URLs, `HttpOnly` cookies, SSE and file downloads then behave exactly
//! as they do in a browser, and neither the console nor the Go server changes.

use std::sync::Arc;
use std::time::Duration;

use axum::extract::{Request, State};
use axum::http::StatusCode;
use axum::response::Response;
use axum::Router;
use tokio::net::TcpListener;
use tokio::task::JoinHandle;

use crate::config::{ConfigStore, SharedConfig};

pub mod forward;
pub mod shell;
pub mod static_files;

/// Everything under this prefix belongs to the shell, never to the server.
pub const SHELL_PREFIX: &str = "/__shell";

/// Bounded so a server that never answers does not hang the handshake; there is
/// deliberately no overall timeout, because SSE streams stay open.
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

/// Console requests are JSON-RPC envelopes; 32 MiB is far past anything they
/// need while still stopping an accidental runaway upload.
pub const MAX_REQUEST_BODY: usize = 32 * 1024 * 1024;

#[derive(Clone)]
pub struct ProxyState {
    pub clients: forward::Clients,
    pub config: SharedConfig,
    pub store: Arc<ConfigStore>,
    /// The loopback origin, used to rewrite redirects that point back at the
    /// server so an OAuth round trip stays inside the app.
    pub self_url: String,
    /// `None` only in tests, which drive the proxy without a window.
    pub app: Option<tauri::AppHandle>,
}

pub struct ProxyHandle {
    pub port: u16,
    pub url: String,
    /// Kept so the listener task is not dropped at the end of `start`.
    _task: JoinHandle<()>,
}

#[derive(Debug)]
pub enum ProxyError {
    Bind(std::io::Error),
    Client(reqwest::Error),
}

impl std::fmt::Display for ProxyError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ProxyError::Bind(err) => write!(f, "cannot listen on 127.0.0.1: {err}"),
            ProxyError::Client(err) => write!(f, "cannot build the forwarding client: {err}"),
        }
    }
}

/// Binds 127.0.0.1 on a port the OS picks, so two instances never collide and
/// nothing outside the machine can reach it.
pub async fn start(
    config: SharedConfig,
    store: Arc<ConfigStore>,
    app: tauri::AppHandle,
) -> Result<ProxyHandle, ProxyError> {
    start_inner(config, store, Some(app)).await
}

async fn start_inner(
    config: SharedConfig,
    store: Arc<ConfigStore>,
    app: Option<tauri::AppHandle>,
) -> Result<ProxyHandle, ProxyError> {
    let jar = Arc::new(reqwest::cookie::Jar::default());
    let clients = forward::Clients::new(jar).map_err(ProxyError::Client)?;

    let listener = TcpListener::bind("127.0.0.1:0").await.map_err(ProxyError::Bind)?;
    let port = listener.local_addr().map_err(ProxyError::Bind)?.port();
    let url = format!("http://127.0.0.1:{port}");

    let state = ProxyState { clients, config, store, self_url: url.clone(), app };
    let router = Router::new().fallback(dispatch).with_state(state);
    let task = tokio::spawn(async move {
        if let Err(err) = axum::serve(listener, router).await {
            eprintln!("agenthub desktop: the loopback proxy stopped: {err}");
        }
    });

    Ok(ProxyHandle { port, url, _task: task })
}

async fn dispatch(State(state): State<ProxyState>, req: Request) -> Response {
    let path = req.uri().path().to_string();
    if path == SHELL_PREFIX || path.starts_with(&format!("{SHELL_PREFIX}/")) {
        return shell::handle(&state, req).await;
    }

    let config = state.config.read().await.clone();

    if is_remote_path(&path) {
        return forward::forward(&state, req).await;
    }
    // Before the user has pointed the shell at a server, the console would be
    // four screens of nothing, so the root shows the connection page instead.
    if !config.is_configured() && (path.is_empty() || path == "/" || path == "/index.html") {
        return static_files::shell_page("connect", &config);
    }
    static_files::serve_console(&path, config.lang())
}

/// The paths that belong to the server rather than to the console bundle.
///
/// `/.well-known/` is easy to miss — the Vite dev proxy does not list it — but
/// it is how another agent discovers this one.
pub fn is_remote_path(path: &str) -> bool {
    path == "/api"
        || path.starts_with("/api/")
        || path == "/a2a"
        || path.starts_with("/a2a/")
        || path == "/healthz"
        || path == "/.well-known"
        || path.starts_with("/.well-known/")
}

/// What the console gets when it calls an endpoint with no server configured.
pub fn not_configured() -> Response {
    forward::upstream_json(StatusCode::SERVICE_UNAVAILABLE, "server_not_configured")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn forwards_the_four_prefixes_the_console_uses() {
        for path in [
            "/api/resources",
            "/api/tasks/1/events",
            "/api/backups/1/download",
            "/api/auth/oauth/github",
            "/a2a",
            "/a2a/agent-card",
            "/a2a/stream",
            "/healthz",
            "/.well-known/agent.json",
        ] {
            assert!(is_remote_path(path), "{path} belongs to the server");
        }
    }

    #[test]
    fn keeps_the_console_and_the_shell_local() {
        for path in ["/", "/index.html", "/assets/index-abc.js", "/favicon.svg", "/trace", "/__shell/connect"] {
            assert!(!is_remote_path(path), "{path} must not be forwarded");
        }
    }

    #[test]
    fn does_not_mistake_a_lookalike_for_an_api_path() {
        assert!(!is_remote_path("/apifoo"));
        assert!(!is_remote_path("/a2a-live"));
    }

    // ---- end to end: a real proxy, a real (fake) server, real HTTP ----

    use crate::config::AppConfig;

    /// A proxy listening on its own port, with the config the caller asked for.
    async fn proxy_with(config: AppConfig) -> ProxyHandle {
        let dir = std::env::temp_dir().join(format!("agenthub-proxy-{}", port_stamp()));
        let store = Arc::new(ConfigStore::new(dir));
        start_inner(Arc::new(tokio::sync::RwLock::new(config)), store, None)
            .await
            .expect("the proxy must start")
    }

    fn port_stamp() -> String {
        use std::sync::atomic::{AtomicU32, Ordering};
        static NEXT: AtomicU32 = AtomicU32::new(0);
        let pid = std::process::id();
        format!("{}-{}", pid, NEXT.fetch_add(1, Ordering::Relaxed))
    }

    /// A stand-in agenthub server: `/healthz`, and an `/api/me` that sets a
    /// session cookie the way a production deployment does.
    async fn spawn_server() -> String {
        use axum::http::header::{CACHE_CONTROL, CONTENT_TYPE, SET_COOKIE};
        use axum::routing::get;
        use axum::Json as AxumJson;

        let app = axum::Router::new()
            .route(
                "/healthz",
                get(|| async { ([(CONTENT_TYPE, "application/json")], AxumJson(serde_json::json!({"ok":true}))) }),
            )
            .route(
                "/api/me",
                get(|| async {
                    (
                        [
                            (CONTENT_TYPE, "application/json".to_string()),
                            (CACHE_CONTROL, "no-store".to_string()),
                            (
                                SET_COOKIE,
                                "session=abc; Path=/api; Domain=hub.example.com; Secure; HttpOnly; SameSite=None"
                                    .to_string(),
                            ),
                        ],
                        AxumJson(serde_json::json!({"email":"owner@example.com"})),
                    )
                }),
            );
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        format!("http://127.0.0.1:{port}")
    }

    /// A server that writes one event, waits for the gate, then writes another.
    /// It is deliberately raw TCP: the point is to control exactly when the
    /// bytes hit the wire.
    async fn spawn_gated_stream() -> (String, tokio::sync::oneshot::Sender<()>) {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let (gate_tx, gate_rx) = tokio::sync::oneshot::channel::<()>();
        tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut head = Vec::new();
            let mut byte = [0u8; 1];
            while !head.ends_with(b"\r\n\r\n") {
                if socket.read(&mut byte).await.unwrap_or(0) == 0 {
                    break;
                }
                head.push(byte[0]);
            }
            socket
                .write_all(
                    b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n",
                )
                .await
                .unwrap();
            socket.write_all(b"event: first\ndata: one\n\n").await.unwrap();
            socket.flush().await.unwrap();
            // The second half is only written once the test has seen the first.
            let _ = gate_rx.await;
            socket.write_all(b"event: last\ndata: two\n\n").await.unwrap();
            let _ = socket.flush().await;
        });
        (format!("http://127.0.0.1:{port}"), gate_tx)
    }

    #[tokio::test]
    async fn serves_the_connection_page_until_a_server_is_configured() {
        let proxy = proxy_with(AppConfig::default()).await;
        let resp = reqwest::get(format!("{}/", proxy.url)).await.unwrap();
        assert_eq!(resp.status(), 200);
        let body = resp.text().await.unwrap();
        // The Rust copy is injected, so the page has no strings of its own.
        assert!(body.contains("window.__SHELL__"), "the boot object is missing");
        assert!(body.contains("Connect to agenthub"));
    }

    #[tokio::test]
    async fn refuses_api_calls_while_unconfigured() {
        let proxy = proxy_with(AppConfig::default()).await;
        let resp = reqwest::get(format!("{}/api/resources", proxy.url)).await.unwrap();
        assert_eq!(resp.status(), 503);
        let body: serde_json::Value = resp.json().await.unwrap();
        assert_eq!(body["code"], "server_not_configured");
    }

    #[tokio::test]
    async fn forwards_to_the_server_and_rewrites_the_session_cookie() {
        let server = spawn_server().await;
        let proxy = proxy_with(AppConfig { server_url: server.clone(), ..Default::default() }).await;

        let client = reqwest::Client::new();
        let resp = client.get(format!("{}/api/me", proxy.url)).send().await.unwrap();
        assert_eq!(resp.status(), 200);
        let cookies: Vec<String> = resp
            .headers()
            .get_all("set-cookie")
            .iter()
            .map(|value| value.to_str().unwrap().to_string())
            .collect();
        assert_eq!(cookies, vec!["session=abc; HttpOnly; SameSite=Lax; Path=/".to_string()]);
        // The header that would stop the WebView from storing it is gone.
        assert!(!cookies[0].to_lowercase().contains("secure"));
        assert!(!cookies[0].to_lowercase().contains("domain"));

        let body: serde_json::Value = resp.json().await.unwrap();
        assert_eq!(body["email"], "owner@example.com");
    }

    #[tokio::test]
    async fn streams_server_sent_events_without_buffering() {
        let (server, gate) = spawn_gated_stream().await;
        let proxy = proxy_with(AppConfig { server_url: server, ..Default::default() }).await;

        let client = reqwest::Client::new();
        let mut resp =
            client.get(format!("{}/api/tasks/1/events", proxy.url)).send().await.unwrap();
        assert_eq!(resp.status(), 200);
        assert!(resp.headers().get("content-length").is_none(), "a stream must not declare a length");
        assert_eq!(
            resp.headers().get("content-type").unwrap().to_str().unwrap(),
            "text/event-stream"
        );

        // The server is holding the second event back until we release it. If
        // the proxy buffered the body, this read would not return at all.
        let first = tokio::time::timeout(std::time::Duration::from_secs(5), resp.chunk())
            .await
            .expect("the proxy buffered the stream instead of forwarding it")
            .unwrap()
            .expect("the stream ended before the first event");
        assert_eq!(String::from_utf8_lossy(&first), "event: first\ndata: one\n\n");

        let _ = gate.send(());
        let second = tokio::time::timeout(std::time::Duration::from_secs(5), resp.chunk())
            .await
            .expect("the stream stopped after the first event")
            .unwrap()
            .expect("the stream ended before the last event");
        assert_eq!(String::from_utf8_lossy(&second), "event: last\ndata: two\n\n");
    }

    #[tokio::test]
    async fn falls_back_to_the_console_for_a_client_route() {
        // The console bundle is a build artefact; where it is not built the
        // shell says so instead, which is the other half of this behaviour.
        if static_files::Console::get("index.html").is_none() {
            eprintln!("skipping: backend/static has not been built");
            return;
        }
        let proxy = proxy_with(AppConfig { server_url: "http://127.0.0.1:1".into(), ..Default::default() })
            .await;
        let resp = reqwest::get(format!("{}/trace", proxy.url)).await.unwrap();
        assert_eq!(resp.status(), 200);
        let body = resp.text().await.unwrap();
        assert!(body.contains("<div id=\"app\""), "the console shell did not come back");
    }

    #[tokio::test]
    async fn saves_the_connection_and_hands_it_back() {
        let server = spawn_server().await;
        let dir = std::env::temp_dir().join(format!("agenthub-save-{}", port_stamp()));
        let store = Arc::new(ConfigStore::new(dir));
        let proxy = start_inner(
            Arc::new(tokio::sync::RwLock::new(AppConfig::default())),
            store.clone(),
            None,
        )
        .await
        .unwrap();
        let client = reqwest::Client::new();

        let saved: serde_json::Value = client
            .post(format!("{}/__shell/config", proxy.url))
            .json(&serde_json::json!({ "server_url": format!("{server}/"), "lang": "zh-TW" }))
            .send()
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        assert_eq!(saved["ok"], true);
        assert_eq!(store.load().server_url, server, "the trailing slash is dropped once");
        assert_eq!(store.load().lang(), "zh-TW");

        let probe: serde_json::Value = client
            .post(format!("{}/__shell/test", proxy.url))
            .json(&serde_json::json!({ "server_url": server }))
            .send()
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        assert_eq!(probe["ok"], true, "the server answers /healthz");

        let bad: serde_json::Value = client
            .post(format!("{}/__shell/test", proxy.url))
            .json(&serde_json::json!({ "server_url": "hub.example.com" }))
            .send()
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        assert_eq!(bad["ok"], false);
        assert_eq!(bad["reason"], "invalid");
    }
}
