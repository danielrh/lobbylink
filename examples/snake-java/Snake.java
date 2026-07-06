// Snake.java — the wildlifejava worm, now multiplayer over lobbylink.
//
// This keeps anderrh's original rendering *intact* so the author can keep
// developing the worm (fills, arms, whatever): the body is still the same chain
// of Segments trailing a lead point, drawn as the same hollow circles with the
// same size taper — segments[0] follows the mouse, `attach` pulls the rest along.
// See Segment.java (BSD 2-Clause, anderrh) and the original single-player loop it
// was lifted from.
//
// Everything added here is *multiplayer / gameplay*, layered on top without
// touching how a worm is drawn:
//   * join room "SNAKE" as the next player;
//   * broadcast your head position, length and apple score ~15×/second;
//   * apples: one every 4 seconds, at a spot that is a pure hash of a shared
//     clock, so every client agrees with zero apple messages. The lowest-id
//     player is the clock leader; everyone slaves to it. Eat one (touch it with
//     your head) to grow — by activating more of the existing segment chain.
// Your own worm renders exactly as the original (black); peers are tinted only
// so you can tell them apart.
//
// Build/run (after `cd ../../clients/java && ./gradlew lib`):
//   javac -cp "../../clients/java/build/lib/*" *.java
//   java  -cp "../../clients/java/build/lib/*:." Snake            (':'->';' on Windows)

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

import java.awt.Color;

import xyz.lobbylink.ConnectOptions;
import xyz.lobbylink.CreateOptions;
import xyz.lobbylink.Event;
import xyz.lobbylink.MessageKind;
import xyz.lobbylink.P2PGame;
import xyz.lobbylink.PlayerInfo;

public class Snake {
    // --- the worm, exactly as the original (do not change to keep it faithful):
    static final double WORLD_MIN = -300, WORLD_MAX = 300; // original setScale(-300, 300)
    static final int    SEG_COUNT = 3000;                  // original segment-array size
    /** Segment draw radius, straight from the original loop. */
    static double segSize(int i) {
        if (i == 1 || i == 2) return 4.0;                  // the original's thicker "neck"
        return (SEG_COUNT - i) / 1000.0;                   // tapers to nothing at the tail
    }

    // --- gameplay we add on top -----------------------------------------------
    static final int    START_LEN      = 3;     // a fresh worm is tiny — grows only by eating
    static final int    GROW_PER_APPLE = 12;    // segments activated per apple eaten
    static final double MARGIN         = 30.0;  // apples stay this far from the edges
    static final double APPLE_R        = 10.0;  // apple radius (drawn + hit test)
    static final double EAT_DIST       = 14.0;  // head-to-apple distance to eat
    static final long   APPLE_MS       = 4000;  // a new apple every 4 seconds
    static final long   SEND_MS        = 66;    // broadcast own state ~15 Hz
    static final int    FRAME_MS       = 16;    // ~60 fps loop
    static final double REMOTE_SMOOTH  = 0.35;  // per-frame ease of a peer's head toward its
                                                // latest packet, so it glides instead of jumping
    static final byte   MSG_STATE      = 1;     // best-effort packet type tag

    static final Color BG    = Color.WHITE;                // original background
    static final Color SELF  = Color.BLACK;                // your worm looks like the original
    static final Color APPLE = new Color(220, 40, 40);
    static final Color[] PALETTE = {                       // tints for the *other* players
        new Color( 40, 160,  90), new Color( 40, 110, 220), new Color(220, 110,  40),
        new Color(200,  50, 160), new Color(150, 130,  20), new Color( 30, 160, 160),
        new Color(140,  70, 210), new Color(210,  50,  50),
    };
    static Color colorOf(int id) { return PALETTE[Math.floorMod(id, PALETTE.length)]; }

