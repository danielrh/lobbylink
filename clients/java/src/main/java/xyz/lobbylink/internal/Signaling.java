package xyz.lobbylink.internal;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.WebSocket;
import java.nio.ByteBuffer;
import java.time.Duration;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

import xyz.lobbylink.LobbyException;

/**
 * The signaling WebSocket, on top of the JDK's built-in {@code java.net.http}
 * client (no external dependency). Incoming full-text messages and close/error
 * notifications are handed to a {@link Handler}; outgoing sends are serialized
 * (the JDK forbids overlapping sends on one WebSocket).
 *
 * <p>Unless disabled, a WebSocket ping goes out every
 * {@link #KEEPALIVE_SECS} seconds. Once every peer link is up a game sends
 * nothing over signaling, and reverse proxies and NATs drop a silent
 * connection (Apache's mod_proxy_wstunnel after its Timeout), after which
 * nobody else can join. A ping frame is answered by the server's WebSocket
 * layer and never seen as a lobby message.
 */
public final class Signaling {
    public interface Handler {
        void onText(String message);
        void onClosed();
    }

    /** Interval of the signaling keepalive ping. */
    public static final long KEEPALIVE_SECS = 25;

    private final HttpClient client;
    private final WebSocket ws;
    private final Object sendLock = new Object();
    private CompletableFuture<WebSocket> sendChain;
    private volatile Handler handler;
    private ScheduledExecutorService keepalive; // guarded by sendLock

    private Signaling(HttpClient client, WebSocket ws) {
        this.client = client;
        this.ws = ws;
        this.sendChain = CompletableFuture.completedFuture(ws);
    }

    public void setHandler(Handler h) {
        this.handler = h;
    }

    public static Signaling connect(String wsUrl, String origin, long timeoutMs) throws LobbyException {
        return connect(wsUrl, origin, timeoutMs, true);
    }

    public static Signaling connect(String wsUrl, String origin, long timeoutMs, boolean keepalive)
            throws LobbyException {
        HttpClient client = HttpClient.newHttpClient();
        final Signaling[] holder = new Signaling[1];

        WebSocket.Listener listener = new WebSocket.Listener() {
            private final StringBuilder buf = new StringBuilder();

            @Override
            public void onOpen(WebSocket webSocket) {
                webSocket.request(1);
            }

            @Override
            public CompletionStage<?> onText(WebSocket webSocket, CharSequence data, boolean last) {
                buf.append(data);
                webSocket.request(1);
                if (last) {
                    String message = buf.toString();
                    buf.setLength(0);
                    Handler h = holder[0] == null ? null : holder[0].handler;
                    if (h != null) h.onText(message);
                }
                return null;
            }

            @Override
            public CompletionStage<?> onClose(WebSocket webSocket, int statusCode, String reason) {
                fireClosed();
                return null;
            }

            @Override
            public void onError(WebSocket webSocket, Throwable error) {
                fireClosed();
            }

            private void fireClosed() {
                if (holder[0] != null) holder[0].stopKeepalive();
                Handler h = holder[0] == null ? null : holder[0].handler;
                if (h != null) h.onClosed();
            }
        };

        WebSocket.Builder b = client.newWebSocketBuilder()
                .connectTimeout(Duration.ofMillis(timeoutMs));
        // Empty origin => omit the header (local servers with --allow-no-origin).
        if (origin != null && !origin.isEmpty()) {
            b.header("Origin", origin);
        }

        WebSocket ws;
        try {
            ws = b.buildAsync(URI.create(wsUrl), listener).get(timeoutMs, TimeUnit.MILLISECONDS);
        } catch (TimeoutException e) {
            throw new LobbyException("connect-timeout", "timed out connecting to " + wsUrl);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new LobbyException("connection-failed", "interrupted connecting to " + wsUrl);
        } catch (Exception e) {
            Throwable c = e.getCause() != null ? e.getCause() : e;
            throw new LobbyException("connection-failed", "cannot open " + wsUrl + ": " + c.getMessage());
        }

        Signaling s = new Signaling(client, ws);
        holder[0] = s;
        if (keepalive) s.startKeepalive();
        return s;
    }

    private void startKeepalive() {
        ScheduledExecutorService timer = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "lobbylink-keepalive");
            t.setDaemon(true);
            return t;
        });
        synchronized (sendLock) {
            keepalive = timer;
        }
        timer.scheduleWithFixedDelay(this::sendPing, KEEPALIVE_SECS, KEEPALIVE_SECS, TimeUnit.SECONDS);
        // The socket may have died before the timer was registered.
        if (ws.isInputClosed() || ws.isOutputClosed()) stopKeepalive();
    }

    private void stopKeepalive() {
        synchronized (sendLock) {
            if (keepalive != null) {
                keepalive.shutdownNow();
                keepalive = null;
            }
        }
    }

    private void sendPing() {
        synchronized (sendLock) {
            sendChain = sendChain
                    .thenCompose(w -> w.sendPing(ByteBuffer.allocate(0)))
                    .exceptionally(e -> ws);
        }
    }

    /** Queue a text frame; sends are chained so they never overlap. */
    public void sendText(String text) {
        synchronized (sendLock) {
            sendChain = sendChain
                    .thenCompose(w -> w.sendText(text, true))
                    .exceptionally(e -> ws); // keep the chain alive; a dead socket surfaces via onClose
        }
    }

    public void close() {
        stopKeepalive();
        try {
            ws.sendClose(WebSocket.NORMAL_CLOSURE, "");
        } catch (Exception ignore) {
            // best effort
        }
    }
}
