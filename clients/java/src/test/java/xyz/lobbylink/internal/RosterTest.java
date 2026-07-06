package xyz.lobbylink.internal;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.List;

import org.junit.jupiter.api.Test;

import xyz.lobbylink.LobbyException;
import xyz.lobbylink.PlayerInfo;

class RosterTest {
    @Test
    void snapshotFillsAllSlots() {
        List<Object> wire = Json.asArray(Json.parse("[{\"id\":1,\"occupied\":true,\"connected\":false}]"));
        List<PlayerInfo> roster = Roster.fromSnapshot(3, wire);
        assertEquals(3, roster.size());
        assertFalse(roster.get(0).occupied());
        assertTrue(roster.get(1).occupied());
        assertFalse(roster.get(1).connected());
        assertEquals(2, roster.get(2).id());
    }

    @Test
    void targetChecks() throws Exception {
        Roster.checkTarget(1, 0, 4); // ok
        assertEquals("invalid-target", codeOf(() -> Roster.checkTarget(4, 0, 4)));
        assertEquals("invalid-target", codeOf(() -> Roster.checkTarget(0, 0, 4)));
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
            return "unexpected";
        }
        return "no-exception";
    }

    @Test
    void checkTargetThrowsTyped() {
        assertThrows(LobbyException.class, () -> Roster.checkTarget(9, 0, 4));
    }
}