    public static void main(String[] args) throws Exception {
        String server = args.length > 0 ? args[0] : "https://pqrstuvw.xyz/lobbylink";
        String code   = args.length > 1 ? args[1] : "SNAKE";

        // Register to room SNAKE as the next player — if the server is up.
        P2PGame game = null;
        try {
            game = P2PGame.connect(new ConnectOptions(server, code)
                    .create(new CreateOptions(16)));   // first one in creates it
            System.out.println("joined room " + game.code() + " as player " + game.selfId());
        } catch (Exception e) {
            System.out.println("server unavailable (" + e.getMessage() + ") — playing offline");
        }
        final int selfId = game != null ? game.selfId() : 0;
        final int seed   = fnv1a(code);

        // Latest state heard from each peer (written by the event thread, read by
        // the render loop). Their trailing bodies live in remoteWorms, touched
        // only by the render loop.
        final Map<Integer, Remote> remotes = new ConcurrentHashMap<>();
        final Map<Integer, Worm> remoteWorms = new HashMap<>();
        if (game != null) startEventPump(game, remotes);

        MiniDraw md = new MiniDraw(
                "lobbylink snake — room " + code + " — you are P" + selfId, 900, WORLD_MIN, WORLD_MAX);
        Worm me = new Worm(0, 0);

        int myScore = 0;
        int myEaten = -1;              // last apple index this worm consumed (wire i32)
        long consumedIndex = -1;       // apple index currently eaten (hidden) for us
        int seq = 0;
        long lastSend = 0;

        while (md.isOpen()) {
            long now = System.currentTimeMillis();

            // Original control: the head is the mouse; the body follows via attach.
            double hx = clamp(md.mouseX(), WORLD_MIN, WORLD_MAX);
            double hy = clamp(md.mouseY(), WORLD_MIN, WORLD_MAX);
            me.setHead(hx, hy);
            me.step();

            // Lowest-id present player is the clock leader; slave to its clock so
            // every client agrees which apple is out and where.
            int leader = leaderId(game, selfId, remotes.keySet());
            long sharedMs = sharedClock(now, leader, selfId, remotes);
            long appleIdx = Math.floorDiv(sharedMs, APPLE_MS);
            double[] apple = applePos(seed, appleIdx);
            boolean appleVisible = consumedIndex != appleIdx;

            // Eat it if your head reaches it (you decide locally — trust model).
            if (appleVisible && dist(hx, hy, apple[0], apple[1]) < EAT_DIST) {
                myScore++;
                me.grow(GROW_PER_APPLE);   // grow via the existing segment chain
                consumedIndex = appleIdx;
                myEaten = (int) appleIdx;
            }

            // Fold in peers: an apple vanishes for everyone once anyone eats it,
            // and each peer's worm eases toward its latest broadcast head, so it
            // glides smoothly between the ~15 Hz packets (and over dropped ones)
            // instead of teleporting every few frames.
            for (Map.Entry<Integer, Remote> e : remotes.entrySet()) {
                Remote r = e.getValue();
                if (r.eatenIndex == appleIdx) consumedIndex = appleIdx;
                Worm w = remoteWorms.computeIfAbsent(e.getKey(), k -> new Worm(r.hx, r.hy));
                w.setLen(r.length);
                w.easeHead(r.hx, r.hy, REMOTE_SMOOTH);
                w.step();
            }
            remoteWorms.keySet().retainAll(remotes.keySet());

            // Broadcast our own head + score + clock ~15×/second.
            if (game != null && now - lastSend >= SEND_MS) {
                lastSend = now;
                byte[] pkt = encode(seq++ & 0xFFFF, hx, hy, myScore, me.len, sharedMs, myEaten);
                try { game.broadcastBestEffort(pkt); } catch (Exception ignore) { }
            }

            // Draw: field, apple, peer worms, then yours, then the scoreboard.
            md.clear(BG);
            if (appleVisible) {
                md.setPenColor(APPLE);
                md.filledCircle(apple[0], apple[1], APPLE_R);
            }
            for (Map.Entry<Integer, Worm> e : remoteWorms.entrySet()) {
                e.getValue().draw(md, colorOf(e.getKey()));
            }
            me.draw(md, SELF);
            drawScoreboard(md, selfId, myScore, remotes);
            md.show();
            md.pause(FRAME_MS);
        }

        if (game != null) game.close();
        System.exit(0);
    }

