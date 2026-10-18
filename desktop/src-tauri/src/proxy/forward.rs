//! Forwarding one request to the configured agenthub server.
//!
//! Three details decide whether the console works at all:
//!
//! * the response body is a **stream**, never a buffered byte vector — the task
//!   event endpoints are server-sent events that stay open for minutes;
//! * `Set-Cookie` is rewritten before it reaches the WebView, because the
//!   session cookie is `HttpOnly` and, on a real deployment, `Secure`, and
//!   neither attribute survives a plain-http loopback origin;
//! * `Location` is rewritten when it points back at the server, so an OAuth
//!   round trip lands on the loopback origin instead of teleporting the user
//!   out of the app.

use std::borrow::Cow;
use std::sync::Arc;

use axum::body::{to_bytes, Body};
use axum::extract::Request;
use axum::http::header::{HeaderName, HeaderValue};
use axum::http::{Response as HttpResponse, StatusCode};
use axum::response::{IntoResponse, Response};
use reqwest::cookie::Jar;
use url::Url;

use super::{ProxyState, CONNECT_TIMEOUT, MAX_REQUEST_BODY};

/// Two clients, because "do not verify the certificate" is a build-time
/// property of a `reqwest::Client`. Both share one cookie jar so toggling the
/// setting never loses the session.
#[derive(Clone)]
pub struct Clients {
    pub verified: reqwest::Client,
    pub insecure: reqwest::Client,
}

impl Clients {
    pub fn new(jar: Arc<Jar>) -> Result<Self, reqwest::Error> {
        Ok(Clients {
            verified: build_client(false, jar.clone())?,
            insecure: build_client(true, jar)?,
        })
    }
}

fn build_client(accept_invalid_certs: bool, jar: Arc<Jar>) -> Result<reqwest::Client, reqwest::Error> {
    reqwest::Client::builder()
        .cookie_provider(jar)
        // SSE streams stay open for as long as the task runs, so there is no
        // overall request timeout; only the handshake is bounded.
        .connect_timeout(CONNECT_TIMEOUT)
        .danger_accept_invalid_certs(accept_invalid_certs)
        // Redirects belong to the WebView: a 302 to an OAuth provider has to be
        // followed by the browser, and a 302 back to the console has to be
        // rewritten to the loopback origin (see `rewrite_location`).
        .redirect(reqwest::redirect::Policy::none())
        .build()
}

/// Sends one request upstream and streams the answer back.
pub async fn forward(state: &ProxyState, req: Request) -> Response {
    let (base, insecure) = {
        let config = state.config.read().await;
        match config.server_url() {
            Some(base) => (base, config.accept_invalid_certs),
            None => return super::not_configured(),
        }
    };
    let client = if insecure {
        &state.clients.insecure
    } else {
        &state.clients.verified
    };

    let (parts, body) = req.into_parts();
    let Ok(url) = target_url(&base, parts.uri.path(), parts.uri.query()) else {
        return upstream_json(StatusCode::BAD_GATEWAY, "invalid_target");
    };
    let Ok(method) = reqwest::Method::from_bytes(parts.method.as_str().as_bytes()) else {
        return upstream_json(StatusCode::BAD_GATEWAY, "unsupported_method");
    };
    let Ok(body_bytes) = to_bytes(body, MAX_REQUEST_BODY).await else {
        return upstream_json(StatusCode::BAD_GATEWAY, "request_body_unreadable");
    };

    let mut out = reqwest::Request::new(method, url.clone());
    for (name, value) in parts.headers.iter() {
        if drop_request_header(name.as_str()) {
            continue;
        }
        if let Some((name, value)) = header(name.as_str(), value.as_bytes()) {
            out.headers_mut().append(name, value);
        }
    }
    // Identity encoding keeps SSE readable: a compressed stream would arrive as
    // one compressed blob, which is the classic "events appear only at the end"
    // failure. reqwest is built without gzip/deflate, so whatever comes back is
    // passed through byte for byte.
    out.headers_mut()
        .insert(reqwest::header::ACCEPT_ENCODING, HeaderValue::from_static("identity"));
    if !body_bytes.is_empty() {
        *out.body_mut() = Some(reqwest::Body::from(body_bytes));
    }

    match client.execute(out).await {
        Ok(resp) => build_response(state, resp, &base),
        Err(err) => {
            let reason = classify(&err);
            // Never log the URL: it may carry credentials in its userinfo.
            eprintln!("agenthub desktop: upstream request failed ({reason})");
            upstream_json(StatusCode::BAD_GATEWAY, reason)
        }
    }
}

