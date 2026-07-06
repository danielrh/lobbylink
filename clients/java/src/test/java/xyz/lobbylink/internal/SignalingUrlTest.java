package xyz.lobbylink.internal;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import org.junit.jupiter.api.Test;

import xyz.lobbylink.LobbyException;

class SignalingUrlTest {
    @Test
    void normalizesLikeTheOtherClients() throws Exception {
        assertEquals("wss://pqrstuvw.xyz/lobbylink/ws", SignalingUrl.normalize("https://pqrstuvw.xyz/lobbylink"));
        assertEquals("wss://host/ws", SignalingUrl.normalize("https://host"));
        assertEquals("wss://host/ws", SignalingUrl.normalize("https://host/"));
        assertEquals("wss://host/ws", SignalingUrl.normalize("https://host///"));
        assertEquals("ws://host:8789/ws", SignalingUrl.normalize("http://host:8789"));
        assertEquals("wss://host:4443/ws", SignalingUrl.normalize("wss://host:4443/ws"));
        assertEquals("ws://h/lobby/ws", SignalingUrl.normalize("ws://h/lobby/ws"));
        assertEquals("wss://host/path/ws", SignalingUrl.normalize("https://host/path?query=1#frag"));
    }

    @Test
    void rejectsBadUrls() {
        assertEquals("invalid-server-url", codeOf(() -> SignalingUrl.normalize("host:1234")));
        assertEquals("invalid-server-url", codeOf(() -> SignalingUrl.normalize("ftp://host")));
        assertEquals("invalid-server-url", codeOf(() -> SignalingUrl.normalize("https:///path")));
    }

    @Test
    void origins() {
        assertEquals("https://pqrstuvw.xyz", SignalingUrl.defaultOrigin("wss://pqrstuvw.xyz/lobbylink/ws"));
        assertEquals("http://127.0.0.1:8789", SignalingUrl.defaultOrigin("ws://127.0.0.1:8789/ws"));
    }

    @Test
    void codeValidation() {
        assertDoesNotThrow(() -> SignalingUrl.validateCode("ROOM"));
        assertDoesNotThrow(() -> SignalingUrl.validateCode("a_b-9"));
        assertDoesNotThrow(() -> SignalingUrl.validateCode("x".repeat(64)));
        assertThrows(LobbyException.class, () -> SignalingUrl.validateCode("abc"));
        assertThrows(LobbyException.class, () -> SignalingUrl.validateCode("x".repeat(65)));
        assertThrows(LobbyException.class, () -> SignalingUrl.validateCode("bad room"));
        assertThrows(LobbyException.class, () -> SignalingUrl.validateCode("bad!"));
        assertThrows(LobbyException.class, () -> SignalingUrl.validateCode(""));
    }

    private interface ThrowingRun {
        void run() throws Exception;
    }

    private static String codeOf(ThrowingRun r) {
        try {
            r.run();
        } catch (LobbyException e) {
            return e.getCode();
        } catch (Exception e) {
            return "unexpected:" + e;
        }
        return "no-exception";
    }
}