    // --- shared clock ---------------------------------------------------------

    /** Lowest present player id: the clock leader (self, occupied roster slots,
     *  and everyone we've recently heard from). */
    static int leaderId(P2PGame game, int selfId, Iterable<Integer> heardFrom) {
        int leader = selfId;
        if (game != null) {
            for (PlayerInfo p : game.players()) {
                if (p.occupied()) leader = Math.min(leader, p.id());
            }
        }
        for (int id : heardFrom) leader = Math.min(leader, id);
        return leader;
    }

    /** Our estimate of the leader's clock now: our wall clock if we are the
     *  leader, else the leader's last broadcast clock plus the wall time since. */
    static long sharedClock(long now, int leader, int selfId, Map<Integer, Remote> remotes) {
        if (leader == selfId) return now;
        Remote r = remotes.get(leader);
        if (r == null) return now;
        return r.clockMs + (now - r.recvWall);
    }

    // --- deterministic apple field (a pure function of the shared clock) -------
    // Same hashing/PRNG house style as examples/PROTOCOL.md.

    static int fnv1a(String s) {
        int h = 0x811c9dc5;
        for (byte b : s.getBytes(StandardCharsets.UTF_8)) h = (h ^ (b & 0xff)) * 0x01000193;
        return h;
    }

    /** mulberry32: advances state[0], returns a double in [0, 1). */
    static double mulberry(int[] state) {
        state[0] += 0x6D2B79F5;
        int t = state[0];
        t = (t ^ (t >>> 15)) * (t | 1);
        t ^= t + ((t ^ (t >>> 7)) * (t | 61));
        t ^= t >>> 14;
        return (t & 0xffffffffL) / 4294967296.0;
    }

    /** Where apple #idx sits — a hash of the (leader-clock-derived) index. */
    static double[] applePos(int seed, long idx) {
        int[] st = { seed ^ ((int) idx * 0x9E3779B9) };
        double rx = mulberry(st), ry = mulberry(st);
        double span = (WORLD_MAX - WORLD_MIN) - 2 * MARGIN;
        return new double[] { WORLD_MIN + MARGIN + rx * span, WORLD_MIN + MARGIN + ry * span };
    }

    // --- wire format: one best-effort STATE packet, big-endian ----------------
    // u8 type=1 | u16 seq | f32 headX | f32 headY | u32 score | u32 length |
    // f64 clockMs | i32 eatenIndex        (31 bytes; one full snapshot per send)

    static byte[] encode(int seq, double hx, double hy, int score, int length,
                         long clockMs, int eaten) {
        ByteBuffer b = ByteBuffer.allocate(31);   // big-endian by default
        b.put(MSG_STATE);
        b.putShort((short) seq);
        b.putFloat((float) hx);
        b.putFloat((float) hy);
        b.putInt(score);
        b.putInt(length);
        b.putDouble((double) clockMs);
        b.putInt(eaten);
        return b.array();
    }

    static void startEventPump(P2PGame game, Map<Integer, Remote> remotes) {
        Thread t = new Thread(() -> {
            try {
                Event ev;
                while ((ev = game.nextEvent()) != null) {
                    if (ev instanceof Event.Message m && m.kind() == MessageKind.BEST_EFFORT) {
                        ingest(remotes, m.from(), m.data());
                    } else if (ev instanceof Event.PlayerLeft pl) {
                        remotes.remove(pl.playerId());
                    }
                }
            } catch (InterruptedException ignored) {
            }
        }, "snake-events");
        t.setDaemon(true);
        t.start();
    }

