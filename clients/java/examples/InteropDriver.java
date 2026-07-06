// Interop driver, mirroring clients/rust/interop/driver: joins an existing room
// (retrying while the responder starts up), asserts reliable echo roundtrips for
// several sizes plus a best-effort echo, waits for the responder to leave, then
// prints INTEROP-PASS. Exits nonzero on failure.
//
//   javac -cp lobbylink-client-all.jar InteropDriver.java
//   java  -cp lobbylink-client-all.jar:. InteropDriver <server> <code> [--no-origin]

import java.util.Arrays;

import xyz.lobbylink.ConnectOptions;
import xyz.lobbylink.Event;
import xyz.lobbylink.MessageKind;
import xyz.lobbylink.P2PGame;
import xyz.lobbylink.PlayerLeftReason;

public class InteropDriver {
    static byte[] pattern(int len, int seed) {
        byte[] b = new byte[len];
        for (int i = 0; i < len; i++) {
            b[i] = (byte) ((i * 31 + seed) & 0xFF);
        }
        return b;
    }

    static byte[] recvReliable(P2PGame game, int from, String what) throws Exception {
        long deadline = System.currentTimeMillis() + 25_000;
        while (System.currentTimeMillis() < deadline) {
            Event ev = game.nextEvent(deadline - System.currentTimeMillis());
            if (ev == null) throw new RuntimeException("timeout/closed waiting for " + what);
            if (ev instanceof Event.Message m && m.kind() == MessageKind.RELIABLE && m.from() == from) {
                return m.data();
            }
        }
        throw new RuntimeException("timeout waiting for " + what);
    }

    public static void main(String[] args) throws Exception {
        String server = args[0];
        String code = args[1];
        boolean noOrigin = args.length > 2 && args[2].equals("--no-origin");

        P2PGame game = null;
        for (int i = 0; i < 40 && game == null; i++) {
            try {
                ConnectOptions opts = new ConnectOptions(server, code);
                if (noOrigin) opts.origin("");
                game = P2PGame.connect(opts);
            } catch (Exception e) {
                System.err.println("waiting for room: " + e.getMessage());
                Thread.sleep(500);
            }
        }
        if (game == null) throw new RuntimeException("could not join room");

        int peer = game.selfId() == 0 ? 1 : 0;
        System.out.println("joined as player " + game.selfId() + ", driving against " + peer);

        // Wait for the peer connection.
        long deadline = System.currentTimeMillis() + 30_000;
        boolean connected = false;
        while (System.currentTimeMillis() < deadline && !connected) {
            Event ev = game.nextEvent(deadline - System.currentTimeMillis());
            if (ev instanceof Event.PeerState ps && ps.playerId() == peer) {
                if (ps.state().equals("connected")) connected = true;
                else if (ps.state().equals("failed")) throw new RuntimeException("peer connection failed");
            }
        }
        if (!connected) throw new RuntimeException("timeout waiting for peer connection");
        System.out.println("peer connected");

        Object[][] cases = {
                {"small", "interop-hello".getBytes()},
                {"0-byte", new byte[0]},
                {"300KB", pattern(300 * 1024, 3)},
                {"8MiB", pattern(8 * 1024 * 1024, 9)},
        };
        for (Object[] c : cases) {
            String name = (String) c[0];
            byte[] data = (byte[]) c[1];
            game.sendReliable(peer, data);
            byte[] echo = recvReliable(game, peer, name);
            if (!Arrays.equals(echo, data)) throw new RuntimeException("echo mismatch for " + name);
            System.out.println("echo ok: " + name + " (" + data.length + " bytes)");
        }

        // Best-effort echo (lossy: retry).
        boolean bestEffort = false;
        for (int i = 0; i < 50 && !bestEffort; i++) {
            game.sendBestEffort(peer, "dgram".getBytes());
            long until = System.currentTimeMillis() + 300;
            while (System.currentTimeMillis() < until) {
                Event ev = game.nextEvent(until - System.currentTimeMillis());
                if (ev instanceof Event.Message m && m.kind() == MessageKind.BEST_EFFORT
                        && new String(m.data()).equals("dgram")) {
                    bestEffort = true;
                    break;
                }
            }
        }
        if (!bestEffort) throw new RuntimeException("best-effort echo never arrived");
        System.out.println("echo ok: best-effort");

        game.sendReliable(peer, "quit".getBytes());
        long leaveDeadline = System.currentTimeMillis() + 15_000;
        boolean left = false;
        while (System.currentTimeMillis() < leaveDeadline && !left) {
            Event ev = game.nextEvent(leaveDeadline - System.currentTimeMillis());
            if (ev instanceof Event.PlayerLeft pl && pl.reason() == PlayerLeftReason.EXPLICIT_LEAVE) {
                left = true;
            }
        }
        if (!left) throw new RuntimeException("timeout waiting for responder leave");
        System.out.println("responder left explicitly");

        game.close();
        System.out.println("INTEROP-PASS");
    }
}