fn build_response(state: &ProxyState, resp: reqwest::Response, base: &str) -> Response {
    let status = StatusCode::from_u16(resp.status().as_u16()).unwrap_or(StatusCode::BAD_GATEWAY);
    let mut builder = HttpResponse::builder().status(status);
    if let Some(headers) = builder.headers_mut() {
        for (name, value) in resp.headers().iter() {
            if drop_response_header(name.as_str()) {
                continue;
            }
            let is_set_cookie = name.as_str().eq_ignore_ascii_case("set-cookie");
            let is_location = name.as_str().eq_ignore_ascii_case("location");
            let value: Cow<'_, [u8]> = if is_set_cookie {
                match value.to_str() {
                    Ok(raw) => Cow::Owned(rewrite_set_cookie(raw).into_bytes()),
                    Err(_) => continue,
                }
            } else if is_location {
                match value.to_str() {
                    Ok(raw) => Cow::Owned(rewrite_location(raw, base, &state.self_url).into_bytes()),
                    Err(_) => continue,
                }
            } else {
                Cow::Borrowed(value.as_bytes())
            };
            if let Some((name, value)) = header(name.as_str(), value.as_ref()) {
                headers.append(name, value);
            }
        }
    }
    builder
        .body(Body::from_stream(resp.bytes_stream()))
        .unwrap_or_else(|_| upstream_json(StatusCode::BAD_GATEWAY, "response_unbuildable"))
}

/// The upstream URL for one console request: the configured base plus the path
/// the console asked for, keeping any prefix the server is mounted under.
pub fn target_url(base: &str, path: &str, query: Option<&str>) -> Result<Url, url::ParseError> {
    let mut url = Url::parse(&format!("{base}{path}"))?;
    url.set_query(query);
    Ok(url)
}

/// Header names that must not travel: hop-by-hop framing, anything the proxy
/// itself decides, and anything that would identify the loopback origin.
pub fn drop_request_header(name: &str) -> bool {
    const DROP: [&str; 12] = [
        "host",
        "content-length",
        "connection",
        "keep-alive",
        "transfer-encoding",
        "te",
        "upgrade",
        "proxy-authorization",
        "proxy-authenticate",
        // The jar owns cookies; forwarding the WebView's (always empty) header
        // would only risk duplicating it.
        "cookie",
        // Both identify the loopback origin, which means nothing upstream, and
        // a strict server would reject the request for them.
        "origin",
        "referer",
    ];
    DROP.contains(&name.to_ascii_lowercase().as_str())
}

/// Header names that must not come back: framing the proxy rewrites, security
/// policies written for the server's own origin, and CORS headers that only
/// make sense for a cross-origin caller (ours are same-origin).
pub fn drop_response_header(name: &str) -> bool {
    const DROP: [&str; 9] = [
        "content-length",
        "transfer-encoding",
        "connection",
        "keep-alive",
        "strict-transport-security",
        "content-security-policy",
        "content-security-policy-report-only",
        "x-frame-options",
        "alt-svc",
    ];
    DROP.contains(&name.to_ascii_lowercase().as_str())
}

/// Turns a `Set-Cookie` from the server into one the loopback origin will keep.
///
/// `Secure` has to go (the origin is plain http, even though it is localhost),
/// `Domain` has to go (it names the server, not 127.0.0.1), `SameSite=None`
/// becomes `Lax` (it would otherwise be rejected without `Secure`), and the
/// path is forced to `/` so the cookie rides along with every console request.
/// `HttpOnly` and `Max-Age`/`Expires` are left exactly as the server sent them.
pub fn rewrite_set_cookie(raw: &str) -> String {
    let mut out = String::new();
    for (index, part) in raw.split(';').enumerate() {
        let attr = part.trim();
        if attr.is_empty() {
            continue;
        }
        if index == 0 {
            out.push_str(attr);
            continue;
        }
        let lower = attr.to_ascii_lowercase();
        if lower == "secure" || lower.starts_with("domain=") || lower.starts_with("path=") {
            continue;
        }
        // SameSite=None is only accepted together with Secure, which we just
        // removed, so it has to become Lax; anything stricter is left alone.
        if let Some(value) = lower.strip_prefix("samesite=") {
            if value.trim() == "none" {
                out.push_str("; SameSite=Lax");
            } else {
                out.push_str("; ");
                out.push_str(attr);
            }
            continue;
        }
        out.push_str("; ");
        out.push_str(attr);
    }
    out.push_str("; Path=/");
    out
}

