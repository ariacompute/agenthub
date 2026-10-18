//! Connection configuration: where the server is, and what language the shell
//! speaks.
//!
//! It lives in the shell's own file (`app_config_dir/config.json`), not in the
//! server's database — the shell has to know where the server *is* before it
//! can ask it anything.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use tokio::sync::RwLock;

use crate::i18n;

/// The persisted shape. `server_url` empty means "not configured yet", which is
/// what sends the shell to the connection page.
#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct AppConfig {
    #[serde(default)]
    pub server_url: String,
    #[serde(default)]
    pub accept_invalid_certs: bool,
    /// Empty means "follow the system language".
    #[serde(default)]
    pub lang: String,
}

/// Shared between the tray (which reads it for menu labels) and the proxy
/// (which reads it for every forwarded request).
pub type SharedConfig = Arc<RwLock<AppConfig>>;

impl AppConfig {
    /// The language to render the shell in: the chosen one, else the system's.
    pub fn lang(&self) -> &'static str {
        if self.lang.is_empty() {
            return i18n::detect_system_lang();
        }
        i18n::match_lang(&self.lang)
    }

    /// The configured server, trailing slash removed, or `None` when the user
    /// has not pointed the shell anywhere yet.
    pub fn server_url(&self) -> Option<String> {
        normalize_server_url(&self.server_url).ok()
    }

    pub fn is_configured(&self) -> bool {
        self.server_url().is_some()
    }
}

/// Why an address is not acceptable.
#[derive(Debug, PartialEq, Eq)]
pub enum UrlError {
    Empty,
    Invalid,
}

impl std::fmt::Display for UrlError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            UrlError::Empty => f.write_str("empty"),
            UrlError::Invalid => f.write_str("invalid"),
        }
    }
}

/// Accepts only `http`/`https` URLs with a host, and returns them without a
/// trailing slash so joining a path onto them is unambiguous.
pub fn normalize_server_url(raw: &str) -> Result<String, UrlError> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Err(UrlError::Empty);
    }
    let parsed = url::Url::parse(trimmed).map_err(|_| UrlError::Invalid)?;
    match parsed.scheme() {
        "http" | "https" => {}
        _ => return Err(UrlError::Invalid),
    }
    if parsed.host_str().map(|h| h.is_empty()).unwrap_or(true) {
        return Err(UrlError::Invalid);
    }
    // Drop anything the console would never want to carry: a query string, a
    // fragment, or a trailing slash. Credentials in the userinfo are kept —
    // some deployments front the console with basic auth — but are never logged.
    let mut out = parsed.clone();
    out.set_query(None);
    out.set_fragment(None);
    Ok(out.as_str().trim_end_matches('/').to_string())
}

#[derive(Debug)]
pub enum ConfigError {
    Io(io::Error),
    Json(serde_json::Error),
}

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ConfigError::Io(err) => write!(f, "cannot read or write the config file: {err}"),
            ConfigError::Json(err) => write!(f, "the config file is not valid JSON: {err}"),
        }
    }
}

impl From<io::Error> for ConfigError {
    fn from(err: io::Error) -> Self {
        ConfigError::Io(err)
    }
}

impl From<serde_json::Error> for ConfigError {
    fn from(err: serde_json::Error) -> Self {
        ConfigError::Json(err)
    }
}

/// Reads and writes `config.json` in one directory.
#[derive(Debug, Clone)]
pub struct ConfigStore {
    path: PathBuf,
}

impl ConfigStore {
    pub fn new(dir: PathBuf) -> Self {
        ConfigStore { path: dir.join("config.json") }
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Missing or unreadable config is not an error: it means "first run".
    pub fn load(&self) -> AppConfig {
        match fs::read_to_string(&self.path) {
            Ok(text) => serde_json::from_str(&text).unwrap_or_default(),
            Err(_) => AppConfig::default(),
        }
    }

    pub fn save(&self, config: &AppConfig) -> Result<(), ConfigError> {
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent)?;
        }
        let text = serde_json::to_string_pretty(config)?;
        fs::write(&self.path, text)?;
        restrict_to_owner(&self.path);
        Ok(())
    }
}

/// The config can hold a URL with credentials, so keep it to the current user.
#[cfg(unix)]
fn restrict_to_owner(path: &Path) {
    use std::os::unix::fs::PermissionsExt;
    let _ = fs::set_permissions(path, fs::Permissions::from_mode(0o600));
}

#[cfg(not(unix))]
fn restrict_to_owner(_path: &Path) {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_http_and_https_addresses() {
        assert_eq!(normalize_server_url("http://localhost:3500").unwrap(), "http://localhost:3500");
        assert_eq!(normalize_server_url("https://hub.example.com").unwrap(), "https://hub.example.com");
    }

    #[test]
    fn drops_the_trailing_slash_and_the_query() {
        assert_eq!(
            normalize_server_url("https://hub.example.com/").unwrap(),
            "https://hub.example.com"
        );
        assert_eq!(
            normalize_server_url("https://hub.example.com/agenthub/?a=1#x").unwrap(),
            "https://hub.example.com/agenthub"
        );
    }

    #[test]
    fn keeps_a_path_prefix_so_joining_a_route_stays_correct() {
        assert_eq!(
            normalize_server_url("https://example.com/agenthub").unwrap(),
            "https://example.com/agenthub"
        );
    }

    #[test]
    fn refuses_anything_that_is_not_an_address() {
        assert_eq!(normalize_server_url(""), Err(UrlError::Empty));
        assert_eq!(normalize_server_url("   "), Err(UrlError::Empty));
        assert_eq!(normalize_server_url("hub.example.com"), Err(UrlError::Invalid));
        assert_eq!(normalize_server_url("ftp://hub.example.com"), Err(UrlError::Invalid));
        assert_eq!(normalize_server_url("file:///etc/passwd"), Err(UrlError::Invalid));
        assert_eq!(normalize_server_url("https://"), Err(UrlError::Invalid));
    }

    #[test]
    fn an_empty_url_means_unconfigured() {
        let config = AppConfig::default();
        assert!(!config.is_configured());
        assert_eq!(config.server_url(), None);
    }

    #[test]
    fn a_stored_url_that_stopped_being_valid_counts_as_unconfigured() {
        let config = AppConfig { server_url: "not a url".into(), ..Default::default() };
        assert!(!config.is_configured());
    }

    #[test]
    fn a_round_trip_through_the_store_keeps_every_field() {
        let dir = std::env::temp_dir().join(format!("agenthub-cfg-{}", std::process::id()));
        let store = ConfigStore::new(dir.clone());
        let config = AppConfig {
            server_url: "https://hub.example.com".into(),
            accept_invalid_certs: true,
            lang: "zh-TW".into(),
        };
        store.save(&config).unwrap();
        assert_eq!(store.load(), config);
        assert_eq!(store.load().lang(), "zh-TW");
        assert!(store.load().is_configured());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_missing_or_broken_file_is_treated_as_a_first_run() {
        let dir = std::env::temp_dir().join(format!("agenthub-cfg-missing-{}", std::process::id()));
        let store = ConfigStore::new(dir.clone());
        assert_eq!(store.load(), AppConfig::default());
        let _ = fs::create_dir_all(&dir);
        fs::write(store.path(), "{ not json").unwrap();
        assert_eq!(store.load(), AppConfig::default());
        let _ = fs::remove_dir_all(&dir);
    }
}
