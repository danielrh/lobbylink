package xyz.lobbylink.internal;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;

import xyz.lobbylink.CreateOptions;

class JsonTest {
    @Test
    void roundtripPrimitives() {
        assertEquals("\"hi\\n\\\"there\\\"\"", Json.write("hi\n\"there\""));
        assertEquals("42", Json.write(42L));
        assertEquals("true", Json.write(Boolean.TRUE));
        assertEquals("null", Json.write(null));
        assertEquals("[1,2,3]", Json.write(List.of(1L, 2L, 3L)));
    }

    @Test
    void parseObject() {
        Map<String, Object> m = Json.asObject(Json.parse(
                "{\"type\":\"joined\",\"selfId\":0,\"started\":true,\"nested\":{\"a\":1}}"));
        assertEquals("joined", Json.str(m, "type"));
        assertEquals(0, Json.intVal(m, "selfId", -1));
        assertTrue(Json.bool(m, "started", false));
        assertEquals(1, Json.intVal(Json.asObject(m.get("nested")), "a", -1));
    }

    @Test
    void parseArraysAndNumbers() {
        List<Object> a = Json.asArray(Json.parse("[0, 1.5, -3, \"x\", null, false]"));
        assertEquals(0L, a.get(0));
        assertEquals(1.5, (Double) a.get(1), 1e-9);
        assertEquals(-3L, a.get(2));
        assertEquals("x", a.get(3));
        assertNull(a.get(4));
        assertEquals(Boolean.FALSE, a.get(5));
    }

    @Test
    void clientJoinEncodesLikeTheOtherClients() {
        String s = Protocol.join("ROOM", null, "tok", new CreateOptions(4));
        Map<String, Object> v = Json.asObject(Json.parse(s));
        assertEquals("join", Json.str(v, "type"));
        assertEquals("ROOM", Json.str(v, "code"));
        assertEquals("tok", Json.str(v, "resumeToken"));
        assertNull(v.get("appId"));
        Map<String, Object> create = Json.asObject(v.get("create"));
        assertEquals(4, Json.intVal(create, "maxPlayers", -1));
        assertEquals(Boolean.FALSE, create.get("waitUntilFull"));
        assertEquals(Boolean.TRUE, create.get("allowLateJoin"));
        assertNull(create.get("reconnectPolicy"));
        assertNull(create.get("claimAfterMs"));
    }

    @Test
    void leaveEncoding() {
        assertEquals("{\"type\":\"leave\"}", Protocol.leave());
    }

    @Test
    void iceCandidatePassthrough() {
        // An ICE signal keeps the candidate JSON verbatim as a nested map.
        Map<String, Object> cand = Json.asObject(Json.parse(
                "{\"candidate\":\"candidate:1 1 udp ...\",\"sdpMid\":\"0\",\"sdpMLineIndex\":0}"));
        String signal = Protocol.signal(1, Protocol.ice(cand));
        Map<String, Object> v = Json.asObject(Json.parse(signal));
        assertEquals("signal", Json.str(v, "type"));
        Map<String, Object> payload = Json.asObject(v.get("payload"));
        assertEquals("ice", Json.str(payload, "kind"));
        assertEquals("0", Json.str(Json.asObject(payload.get("candidate")), "sdpMid"));
    }
}