    static void ingest(Map<Integer, Remote> remotes, int from, byte[] data) {
        if (data.length < 31) return;
        ByteBuffer b = ByteBuffer.wrap(data);
        if (b.get() != MSG_STATE) return;
        int seq = b.getShort() & 0xFFFF;
        Remote prev = remotes.get(from);
        // Drop packets that arrive out of order on the unordered channel.
        if (prev != null && ((seq - prev.seq) & 0xFFFF) >= 0x8000) return;
        double hx = b.getFloat();
        double hy = b.getFloat();
        int score = b.getInt();
        int length = b.getInt();
        long clockMs = (long) b.getDouble();
        int eaten = b.getInt();
        remotes.put(from, new Remote(seq, hx, hy, score, length, clockMs, eaten,
                System.currentTimeMillis()));
    }

    static void drawScoreboard(MiniDraw md, int selfId, int myScore, Map<Integer, Remote> remotes) {
        List<int[]> rows = new ArrayList<>();     // {id, score}
        rows.add(new int[] { selfId, myScore });
        for (Map.Entry<Integer, Remote> e : remotes.entrySet()) {
            rows.add(new int[] { e.getKey(), e.getValue().score });
        }
        rows.sort(Comparator.comparingInt(a -> a[0]));
        double y = WORLD_MAX - 18;
        for (int[] row : rows) {
            md.setPenColor(row[0] == selfId ? SELF : colorOf(row[0]));
            String label = "P" + row[0] + "  " + row[1] + (row[0] == selfId ? "  (you)" : "");
            md.textLeft(WORLD_MIN + 14, y, label);
            y -= 18;
        }
    }

    static double clamp(double v, double lo, double hi) { return v < lo ? lo : v > hi ? hi : v; }
    static double dist(double ax, double ay, double bx, double by) {
        return Math.hypot(ax - bx, ay - by);
    }

    /** The latest snapshot we've received from one peer. */
    static final class Remote {
        final int seq;
        final double hx, hy;
        final int score, length;
        final long clockMs, recvWall;
        final int eatenIndex;
        Remote(int seq, double hx, double hy, int score, int length,
               long clockMs, int eatenIndex, long recvWall) {
            this.seq = seq; this.hx = hx; this.hy = hy; this.score = score;
            this.length = length; this.clockMs = clockMs; this.eatenIndex = eatenIndex;
            this.recvWall = recvWall;
        }
    }

    /** A worm: the original chain of Segments trailing the head. `len` is how
     *  many are active (it grows on apples); the head is placed each frame — from
     *  the mouse for you, from a broadcast for a peer — and the body follows via
     *  Segment.attach, exactly as in the single-player original. Rendering is the
     *  original's hollow-circle loop; only the pen color is a parameter now. */
    static final class Worm {
        final Segment[] segs = new Segment[SEG_COUNT];
        int len = START_LEN;

        Worm(double x, double y) {
            for (int i = 0; i < segs.length; i++) segs[i] = new Segment(x, y, segSize(i));
        }

        void setHead(double x, double y) { segs[0].x = x; segs[0].y = y; }

        /** Ease the head a fraction {@code a} of the way toward (x, y) — used to
         *  interpolate a peer's head between its lower-rate broadcasts. */
        void easeHead(double x, double y, double a) {
            segs[0].x += (x - segs[0].x) * a;
            segs[0].y += (y - segs[0].y) * a;
        }

        void step() {
            for (int i = 1; i < len; i++) segs[i].attach(segs[i - 1]);
        }

        void grow(int n) {
            int old = len;
            len = Math.min(len + n, SEG_COUNT);
            for (int i = old; i < len; i++) {   // new links sprout from the tail
                segs[i].x = segs[old - 1].x;
                segs[i].y = segs[old - 1].y;
            }
        }

        /** Set absolute length (for a peer whose length we were told). */
        void setLen(int n) {
            n = Math.max(1, Math.min(n, SEG_COUNT));
            if (n > len) grow(n - len); else len = n;
        }

        void draw(MiniDraw md, Color color) {
            md.setPenColor(color);
            for (int i = 0; i < len; i++) md.circle(segs[i].x, segs[i].y, segs[i].size);
        }
    }
}
