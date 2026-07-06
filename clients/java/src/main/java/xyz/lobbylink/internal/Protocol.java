package xyz.lobbylink.internal;

import java.util.LinkedHashMap;
import java.util.Map;

import xyz.lobbylink.CreateOptions;

/**
 * Signaling wire encoders and the fatal-code classification. Field and tag
 * names must match the server / other clients exactly. Server -> client
 * messages are parsed with {@link Json} directly in the Actor.
 */
public final class Protocol {
    private Protocol() {}

    // ---- client -> server ----

    public static String join(String code, String appId, String resumeToken, CreateOptions create) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("type", "join");
        m.put("code", code);
        if (appId != null) m.put("appId", appId);
        if (resumeToken != null) m.put("resumeToken", resumeToken);
        if (create != null) m.put("create", createWire(create));
        return Json.write(m);
    }

    public static String claimSlot(String code, int playerId, String appId) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("type", "claim-slot");
        m.put("code", code);
        m.put("playerId", playerId);
        if (appId != null) m.put("appId", appId);
        return Json.write(m);
    }

    public static String signal(int to, Map<String, Object> payload) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("type", "signal");
        m.put("to", to);
        m.put("payload", payload);
        return Json.write(m);
    }

    public static String leave() {
        return "{\"type\":\"leave\"}";
    }

    private static Map<String, Object> createWire(CreateOptions c) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("maxPlayers", c.maxPlayers);
        m.put("waitUntilFull", c.waitUntilFull);
        m.put("allowLateJoin", c.allowLateJoin);
        m.put("allowReconnect", c.allowReconnect);
        m.put("allowReplacement", c.allowReplacement);
        if (c.reconnectPolicy != null) m.put("reconnectPolicy", c.reconnectPolicy.wireName());
        if (c.claimAfterMs != null) m.put("claimAfterMs", c.claimAfterMs);
        return m;
    }

    // ---- signal payloads ----

    public static Map<String, Object> offer(String sdp) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("kind", "offer");
        m.put("sdp", sdp);
        return m;
    }

    public static Map<String, Object> answer(String sdp) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("kind", "answer");
        m.put("sdp", sdp);
        return m;
    }

    public static Map<String, Object> ice(Map<String, Object> candidate) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("kind", "ice");
        m.put("candidate", candidate);
        return m;
    }

    // ---- error-code classification ----

    /** Codes after which the WebSocket will not come back. */
    public static boolean isFatalCode(String code) {
        return code.equals("replaced") || code.equals("session-superseded")
                || code.equals("room-expired") || code.equals("slow-consumer");
    }

    /** Fatal codes that also mean our peers are gone / we left the room. */
    public static boolean isGameOverCode(String code) {
        return code.equals("replaced") || code.equals("session-superseded") || code.equals("room-expired");
    }
}
