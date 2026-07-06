//! Signaling URL normalization: http(s) becomes ws(s) and "/ws" is
//! appended unless already present, so subpath deployments like
//! https://host/lobbylink work unchanged. Hand-rolled (no `url`
//! crate) and shared by both backends so behavior is identical.

use super::error::LobbyError;

fn invalid(server: &str) -> LobbyError {
    LobbyError::new("invalid-server-url", format!("invalid server URL: {server}"))
}

/// `https://host[:port][/path]` -> `wss://host[:port][/path]/ws`
pub fn signaling_url(server: &str) -> Result<String, LobbyError> {
    let (scheme, rest) = server.split_once("://").ok_or_else(|| invalid(server))?;
    let ws_scheme = match scheme.to_ascii_lowercase().as_str() {
        "http" | "ws" => "ws",
        "https" | "wss" => "wss",
        other => {
            return Err(LobbyError::new(
                "invalid-server-url",
                format!("unsupported scheme {other}: in server URL"),
            ))
        }
    };
    // Drop fragment, then query.
    let rest = rest.split('#').next().unwrap_or(rest);
    let rest = rest.split('?').next().unwrap_or(rest);
    let (authority, path) = match rest.find('/') {
        Some(i) => (&rest[..i], &rest[i..]),
        None => (rest, ""),
    };
    if authority.is_empty() {
        return Err(invalid(server));
    }
    let path = path.trim_end_matches('/');
    if path.ends_with("/ws") {
        Ok(format!("{ws_scheme}://{authority}{path}"))
    } else {
        Ok(format!("{ws_scheme}://{authority}{path}/ws"))
    }
}

/// The http(s) origin matching a normalized ws(s) signaling URL —
/// the native backend's default Origin header. (In a browser the
/// page's own origin is sent automatically; wasm cannot set headers.)
#[cfg_attr(target_arch = "wasm32", allow(dead_code))]
pub fn default_origin(ws_url: &str) -> String {
    let (scheme, rest) = ws_url.split_once("://").unwrap_or(("wss", ws_url));
    let authority = rest.split('/').next().unwrap_or(rest);
    let http_scheme = if scheme == "ws" { "http" } else { "https" };
    format!("{http_scheme}://{authority}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_like_ts() {
        assert_eq!(
            signaling_url("https://pqrstuvw.xyz/lobbylink").unwrap(),
            "wss://pqrstuvw.xyz/lobbylink/ws"
        );
        assert_eq!(signaling_url("https://host").unwrap(), "wss://host/ws");
        assert_eq!(signaling_url("https://host/").unwrap(), "wss://host/ws");
        assert_eq!(signaling_url("https://host///").unwrap(), "wss://host/ws");
        assert_eq!(signaling_url("http://host:8789").unwrap(), "ws://host:8789/ws");
        assert_eq!(
            signaling_url("wss://host:4443/ws").unwrap(),
            "wss://host:4443/ws"
        );
        assert_eq!(signaling_url("ws://h/lobby/ws").unwrap(), "ws://h/lobby/ws");
        assert_eq!(
            signaling_url("https://host/path?query=1#frag").unwrap(),
            "wss://host/path/ws"
        );
    }

    #[test]
    fn rejects_bad_urls() {
        assert_eq!(signaling_url("host:1234").unwrap_err().code, "invalid-server-url");
        assert_eq!(signaling_url("ftp://host").unwrap_err().code, "invalid-server-url");
        assert_eq!(signaling_url("https:///path").unwrap_err().code, "invalid-server-url");
    }

    #[test]
    fn origins() {
        assert_eq!(
            default_origin("wss://pqrstuvw.xyz/lobbylink/ws"),
            "https://pqrstuvw.xyz"
        );
        assert_eq!(default_origin("ws://127.0.0.1:8789/ws"), "http://127.0.0.1:8789");
    }
}
