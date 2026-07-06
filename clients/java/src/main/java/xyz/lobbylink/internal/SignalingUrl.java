package xyz.lobbylink.internal;

import xyz.lobbylink.LobbyException;

/**
 * Signaling URL normalization: http(s) becomes ws(s) and "/ws" is appended
 * unless already present, so subpath deployments like https://host/lobbylink
 * work unchanged. Hand-rolled to match the other clients exactly.
 */
public final class SignalingUrl {
    private SignalingUrl() {}

    /** {@code https://host[:port][/path]} -> {@code wss://host[:port][/path]/ws} */
    public static String normalize(String server) throws LobbyException {
        int idx = server.indexOf("://");
        if (idx < 0) throw invalid(server);
        String scheme = server.substring(0, idx).toLowerCase();
        String rest = server.substring(idx + 3);
        String wsScheme;
        switch (scheme) {
            case "http":
            case "ws":
                wsScheme = "ws";
                break;
            case "https":
            case "wss":
                wsScheme = "wss";
                break;
            default:
                throw new LobbyException("invalid-server-url", "unsupported scheme " + scheme + ": in server URL");
        }
        int hash = rest.indexOf('#');
        if (hash >= 0) rest = rest.substring(0, hash);
        int q = rest.indexOf('?');
        if (q >= 0) rest = rest.substring(0, q);
        String authority;
        String path;
        int slash = rest.indexOf('/');
        if (slash >= 0) {
            authority = rest.substring(0, slash);
            path = rest.substring(slash);
        } else {
            authority = rest;
            path = "";
        }
        if (authority.isEmpty()) throw invalid(server);
        while (path.endsWith("/")) path = path.substring(0, path.length() - 1);
        if (path.endsWith("/ws")) {
            return wsScheme + "://" + authority + path;
        }
        return wsScheme + "://" + authority + path + "/ws";
    }

    /**
     * The http(s) origin matching a normalized ws(s) signaling URL — the default
     * Origin header the server allowlists on hosts that also serve their web client.
     */
    public static String defaultOrigin(String wsUrl) {
        int idx = wsUrl.indexOf("://");
        String scheme;
        String rest;
        if (idx < 0) {
            scheme = "wss";
            rest = wsUrl;
        } else {
            scheme = wsUrl.substring(0, idx);
            rest = wsUrl.substring(idx + 3);
        }
        int slash = rest.indexOf('/');
        String authority = slash >= 0 ? rest.substring(0, slash) : rest;
        String httpScheme = scheme.equals("ws") ? "http" : "https";
        return httpScheme + "://" + authority;
    }

    /** Room code: 4-64 chars of [A-Za-z0-9_-]. */
    public static void validateCode(String code) throws LobbyException {
        boolean ok = code.length() >= 4 && code.length() <= 64;
        if (ok) {
            for (int i = 0; i < code.length(); i++) {
                char c = code.charAt(i);
                boolean allowed = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
                        || (c >= '0' && c <= '9') || c == '_' || c == '-';
                if (!allowed) {
                    ok = false;
                    break;
                }
            }
        }
        if (!ok) {
            throw new LobbyException("invalid-code", "room code must be 4-64 chars of [A-Za-z0-9_-]");
        }
    }

    private static LobbyException invalid(String s) {
        return new LobbyException("invalid-server-url", "invalid server URL: " + s);
    }
}
