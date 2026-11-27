import Foundation

// The JSON signaling protocol (implementation guide §4). Messages are
// built and read through JSONSerialization dictionaries: the envelope
// mixes per-type fields and must tolerate unknown types and fields for
// forward compatibility, which loose dictionaries handle naturally.
enum Wire {
    // -- validation / URL normalization --------------------------------------

    /// Room code rules: 4-64 chars from [A-Za-z0-9_-].
    static func validCode(_ code: String) -> Bool {
        guard code.utf8.count >= 4, code.utf8.count <= 64 else { return false }
        return code.utf8.allSatisfy { c in
            (c >= UInt8(ascii: "A") && c <= UInt8(ascii: "Z"))
                || (c >= UInt8(ascii: "a") && c <= UInt8(ascii: "z"))
                || (c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9"))
                || c == UInt8(ascii: "_") || c == UInt8(ascii: "-")
        }
    }

    /// Normalizes a server URL to the ws(s) endpoint exactly like the
    /// other clients: http(s) -> ws(s), "/ws" appended unless already
    /// present, query/fragment dropped.
    static func signalingURL(_ server: String) throws -> String {
        guard let schemeRange = server.range(of: "://") else {
            throw LobbyError(code: "invalid-server-url", message: "invalid server URL: \(server)")
        }
        let scheme = server[..<schemeRange.lowerBound].lowercased()
        var rest = String(server[schemeRange.upperBound...])
        let wsScheme: String
        switch scheme {
        case "http", "ws":
            wsScheme = "ws"
        case "https", "wss":
            wsScheme = "wss"
        default:
            throw LobbyError(code: "invalid-server-url", message: "unsupported scheme \(scheme) in server URL")
        }
        if let i = rest.firstIndex(of: "#") { rest = String(rest[..<i]) }
        if let i = rest.firstIndex(of: "?") { rest = String(rest[..<i]) }
        var authority = rest
        var path = ""
        if let i = rest.firstIndex(of: "/") {
            authority = String(rest[..<i])
            path = String(rest[i...])
        }
        guard !authority.isEmpty else {
            throw LobbyError(code: "invalid-server-url", message: "invalid server URL: \(server)")
        }
        while path.hasSuffix("/") { path.removeLast() }
        if !path.hasSuffix("/ws") { path += "/ws" }
        return wsScheme + "://" + authority + path
    }

    /// Derives the http(s) origin matching a normalized ws(s) URL;
    /// native clients send it so servers do not need --allow-no-origin.
    static func defaultOrigin(_ wsURL: String) -> String {
        guard let schemeRange = wsURL.range(of: "://") else { return "" }
        let scheme = wsURL[..<schemeRange.lowerBound]
        var authority = String(wsURL[schemeRange.upperBound...])
        if let i = authority.firstIndex(of: "/") { authority = String(authority[..<i]) }
        return (scheme == "ws" ? "http" : "https") + "://" + authority
    }

    // -- outgoing messages ---------------------------------------------------

    static func joinMessage(code: String, appId: String?, resumeToken: String, create: CreateOptions?) -> [String: Any] {
        var msg: [String: Any] = ["type": "join", "code": code]
        if let appId, !appId.isEmpty { msg["appId"] = appId }
        if !resumeToken.isEmpty { msg["resumeToken"] = resumeToken }
        if let create {
            var c: [String: Any] = ["maxPlayers": create.maxPlayers]
            if let v = create.waitUntilFull { c["waitUntilFull"] = v }
            if let v = create.allowLateJoin { c["allowLateJoin"] = v }
            if let v = create.allowReconnect { c["allowReconnect"] = v }
            if let v = create.allowReplacement { c["allowReplacement"] = v }
            if let v = create.reconnectPolicy { c["reconnectPolicy"] = v.rawValue }
            if let v = create.claimAfterMs { c["claimAfterMs"] = v }
            msg["create"] = c
        }
        return msg
    }

    static func claimSlotMessage(code: String, playerId: Int, appId: String?) -> [String: Any] {
        var msg: [String: Any] = ["type": "claim-slot", "code": code, "playerId": playerId]
        if let appId, !appId.isEmpty { msg["appId"] = appId }
        return msg
    }

    static func signalMessage(to: Int, payload: [String: Any]) -> [String: Any] {
        ["type": "signal", "to": to, "payload": payload]
    }

    static let leaveMessage: [String: Any] = ["type": "leave"]

    static func offerPayload(kind: String, sdp: String) -> [String: Any] {
        ["kind": kind, "sdp": sdp]
    }

    static func icePayload(candidate: String, sdpMid: String?) -> [String: Any] {
        var cand: [String: Any] = ["candidate": candidate]
        if let sdpMid { cand["sdpMid"] = sdpMid }
        return ["kind": "ice", "candidate": cand]
    }

    // -- incoming messages ---------------------------------------------------

    static func parse(_ text: String) -> [String: Any]? {
        guard let data = text.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) else { return nil }
        return obj as? [String: Any]
    }