/// Sends a redirect that points back at the server through the loopback origin,
/// and leaves anything else (an OAuth provider, for instance) alone.
pub fn rewrite_location(raw: &str, base: &str, self_url: &str) -> String {
    if base.is_empty() || self_url.is_empty() || !raw.starts_with(base) {
        return raw.to_string();
    }
    format!("{}{}", self_url, &raw[base.len()..])
}

/// Why an upstream request failed, as a machine-readable reason the connection
/// page can translate.
pub fn classify(err: &reqwest::Error) -> &'static str {
    if err.is_timeout() {
        return "timeout";
    }
    let chain = error_chain(err).to_ascii_lowercase();
    if chain.contains("certificate")
        || chain.contains("unknown issuer")
        || chain.contains("self-signed")
        || chain.contains("self signed")
    {
        return "certificate";
    }
    if err.is_connect() {
        return "unreachable";
    }
    "unexpected"
}

fn error_chain(err: &reqwest::Error) -> String {
    let mut out = err.to_string();
    let mut source = std::error::Error::source(err);
    while let Some(err) = source {
        out.push(' ');
        out.push_str(&err.to_string());
        source = err.source();
    }
    out
}

pub(crate) fn upstream_json(status: StatusCode, code: &str) -> Response {
    let body = serde_json::json!({ "code": code });
    (status, axum::Json(body)).into_response()
}

fn header(name: &str, value: &[u8]) -> Option<(HeaderName, HeaderValue)> {
    Some((HeaderName::from_bytes(name.as_bytes()).ok()?, HeaderValue::from_bytes(value).ok()?))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn joins_the_path_onto_the_configured_base() {
        let url = target_url("https://hub.example.com", "/api/tasks", None).unwrap();
        assert_eq!(url.as_str(), "https://hub.example.com/api/tasks");
    }

    #[test]
    fn keeps_a_prefix_the_server_is_mounted_under() {
        let url = target_url("https://example.com/agenthub", "/api/tasks", None).unwrap();
        assert_eq!(url.as_str(), "https://example.com/agenthub/api/tasks");
    }

    #[test]
    fn carries_the_query_string_through() {
        let url = target_url("https://hub.example.com", "/api/tasks/1/events", Some("since=7")).unwrap();
        assert_eq!(url.as_str(), "https://hub.example.com/api/tasks/1/events?since=7");
    }

    #[test]
    fn strips_secure_domain_and_the_original_path() {
        let rewritten =
            rewrite_set_cookie("sid=abc; Path=/api; Domain=hub.example.com; Secure; HttpOnly; SameSite=None");
        assert_eq!(rewritten, "sid=abc; HttpOnly; SameSite=Lax; Path=/");
    }

    #[test]
    fn leaves_a_plain_cookie_alone_but_pins_it_to_the_root() {
        assert_eq!(rewrite_set_cookie("sid=abc"), "sid=abc; Path=/");
        assert_eq!(
            rewrite_set_cookie("sid=abc; Max-Age=600; HttpOnly"),
            "sid=abc; Max-Age=600; HttpOnly; Path=/"
        );
    }

    #[test]
    fn keeps_same_site_strict_as_it_is() {
        assert_eq!(
            rewrite_set_cookie("sid=abc; SameSite=Strict"),
            "sid=abc; SameSite=Strict; Path=/"
        );
        assert_eq!(rewrite_set_cookie("sid=abc; SameSite=Lax"), "sid=abc; SameSite=Lax; Path=/");
    }

    #[test]
    fn rewrites_redirects_that_land_back_on_the_server() {
        assert_eq!(
            rewrite_location("https://hub.example.com/", "https://hub.example.com", "http://127.0.0.1:1"),
            "http://127.0.0.1:1/"
        );
    }

    #[test]
    fn leaves_redirects_to_other_hosts_alone() {
        let raw = "https://github.com/login/oauth/authorize?x=1";
        assert_eq!(rewrite_location(raw, "https://hub.example.com", "http://127.0.0.1:1"), raw);
    }

    #[test]
    fn drops_the_headers_that_would_break_or_misidentify_the_request() {
        for name in ["host", "content-length", "cookie", "origin", "referer", "connection"] {
            assert!(drop_request_header(name), "{name} must not be forwarded");
        }
        for name in ["accept", "content-type", "authorization", "x-requested-with"] {
            assert!(!drop_request_header(name), "{name} must be forwarded");
        }
    }

    #[test]
    fn drops_the_headers_that_would_break_the_response() {
        for name in ["content-length", "transfer-encoding", "strict-transport-security"] {
            assert!(drop_response_header(name), "{name} must not come back");
        }
        for name in ["content-type", "cache-control", "content-disposition"] {
            assert!(!drop_response_header(name), "{name} must come back");
        }
    }
}
