package xyz.lobbylink.internal;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import xyz.lobbylink.LobbyException;
import xyz.lobbylink.PlayerInfo;

/** Roster construction and send-target validation, shared with the other clients. */
public final class Roster {
    private Roster() {}

    /** Build the full roster (one entry per slot, id == index) from a server snapshot. */
    public static List<PlayerInfo> fromSnapshot(int maxPlayers, List<Object> players) {
        PlayerInfo[] roster = new PlayerInfo[Math.max(0, maxPlayers)];
        for (int i = 0; i < roster.length; i++) {
            roster[i] = new PlayerInfo(i, false, false);
        }
        if (players != null) {
            for (Object o : players) {
                Map<String, Object> p = Json.asObject(o);
                if (p == null) continue;
                int id = Json.intVal(p, "id", -1);
                if (id >= 0 && id < roster.length) {
                    roster[id] = new PlayerInfo(id, Json.bool(p, "occupied", false), Json.bool(p, "connected", false));
                }
            }
        }
        List<PlayerInfo> out = new ArrayList<>(roster.length);
        for (PlayerInfo p : roster) out.add(p);
        return out;
    }

    /** Validate a send target (shared by all clients). */
    public static void checkTarget(int to, int selfId, int maxPlayers) throws LobbyException {
        if (to < 0 || to >= maxPlayers) {
            throw new LobbyException("invalid-target",
                    "player id " + to + " out of range 0.." + Math.max(0, maxPlayers - 1));
        }
        if (to == selfId) {
            throw new LobbyException("invalid-target", "cannot send to yourself");
        }
    }
}