    static func str(_ obj: [String: Any], _ key: String, _ def: String = "") -> String {
        obj[key] as? String ?? def
    }

    static func int(_ obj: [String: Any], _ key: String, _ def: Int = 0) -> Int {
        if let v = obj[key] as? Int { return v }
        if let v = obj[key] as? NSNumber { return v.intValue }
        return def
    }

    static func bool(_ obj: [String: Any], _ key: String, _ def: Bool = false) -> Bool {
        obj[key] as? Bool ?? def
    }

    /// Builds a full roster (one entry per slot, id == index) from the
    /// server's players array.
    static func roster(maxPlayers: Int, players: Any?) -> [PlayerInfo] {
        var out = (0..<max(maxPlayers, 0)).map { PlayerInfo(id: $0, occupied: false, connected: false) }
        for entry in players as? [Any] ?? [] {
            guard let m = entry as? [String: Any] else { continue }
            let id = int(m, "id", -1)
            guard id >= 0, id < out.count else { continue }
            out[id] = PlayerInfo(id: id, occupied: bool(m, "occupied"), connected: bool(m, "connected"))
        }
        return out
    }

    /// Parses the "iceServers" array of a joined message; "urls" may be
    /// a string or an array of strings.
    static func iceServers(_ value: Any?) -> [ICEServer] {
        var out: [ICEServer] = []
        for entry in value as? [Any] ?? [] {
            guard let m = entry as? [String: Any] else { continue }
            var urls: [String] = []
            if let s = m["urls"] as? String {
                urls.append(s)
            } else if let list = m["urls"] as? [Any] {
                urls.append(contentsOf: list.compactMap { $0 as? String })
            }
            guard !urls.isEmpty else { continue }
            out.append(ICEServer(urls: urls, username: m["username"] as? String, credential: m["credential"] as? String))
        }
        return out
    }

    // -- ICE URI building ----------------------------------------------------

    /// Flattens ICE servers to libdatachannel URI strings, embedding
    /// TURN credentials as `turn:username:credential@host:port?...`
    /// (percent-encoded).
    static func iceURIs(_ servers: [ICEServer]) -> [String] {
        var out: [String] = []
        for server in servers {
            for url in server.urls {
                guard let username = server.username, !username.isEmpty,
                      let credential = server.credential, !credential.isEmpty,
                      url.lowercased().hasPrefix("turn"),
                      let colon = url.firstIndex(of: ":")
                else {
                    out.append(url)
                    continue
                }
                let scheme = url[..<colon]
                let rest = url[url.index(after: colon)...]
                out.append("\(scheme):\(percentEncode(username)):\(percentEncode(credential))@\(rest)")
            }
        }
        return out
    }

    private static let unreserved = CharacterSet(
        charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")

    private static func percentEncode(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: unreserved) ?? s
    }
}
