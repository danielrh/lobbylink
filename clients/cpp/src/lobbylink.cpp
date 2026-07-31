// lobbylink C++ client implementation. One file, organized in the same
// sections as the reference TypeScript client (clients/ts/src/index.ts):
// helpers, framing, peer link, game. The public header stays free of
// libdatachannel and JSON types via pimpl.
//
// Threading model: libdatachannel invokes WebSocket and PeerConnection
// callbacks on its internal threads. All shared state lives behind
// Impl::mu (game state) or PeerLink::mu (per-peer state), and every
// externally visible occurrence becomes an Event pushed onto one
// mutex+condvar queue that nextEvent() drains. Two hard rules keep this
// deadlock-free:
//   1. Never destroy an rtc object (whose destructor joins in-flight
//      callbacks) while holding a lock those callbacks take.
//   2. Callbacks lock at most one of {Impl::mu, PeerLink::mu, evMu,
//      wsMu} at a time and never call back into rtc teardown.

#include "lobbylink/lobbylink.hpp"

#include <nlohmann/json.hpp>
#include <rtc/rtc.hpp>

#include <algorithm>
#include <atomic>
#include <condition_variable>
#include <cstdio>
#include <cstring>
#include <deque>
#include <fstream>
#include <functional>
#include <map>
#include <mutex>
#include <thread>
#include <utility>

namespace lobbylink {

using json = nlohmann::json;

// ---------------------------------------------------------------------------
// Tunables (wire contract: clients/ts/README.md)
// ---------------------------------------------------------------------------

namespace {

constexpr std::uint8_t kFrameMagic = 0x4C; // 'L'
constexpr std::uint8_t kFrameVersion = 0x01;
constexpr std::size_t kFrameHeaderLen = 18;
// Payload bytes per reliable chunk.
constexpr std::size_t kChunkPayload = 16 * 1024;
// A received frame may not carry more payload than this.
constexpr std::size_t kMaxFramePayload = 64 * 1024;
// Send- and receive-side cap on one reliable message.
constexpr std::size_t kMaxReliableMessage = 16 * 1024 * 1024;
constexpr std::uint32_t kMaxReassemblyChunks = 4096;
constexpr auto kReassemblyTimeout = std::chrono::seconds(30);
constexpr std::size_t kMaxBestEffort = 16'000;
// Pause chunk sends above this bufferedAmount...
constexpr std::size_t kSendHighWater = 1 << 20;
// ...and resume once it drains below this.
constexpr std::size_t kSendLowWater = 256 * 1024;
constexpr auto kDefaultConnectTimeout = std::chrono::seconds(20);
// How long sendReliable waits for a usable channel to the target.
constexpr auto kChannelTimeout = std::chrono::seconds(30);
constexpr std::uint16_t kReliableChannelId = 1;
constexpr std::uint16_t kBestEffortChannelId = 2;
// Automatic ICE-failure rebuilds per peer before giving up.
constexpr int kMaxPeerRebuilds = 3;

void warn(const std::string &msg) {
    std::fprintf(stderr, "[lobbylink] %s\n", msg.c_str());
}

// Server error codes after which the WebSocket will not come back.
bool isFatalCode(const std::string &code) {
    return code == "replaced" || code == "session-superseded" ||
           code == "room-expired" || code == "slow-consumer";
}

// Fatal codes that also mean our peers are gone / we left the room.
bool isGameOverCode(const std::string &code) {
    return code == "replaced" || code == "session-superseded" ||
           code == "room-expired";
}

// Room codes: 4-64 chars of [A-Za-z0-9_-], matched exactly.
bool validCode(const std::string &code) {
    if (code.size() < 4 || code.size() > 64)
        return false;
    for (char c : code) {
        if (!((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
              (c >= '0' && c <= '9') || c == '_' || c == '-'))
            return false;
    }
    return true;
}

// signalingUrl normalizes a server URL to the ws(s) endpoint exactly
// like the other clients: http(s) -> ws(s), "/ws" appended unless
// already present, query/fragment dropped.
std::string signalingUrl(const std::string &server) {
    auto sep = server.find("://");
    if (sep == std::string::npos)
        throw LobbyError("invalid-server-url", "invalid server URL: " + server);
    std::string scheme = server.substr(0, sep);
    std::transform(scheme.begin(), scheme.end(), scheme.begin(),
                   [](unsigned char c) { return std::tolower(c); });
    std::string wsScheme;
    if (scheme == "http" || scheme == "ws")
        wsScheme = "ws";
    else if (scheme == "https" || scheme == "wss")
        wsScheme = "wss";
    else
        throw LobbyError("invalid-server-url",
                         "unsupported scheme " + scheme + " in server URL");
    std::string rest = server.substr(sep + 3);
    rest = rest.substr(0, rest.find('#'));
    rest = rest.substr(0, rest.find('?'));
    std::string authority = rest, path;
    if (auto slash = rest.find('/'); slash != std::string::npos) {
        authority = rest.substr(0, slash);
        path = rest.substr(slash);
    }
    if (authority.empty())
        throw LobbyError("invalid-server-url", "invalid server URL: " + server);
    while (!path.empty() && path.back() == '/')
        path.pop_back();
    if (path.size() < 3 || path.compare(path.size() - 3, 3, "/ws") != 0)
        path += "/ws";
    return wsScheme + "://" + authority + path;
}

// defaultOrigin derives the http(s) origin matching a normalized ws(s)
// URL; native clients send it so servers do not need --allow-no-origin.
std::string defaultOrigin(const std::string &wsUrl) {
    auto sep = wsUrl.find("://");
    std::string scheme = wsUrl.substr(0, sep);
    std::string rest = wsUrl.substr(sep + 3);
    std::string authority = rest.substr(0, rest.find('/'));
    return (scheme == "ws" ? "http://" : "https://") + authority;
}

std::string loadTokenFile(const std::string &path) {
    std::ifstream f(path, std::ios::binary);
    if (!f)
        return "";
    std::string token((std::istreambuf_iterator<char>(f)),
                      std::istreambuf_iterator<char>());
    while (!token.empty() &&
           (token.back() == '\n' || token.back() == '\r' || token.back() == ' '))
        token.pop_back();
    return token;
}

void saveTokenFile(const std::string &path, const std::string &token) {
    std::ofstream f(path, std::ios::binary | std::ios::trunc);
    if (!f) {
        warn("cannot persist resume token to " + path);
        return;
    }
    f << token;
}

std::string peerStateString(rtc::PeerConnection::State state) {
    using S = rtc::PeerConnection::State;
    switch (state) {
    case S::New:
        return "new";
    case S::Connecting:
        return "connecting";
    case S::Connected:
        return "connected";
    case S::Disconnected:
        return "disconnected";
    case S::Failed:
        return "failed";
    case S::Closed:
        return "closed";
    }
    return "unknown";
}

// ---------------------------------------------------------------------------
// Reliable-channel framing
// ---------------------------------------------------------------------------

void putU32(std::byte *p, std::uint32_t v) {
    p[0] = std::byte(v >> 24);
    p[1] = std::byte(v >> 16);
    p[2] = std::byte(v >> 8);
    p[3] = std::byte(v);
}

std::uint32_t getU32(const std::uint8_t *p) {
    return (std::uint32_t(p[0]) << 24) | (std::uint32_t(p[1]) << 16) |
           (std::uint32_t(p[2]) << 8) | std::uint32_t(p[3]);
}

rtc::binary makeFrame(std::uint32_t msgId, std::uint32_t seq, std::uint32_t total,
                      const std::uint8_t *payload, std::size_t len) {
    rtc::binary frame(kFrameHeaderLen + len);
    frame[0] = std::byte(kFrameMagic);
    frame[1] = std::byte(kFrameVersion);
    putU32(frame.data() + 2, msgId);
    putU32(frame.data() + 6, seq);
    putU32(frame.data() + 10, total);
    putU32(frame.data() + 14, std::uint32_t(len));
    if (len > 0)
        std::memcpy(frame.data() + kFrameHeaderLen, payload, len);
    return frame;
}

std::uint32_t chunkCount(std::size_t n) {
    if (n == 0)
        return 1;
    return std::uint32_t((n + kChunkPayload - 1) / kChunkPayload);
}

struct Frame {
    std::uint32_t msgId = 0, seq = 0, total = 0;
    const std::uint8_t *payload = nullptr; // aliases the receive buffer
    std::size_t payloadLen = 0;
};

// parseFrame validates one received frame; returns an error string or
// empty on success.
std::string parseFrame(const std::uint8_t *buf, std::size_t len, Frame &out) {
    if (len < kFrameHeaderLen)
        return "frame shorter than header";
    if (buf[0] != kFrameMagic)
        return "bad frame magic";
    if (buf[1] != kFrameVersion)
        return "unsupported frame version";
    out.msgId = getU32(buf + 2);
    out.seq = getU32(buf + 6);
    out.total = getU32(buf + 10);
    std::uint32_t payloadLen = getU32(buf + 14);
    if (out.total < 1 || out.total > kMaxReassemblyChunks)
        return "bad frame total";
    if (out.seq >= out.total)
        return "frame seq out of range";
    if (payloadLen > kMaxFramePayload)
        return "frame payload too large";
    if (std::size_t(payloadLen) != len - kFrameHeaderLen)
        return "frame payload length mismatch";
    out.payload = buf + kFrameHeaderLen;
    out.payloadLen = payloadLen;
    return "";
}

// Reassembler rebuilds chunked reliable messages from one sender, keyed
// by msgId. Incomplete messages are dropped after 30s; msgId reuse with
// a different total starts a new message; duplicate chunks are ignored.
class Reassembler {
public:
    // Feeds one frame; returns the full message when it completes.
    std::optional<std::vector<std::uint8_t>> push(const Frame &f) {
        auto now = std::chrono::steady_clock::now();
        for (auto it = inflight_.begin(); it != inflight_.end();) {
            if (now - it->second.startedAt > kReassemblyTimeout)
                it = inflight_.erase(it);
            else
                ++it;
        }
        auto it = inflight_.find(f.msgId);
        if (it != inflight_.end() && it->second.total != f.total) {
            inflight_.erase(it);
            it = inflight_.end();
        }
        if (it == inflight_.end()) {
            Entry e;
            e.total = f.total;
            e.chunks.resize(f.total);
            e.have.assign(f.total, false);
            e.startedAt = now;
            it = inflight_.emplace(f.msgId, std::move(e)).first;
        }
        Entry &e = it->second;
        if (!e.have[f.seq]) {
            e.chunks[f.seq].assign(f.payload, f.payload + f.payloadLen);
            e.have[f.seq] = true;
            e.received++;
            e.bytes += f.payloadLen;
            if (e.bytes > kMaxReliableMessage) {
                warn("dropping oversized reliable message");
                inflight_.erase(it);
                return std::nullopt;
            }
        }
        if (e.received < e.total)
            return std::nullopt;
        std::vector<std::uint8_t> out;
        out.reserve(e.bytes);
        for (auto &chunk : e.chunks)
            out.insert(out.end(), chunk.begin(), chunk.end());
        inflight_.erase(it);
        return out;
    }

private:
    struct Entry {
        std::uint32_t total = 0;
        std::vector<std::vector<std::uint8_t>> chunks;
        std::vector<bool> have; // distinguishes empty chunks from missing
        std::uint32_t received = 0;
        std::size_t bytes = 0;
        std::chrono::steady_clock::time_point startedAt;
    };
    std::map<std::uint32_t, Entry> inflight_;
};

// ---------------------------------------------------------------------------
// Timer queue: one worker thread running delayed peer-rebuild tasks
// ---------------------------------------------------------------------------

class TimerQueue {
public:
    TimerQueue() : worker_([this] { run(); }) {}
    ~TimerQueue() { stop(); }

    void schedule(std::chrono::milliseconds delay, std::function<void()> fn) {
        std::lock_guard<std::mutex> lock(m_);
        if (stopped_)
            return;
        tasks_.push_back({std::chrono::steady_clock::now() + delay, std::move(fn)});
        cv_.notify_all();
    }

    // Stops the worker and waits for a running task to finish. Must not
    // be called from a task.
    void stop() {
        {
            std::lock_guard<std::mutex> lock(m_);
            if (stopped_)
                return;
            stopped_ = true;
            tasks_.clear();
            cv_.notify_all();
        }
        worker_.join();
    }

private:
    struct Task {
        std::chrono::steady_clock::time_point when;
        std::function<void()> fn;
    };

    void run() {
        std::unique_lock<std::mutex> lock(m_);
        while (!stopped_) {
            auto due = std::find_if(tasks_.begin(), tasks_.end(), [](const Task &t) {
                return t.when <= std::chrono::steady_clock::now();
            });
            if (due != tasks_.end()) {
                auto fn = std::move(due->fn);
                tasks_.erase(due);
                lock.unlock();
                fn();
                lock.lock();
                continue;
            }
            if (tasks_.empty()) {
                cv_.wait(lock);
            } else {
                auto next = std::min_element(tasks_.begin(), tasks_.end(),
                                             [](const Task &a, const Task &b) {
                                                 return a.when < b.when;
                                             })
                                ->when;
                cv_.wait_until(lock, next);
            }
        }
    }

    std::mutex m_;
    std::condition_variable cv_;
    std::vector<Task> tasks_;
    bool stopped_ = false;
    std::thread worker_;
};

// ---------------------------------------------------------------------------
// Peer link: one PeerConnection + two DataChannels per remote player
// ---------------------------------------------------------------------------

struct PeerLink {
    const int playerId;
    const bool initiator;
    std::shared_ptr<rtc::PeerConnection> pc;
    std::shared_ptr<rtc::DataChannel> reliable;
    std::shared_ptr<rtc::DataChannel> bestEffort;

    std::mutex mu;
    std::condition_variable cv;
    bool closed = false;
    bool reliableOpen = false;
    std::uint32_t nextMsgId = 0;
    Reassembler reassembler;
    // ICE candidates that arrived before the remote description; only
    // queued while havePending is true.
    std::vector<json> pendingCandidates;
    bool havePending = true;

    std::mutex sendMu; // serializes reliable sends to this peer

    PeerLink(int playerId_, bool initiator_, const rtc::Configuration &config)
        : playerId(playerId_), initiator(initiator_),
          pc(std::make_shared<rtc::PeerConnection>(config)) {}

    bool isClosed() {
        std::lock_guard<std::mutex> lock(mu);
        return closed;
    }

    // queueCandidate stashes cand while the remote description is not
    // set yet; reports whether the candidate was queued.
    bool queueCandidate(const json &cand) {
        std::lock_guard<std::mutex> lock(mu);
        if (!havePending)
            return false;
        pendingCandidates.push_back(cand);
        return true;
    }

    // takePending returns the queued candidates and switches the link
    // to direct-add mode.
    std::vector<json> takePending() {
        std::lock_guard<std::mutex> lock(mu);
        havePending = false;
        return std::exchange(pendingCandidates, {});
    }

    // waitReliableOpen blocks until the reliable channel opens, the
    // link dies, or the channel timeout passes; throws on the latter two.
    void waitReliableOpen() {
        std::unique_lock<std::mutex> lock(mu);
        if (!cv.wait_for(lock, kChannelTimeout,
                         [&] { return reliableOpen || closed; }))
            throw LobbyError("channel-timeout", "timed out opening channel to player " +
                                                    std::to_string(playerId));
        if (closed)
            throw LobbyError("peer-closed",
                             "connection to player " + std::to_string(playerId) +
                                 " closed");
    }

    // awaitDrain blocks until bufferedAmount is back under the
    // low-water mark (or the link dies). Called only with
    // bufferedAmount over the high-water mark.
    void awaitDrain() {
        std::unique_lock<std::mutex> lock(mu);
        for (;;) {
            if (closed)
                throw LobbyError("peer-closed",
                                 "connection to player " + std::to_string(playerId) +
                                     " closed");
            if (reliable->bufferedAmount() <= kSendLowWater)
                return;
            // onBufferedAmountLow pings cv; the timeout is a fallback
            // poll for teardown races.
            cv.wait_for(lock, std::chrono::milliseconds(200));
        }
    }

    // sendReliableMessage chunks and writes one message on the reliable
    // channel, applying backpressure. Sends to one peer are serialized.
    void sendReliableMessage(const std::uint8_t *data, std::size_t size) {
        std::lock_guard<std::mutex> sendLock(sendMu);
        waitReliableOpen();
        std::uint32_t msgId;
        {
            std::lock_guard<std::mutex> lock(mu);
            msgId = nextMsgId++;
        }
        const std::uint32_t total = chunkCount(size);
        for (std::uint32_t seq = 0; seq < total; seq++) {
            if (isClosed())
                throw LobbyError("send-failed", "connection to player " +
                                                    std::to_string(playerId) +
                                                    " closed mid-send");
            if (reliable->bufferedAmount() > kSendHighWater)
                awaitDrain();
            const std::size_t start = std::size_t(seq) * kChunkPayload;
            const std::size_t len = std::min(kChunkPayload, size - start);
            try {
                reliable->send(makeFrame(msgId, seq, total, data + start, len));
            } catch (const std::exception &e) {
                throw LobbyError("send-failed", "send to player " +
                                                    std::to_string(playerId) +
                                                    " failed: " + e.what());
            }
        }
    }

    // sendBestEffortMessage writes one datagram, dropping it if the
    // channel is not open or its buffer is over the high-water mark
    // (that is the best-effort contract).
    void sendBestEffortMessage(const std::uint8_t *data, std::size_t size) {
        std::shared_ptr<rtc::DataChannel> dc;
        {
            std::lock_guard<std::mutex> lock(mu);
            if (closed)
                return;
            dc = bestEffort;
        }
        if (!dc || !dc->isOpen() || dc->bufferedAmount() > kSendHighWater)
            return;
        try {
            dc->send(reinterpret_cast<const std::byte *>(data), size);
        } catch (...) {
            // racing close: drop
        }
    }

    // closeLink tears the link down. Callbacks are reset first so
    // nothing references the game afterwards; safe to call from any
    // thread except the link's own rtc callbacks.
    void closeLink() {
        {
            std::lock_guard<std::mutex> lock(mu);
            if (closed)
                return;
            closed = true;
            pendingCandidates.clear();
            havePending = false;
            cv.notify_all();
        }
        if (reliable)
            reliable->resetCallbacks();
        if (bestEffort)
            bestEffort->resetCallbacks();
        pc->resetCallbacks();
        try {
            if (reliable)
                reliable->close();
        } catch (...) {
        }
        try {
            if (bestEffort)
                bestEffort->close();
        } catch (...) {
        }
        try {
            pc->close();
        } catch (...) {
        }
    }
};

// awaitLink waiter: filled in by createLink or teardown.
struct LinkWaiter {
    std::mutex m;
    std::condition_variable cv;
    std::shared_ptr<PeerLink> link;
    bool done = false;
};

} // namespace

// ---------------------------------------------------------------------------
// Signaling context: shared by the WebSocket callbacks and the game
// ---------------------------------------------------------------------------

// The WebSocket callbacks capture only this shared context, never the
// game directly, so a callback that fires during connect() (before the
// game exists) or during teardown (after ctx->game is nulled) is safe.
// Server messages that race the handshake are buffered in `backlog`
// and flushed, still ordered, when the game attaches.
struct SignalCtx {
    std::mutex m;
    std::condition_variable cv;
    bool wsOpen = false;
    bool failed = false;
    // The socket died between "joined" and game attach; reported as a
    // SignalingClosed event once the game is live.
    bool closedEarly = false;
    std::string failCode, failMsg;
    std::optional<json> joined;
    std::vector<json> backlog;
    P2PGame::Impl *game = nullptr;
};

// ---------------------------------------------------------------------------
// P2PGame
// ---------------------------------------------------------------------------

struct P2PGame::Impl {
    // Immutable after connect.
    std::string code;
    int selfId = 0;
    int maxPlayers = 0;
    std::string resumeToken;
    std::string tokenFile;
    std::vector<ICEServer> iceServers;
    rtc::Configuration rtcConfig;

    std::shared_ptr<rtc::WebSocket> ws;
    std::shared_ptr<SignalCtx> ctx;
    std::mutex wsMu; // serializes WebSocket sends

    std::mutex mu; // game state below
    std::vector<PlayerInfo> roster;
    bool startedFlag = false;
    bool closedFlag = false;
    bool fatal = false;
    std::map<int, std::shared_ptr<PeerLink>> peers;
    std::map<int, int> rebuilds;
    std::map<int, std::vector<std::shared_ptr<LinkWaiter>>> linkWaiters;
    std::atomic<bool> closedAtomic{false};

    std::mutex evMu; // event queue below
    std::condition_variable evCv;
    std::deque<Event> evQueue;
    bool evClosed = false;

    TimerQueue timers;

    // -- events -------------------------------------------------------------

    void emit(Event ev) {
        if (closedAtomic.load())
            return;
        std::lock_guard<std::mutex> lock(evMu);
        if (evClosed)
            return;
        evQueue.push_back(std::move(ev));
        evCv.notify_one();
    }

    static Event messageEvent(int from, MessageKind kind,
                              std::vector<std::uint8_t> data) {
        Event ev;
        ev.type = Event::Type::Message;
        ev.playerId = from;
        ev.kind = kind;
        ev.data = std::move(data);
        return ev;
    }

    // -- signaling ----------------------------------------------------------

    void sendWs(const json &msg) {
        std::lock_guard<std::mutex> lock(wsMu);
        try {
            if (ws && ws->isOpen())
                ws->send(msg.dump());
        } catch (const std::exception &e) {
            // Socket died; onClosed reports the loss.
            warn(std::string("signaling send failed: ") + e.what());
        }
    }

    void sendSignal(int to, json payload) {
        sendWs(json{{"type", "signal"}, {"to", to}, {"payload", std::move(payload)}});
    }

    void onSignalingLost() {
        {
            std::lock_guard<std::mutex> lock(mu);
            if (closedFlag || fatal)
                return;
            fatal = true;
        }
        Event ev;
        ev.type = Event::Type::SignalingClosed;
        ev.code = "connection-lost";
        ev.message = "signaling connection lost; existing peer channels stay up";
        emit(std::move(ev));
    }

    // -- lobby message handling ---------------------------------------------

    void applyRoster(const json &players) {
        // Callers hold mu. The server sends all slots in id order.
        if (!players.is_array())
            return;
        for (const auto &p : players) {
            int id = p.value("id", -1);
            if (id < 0 || id >= int(roster.size()))
                continue;
            roster[id].occupied = p.value("occupied", false);
            roster[id].connected = p.value("connected", false);
        }
    }

    void markOccupied(int playerId) {
        std::lock_guard<std::mutex> lock(mu);
        if (playerId >= 0 && playerId < int(roster.size())) {
            roster[playerId].occupied = true;
            roster[playerId].connected = true;
        }
    }

    void handleServerMessage(const json &msg) {
        const std::string type = msg.value("type", "");
        if (type == "player-joined") {
            int playerId = msg.value("playerId", -1);
            {
                std::lock_guard<std::mutex> lock(mu);
                if (msg.contains("players"))
                    applyRoster(msg["players"]);
            }
            Event ev;
            ev.type = Event::Type::PlayerJoined;
            ev.playerId = playerId;
            emit(std::move(ev));
            resetPeer(playerId);
        } else if (type == "player-left") {
            int playerId = msg.value("playerId", -1);
            std::string reason = msg.value("reason", "") == "explicit-leave"
                                     ? "explicit-leave"
                                     : "disconnected";
            {
                std::lock_guard<std::mutex> lock(mu);
                if (playerId >= 0 && playerId < int(roster.size())) {
                    if (reason == "explicit-leave")
                        roster[playerId].occupied = false;
                    roster[playerId].connected = false;
                }
            }
            if (reason == "explicit-leave")
                closePeer(playerId);
            // On "disconnected" the peer only lost signaling; an
            // established DataChannel may well still be alive, so keep it.
            Event ev;
            ev.type = Event::Type::PlayerLeft;
            ev.playerId = playerId;
            ev.reason = reason;
            emit(std::move(ev));
        } else if (type == "player-rejoined") {
            int playerId = msg.value("playerId", -1);
            markOccupied(playerId);
            Event ev;
            ev.type = Event::Type::PlayerRejoined;
            ev.playerId = playerId;
            ev.wasReplacement = msg.value("wasReplacement", false);
            emit(std::move(ev));
            resetPeer(playerId);
        } else if (type == "player-replaced") {
            int playerId = msg.value("playerId", -1);
            markOccupied(playerId);
            Event ev;
            ev.type = Event::Type::PlayerReplaced;
            ev.playerId = playerId;
            emit(std::move(ev));
            resetPeer(playerId);
        } else if (type == "room-started") {
            {
                std::lock_guard<std::mutex> lock(mu);
                startedFlag = true;
            }
            Event ev;
            ev.type = Event::Type::Started;
            emit(std::move(ev));
        } else if (type == "signal") {
            if (msg.contains("payload") && msg["payload"].is_object())
                handleSignal(msg.value("from", -1), msg["payload"]);
        } else if (type == "error") {
            std::string code = msg.value("code", "internal");
            std::string message = msg.value("message", "");
            if (isFatalCode(code)) {
                {
                    std::lock_guard<std::mutex> lock(mu);
                    fatal = true;
                }
                if (isGameOverCode(code)) {
                    teardownPeers();
                    // "session-superseded" means our own token resumed
                    // elsewhere; that process owns the token file now.
                    if (!tokenFile.empty() && code != "session-superseded")
                        std::remove(tokenFile.c_str());
                }
                Event ev;
                ev.type = Event::Type::SignalingClosed;
                ev.code = code;
                ev.message = message;
                emit(std::move(ev));
            } else {
                Event ev;
                ev.type = Event::Type::LobbyError;
                ev.code = code;
                ev.message = message;
                emit(std::move(ev));
            }
        } else {
            // "joined" is only expected once (handled in connect); any
            // unknown message type is ignored for forward compatibility.
        }
    }

    // -- WebRTC signaling ---------------------------------------------------

    // takeLinkLocked removes and returns the current link (mu held);
    // the caller closes it outside the lock (rule 1 in the header).
    std::shared_ptr<PeerLink> takeLinkLocked(int playerId) {
        auto it = peers.find(playerId);
        if (it == peers.end())
            return nullptr;
        auto link = it->second;
        peers.erase(it);
        return link;
    }

    // createLinkLocked builds the PeerConnection and registers its
    // callbacks (mu held). DataChannels are created separately: the
    // initiator creates them immediately (which triggers the offer via
    // auto-negotiation), the answerer only after setRemoteDescription
    // — creating negotiated channels on a fresh PeerConnection would
    // make libdatachannel emit an offer from the answering side too.
    std::shared_ptr<PeerLink> createLinkLocked(int playerId, bool initiator) {
        auto link = std::make_shared<PeerLink>(playerId, initiator, rtcConfig);
        peers[playerId] = link;
        std::weak_ptr<PeerLink> wlink = link;
        Impl *g = this;

        link->pc->onLocalDescription([g, wlink, playerId](rtc::Description desc) {
            auto l = wlink.lock();
            if (!l || l->isClosed())
                return;
            g->sendSignal(playerId,
                          json{{"kind", desc.typeString()}, {"sdp", std::string(desc)}});
        });
        link->pc->onLocalCandidate([g, wlink, playerId](rtc::Candidate cand) {
            auto l = wlink.lock();
            if (!l || l->isClosed())
                return;
            g->sendSignal(playerId,
                          json{{"kind", "ice"},
                               {"candidate", json{{"candidate", cand.candidate()},
                                                  {"sdpMid", cand.mid()}}}});
        });
        link->pc->onStateChange([g, wlink, playerId](rtc::PeerConnection::State state) {
            auto l = wlink.lock();
            if (!l || l->isClosed())
                return;
            Event ev;
            ev.type = Event::Type::PeerState;
            ev.playerId = playerId;
            ev.state = peerStateString(state);
            g->emit(std::move(ev));
            if (state == rtc::PeerConnection::State::Connected) {
                std::lock_guard<std::mutex> lock(g->mu);
                g->rebuilds.erase(playerId);
            } else if (state == rtc::PeerConnection::State::Failed) {
                g->handlePeerFailure(l);
            }
        });
        return link;
    }

    // createChannels creates the two pre-negotiated DataChannels and
    // wires the data path. Both sides create both channels.
    void createChannels(const std::shared_ptr<PeerLink> &link) {
        std::weak_ptr<PeerLink> wlink = link;
        Impl *g = this;
        try {
            rtc::DataChannelInit reliableInit;
            reliableInit.negotiated = true;
            reliableInit.id = kReliableChannelId;
            link->reliable = link->pc->createDataChannel("reliable", reliableInit);

            rtc::DataChannelInit bestEffortInit;
            bestEffortInit.negotiated = true;
            bestEffortInit.id = kBestEffortChannelId;
            bestEffortInit.reliability.unordered = true;
            bestEffortInit.reliability.maxRetransmits = 0;
            link->bestEffort =
                link->pc->createDataChannel("best-effort", bestEffortInit);
        } catch (const std::exception &e) {
            warn("cannot create data channels for player " +
                 std::to_string(link->playerId) + ": " + e.what());
            return;
        }

        link->reliable->onOpen([wlink] {
            if (auto l = wlink.lock()) {
                std::lock_guard<std::mutex> lock(l->mu);
                l->reliableOpen = true;
                l->cv.notify_all();
            }
        });
        link->reliable->onClosed([wlink] {
            if (auto l = wlink.lock()) {
                std::lock_guard<std::mutex> lock(l->mu);
                l->cv.notify_all(); // wake senders; closed is checked there
            }
        });
        link->reliable->setBufferedAmountLowThreshold(kSendLowWater);
        link->reliable->onBufferedAmountLow([wlink] {
            if (auto l = wlink.lock()) {
                std::lock_guard<std::mutex> lock(l->mu);
                l->cv.notify_all();
            }
        });
        link->reliable->onMessage([g, wlink](rtc::message_variant data) {
            auto l = wlink.lock();
            if (!l || l->isClosed())
                return;
            g->onReliableData(l, data);
        });
        link->bestEffort->onMessage([g, wlink](rtc::message_variant data) {
            auto l = wlink.lock();
            if (!l || l->isClosed())
                return;
            g->emit(messageEvent(l->playerId, MessageKind::BestEffort,
                                 toBytes(data)));
        });
        // A racing open before onOpen was registered would otherwise be
        // missed; latch the current state.
        if (link->reliable->isOpen()) {
            std::lock_guard<std::mutex> lock(link->mu);
            link->reliableOpen = true;
            link->cv.notify_all();
        }
    }

    static std::vector<std::uint8_t> toBytes(const rtc::message_variant &data) {
        if (std::holds_alternative<rtc::binary>(data)) {
            const auto &b = std::get<rtc::binary>(data);
            const auto *p = reinterpret_cast<const std::uint8_t *>(b.data());
            return std::vector<std::uint8_t>(p, p + b.size());
        }
        const auto &s = std::get<std::string>(data);
        return std::vector<std::uint8_t>(s.begin(), s.end());
    }

    void onReliableData(const std::shared_ptr<PeerLink> &link,
                        const rtc::message_variant &data) {
        auto bytes = toBytes(data);
        Frame f;
        if (auto err = parseFrame(bytes.data(), bytes.size(), f); !err.empty()) {
            warn("dropping reliable frame from player " +
                 std::to_string(link->playerId) + ": " + err);
            return;
        }
        std::optional<std::vector<std::uint8_t>> full;
        {
            std::lock_guard<std::mutex> lock(link->mu);
            full = link->reassembler.push(f);
        }
        if (full)
            emit(messageEvent(link->playerId, MessageKind::Reliable,
                              std::move(*full)));
    }

    // notifyWaiters fulfills every awaitLink() blocked on playerId.
    void notifyWaiters(int playerId, const std::shared_ptr<PeerLink> &link) {
        std::vector<std::shared_ptr<LinkWaiter>> waiters;
        {
            std::lock_guard<std::mutex> lock(mu);
            auto it = linkWaiters.find(playerId);
            if (it == linkWaiters.end())
                return;
            waiters = std::move(it->second);
            linkWaiters.erase(it);
        }
        for (auto &w : waiters) {
            std::lock_guard<std::mutex> lock(w->m);
            w->link = link;
            w->done = true;
            w->cv.notify_all();
        }
    }

    // initiatePeer starts (or restarts) an offer to playerId. Creating
    // the first negotiated channel triggers the offer, delivered via
    // onLocalDescription.
    void initiatePeer(int playerId) {
        std::shared_ptr<PeerLink> link, old;
        {
            std::lock_guard<std::mutex> lock(mu);
            if (closedFlag)
                return;
            old = takeLinkLocked(playerId);
            link = createLinkLocked(playerId, true);
        }
        if (old)
            old->closeLink();
        createChannels(link);
        notifyWaiters(playerId, link);
    }

    // resetPeer drops any old link after a peer got a new session and
    // re-offers if we are the initiator.
    void resetPeer(int playerId) {
        if (playerId == selfId || playerId < 0)
            return;
        {
            std::lock_guard<std::mutex> lock(mu);
            rebuilds.erase(playerId);
        }
        if (selfId < playerId) {
            initiatePeer(playerId); // replaces (and closes) the old link
        } else {
            closePeer(playerId); // the lower-ID peer will offer to us
        }
    }

    void handleSignal(int from, const json &payload) {
        if (from == selfId || from < 0)
            return;
        const std::string kind = payload.value("kind", "");
        if (kind == "offer") {
            if (selfId < from) {
                warn("ignoring offer from higher-ID player " + std::to_string(from) +
                     " (protocol says we offer)");
                return;
            }
            // Every incoming offer starts a fresh session (initial
            // connect or the initiator rebuilding after a failure).
            std::shared_ptr<PeerLink> link, old;
            {
                std::lock_guard<std::mutex> lock(mu);
                if (closedFlag)
                    return;
                old = takeLinkLocked(from);
                link = createLinkLocked(from, false);
            }
            if (old)
                old->closeLink();
            try {
                // Auto-negotiation answers inside setRemoteDescription;
                // the answer reaches us via onLocalDescription.
                link->pc->setRemoteDescription(
                    rtc::Description(payload.value("sdp", ""), "offer"));
            } catch (const std::exception &e) {
                warn("answering offer from player " + std::to_string(from) +
                     " failed: " + e.what());
                return;
            }
            flushCandidates(link);
            createChannels(link);
            notifyWaiters(from, link);
        } else if (kind == "answer") {
            std::shared_ptr<PeerLink> link;
            {
                std::lock_guard<std::mutex> lock(mu);
                auto it = peers.find(from);
                if (it != peers.end())
                    link = it->second;
            }
            if (!link || link->isClosed() ||
                link->pc->signalingState() !=
                    rtc::PeerConnection::SignalingState::HaveLocalOffer) {
                warn("ignoring stale answer from player " + std::to_string(from));
                return;
            }
            try {
                link->pc->setRemoteDescription(
                    rtc::Description(payload.value("sdp", ""), "answer"));
            } catch (const std::exception &e) {
                warn("answer from player " + std::to_string(from) +
                     " failed: " + e.what());
                return;
            }
            flushCandidates(link);
        } else if (kind == "ice") {
            std::shared_ptr<PeerLink> link;
            {
                std::lock_guard<std::mutex> lock(mu);
                auto it = peers.find(from);
                if (it != peers.end())
                    link = it->second;
            }
            if (!link || link->isClosed())
                return;
            json cand = payload.contains("candidate") ? payload["candidate"] : json();
            if (link->queueCandidate(cand))
                return;
            addCandidate(link, cand);
        }
    }

    void flushCandidates(const std::shared_ptr<PeerLink> &link) {
        for (const auto &cand : link->takePending())
            addCandidate(link, cand);
    }

    void addCandidate(const std::shared_ptr<PeerLink> &link, const json &cand) {
        // null is the optional end-of-candidates marker.
        if (!cand.is_object())
            return;
        std::string candidate = cand.value("candidate", "");
        if (candidate.empty())
            return;
        try {
            if (cand.contains("sdpMid") && cand["sdpMid"].is_string())
                link->pc->addRemoteCandidate(
                    rtc::Candidate(candidate, cand["sdpMid"].get<std::string>()));
            else
                link->pc->addRemoteCandidate(rtc::Candidate(candidate));
        } catch (const std::exception &e) {
            if (!link->isClosed())
                warn("addRemoteCandidate for player " +
                     std::to_string(link->playerId) + " failed: " + e.what());
        }
    }

    // handlePeerFailure schedules an initiator-side rebuild with
    // backoff (1s * count, at most kMaxPeerRebuilds attempts).
    void handlePeerFailure(const std::shared_ptr<PeerLink> &link) {
        if (!link->initiator)
            return;
        const int playerId = link->playerId;
        int count;
        {
            std::lock_guard<std::mutex> lock(mu);
            if (closedFlag)
                return;
            count = ++rebuilds[playerId];
        }
        if (count > kMaxPeerRebuilds) {
            warn("giving up on player " + std::to_string(playerId) + " after " +
                 std::to_string(kMaxPeerRebuilds) + " rebuilds");
            return;
        }
        std::weak_ptr<PeerLink> wlink = link;
        timers.schedule(std::chrono::seconds(count), [this, playerId, wlink] {
            auto l = wlink.lock();
            if (!l)
                return;
            {
                std::lock_guard<std::mutex> lock(mu);
                auto it = peers.find(playerId);
                bool slotOK = playerId < int(roster.size()) &&
                              roster[playerId].occupied && roster[playerId].connected;
                if (closedFlag || it == peers.end() || it->second != l ||
                    l->pc->state() != rtc::PeerConnection::State::Failed || !slotOK)
                    return;
            }
            initiatePeer(playerId);
        });
    }

    // -- data path helpers --------------------------------------------------

    void checkTarget(int to) {
        if (to < 0 || to >= maxPlayers)
            throw LobbyError("invalid-target",
                             "player id " + std::to_string(to) + " out of range 0.." +
                                 std::to_string(maxPlayers - 1));
        if (to == selfId)
            throw LobbyError("invalid-target", "cannot send to yourself");
    }

    // awaitLink resolves the current link to a peer, waiting for one to
    // be created if necessary.
    std::shared_ptr<PeerLink> awaitLink(int playerId) {
        std::shared_ptr<LinkWaiter> waiter;
        {
            std::lock_guard<std::mutex> lock(mu);
            auto it = peers.find(playerId);
            if (it != peers.end() && !it->second->isClosed())
                return it->second;
            if (closedFlag)
                throw LobbyError("closed", "game is closed");
            waiter = std::make_shared<LinkWaiter>();
            linkWaiters[playerId].push_back(waiter);
        }
        std::unique_lock<std::mutex> lock(waiter->m);
        if (!waiter->cv.wait_for(lock, kChannelTimeout, [&] { return waiter->done; })) {
            std::lock_guard<std::mutex> glock(mu);
            auto &list = linkWaiters[playerId];
            list.erase(std::remove(list.begin(), list.end(), waiter), list.end());
            throw LobbyError("channel-timeout",
                             "no WebRTC session with player " +
                                 std::to_string(playerId) + " within 30s");
        }
        if (!waiter->link)
            throw LobbyError("closed", "game is closed");
        return waiter->link;
    }

    // -- teardown -----------------------------------------------------------

    void closePeer(int playerId) {
        std::shared_ptr<PeerLink> link;
        {
            std::lock_guard<std::mutex> lock(mu);
            link = takeLinkLocked(playerId);
        }
        if (link)
            link->closeLink();
    }

    void teardownPeers() {
        std::vector<std::shared_ptr<PeerLink>> links;
        std::map<int, std::vector<std::shared_ptr<LinkWaiter>>> waiters;
        {
            std::lock_guard<std::mutex> lock(mu);
            for (auto &[id, link] : peers)
                links.push_back(link);
            peers.clear();
            waiters = std::exchange(linkWaiters, {});
        }
        for (auto &link : links)
            link->closeLink();
        for (auto &[id, list] : waiters) {
            for (auto &w : list) {
                std::lock_guard<std::mutex> lock(w->m);
                w->done = true; // link stays null: reported as "closed"
                w->cv.notify_all();
            }
        }
    }

    void close() {
        {
            std::lock_guard<std::mutex> lock(mu);
            if (closedFlag)
                return;
            closedFlag = true;
        }
        closedAtomic.store(true);
        // Detach the WebSocket callbacks from this game first; the ws
        // object itself stays alive until destruction so an in-flight
        // callback never touches freed memory.
        if (ctx) {
            std::lock_guard<std::mutex> lock(ctx->m);
            ctx->game = nullptr;
        }
        sendWs(json{{"type", "leave"}});
        {
            std::lock_guard<std::mutex> lock(wsMu);
            try {
                if (ws)
                    ws->close();
            } catch (...) {
            }
        }
        timers.stop();
        teardownPeers();
        if (!tokenFile.empty())
            std::remove(tokenFile.c_str());
        {
            std::lock_guard<std::mutex> lock(evMu);
            evClosed = true; // queued events still drain; then Closed
            evCv.notify_all();
        }
    }
};

// ---------------------------------------------------------------------------
// connect
// ---------------------------------------------------------------------------

namespace {

json buildJoinMessage(const ConnectOptions &opts) {
    json msg;
    if (opts.claimPlayerId) {
        msg["type"] = "claim-slot";
        msg["code"] = opts.code;
        msg["playerId"] = *opts.claimPlayerId;
    } else {
        msg["type"] = "join";
        msg["code"] = opts.code;
        std::string token = opts.resumeToken;
        if (token.empty() && !opts.tokenFile.empty())
            token = loadTokenFile(opts.tokenFile);
        if (!token.empty())
            msg["resumeToken"] = token;
        if (opts.create) {
            const CreateOptions &c = *opts.create;
            json create{{"maxPlayers", c.maxPlayers}};
            if (c.waitUntilFull)
                create["waitUntilFull"] = *c.waitUntilFull;
            if (c.allowLateJoin)
                create["allowLateJoin"] = *c.allowLateJoin;
            if (c.allowReconnect)
                create["allowReconnect"] = *c.allowReconnect;
            if (c.allowReplacement)
                create["allowReplacement"] = *c.allowReplacement;
            if (!c.reconnectPolicy.empty())
                create["reconnectPolicy"] = c.reconnectPolicy;
            if (c.claimAfter)
                create["claimAfterMs"] = c.claimAfter->count();
            msg["create"] = std::move(create);
        }
    }
    if (!opts.appId.empty())
        msg["appId"] = opts.appId;
    return msg;
}

rtc::Configuration buildRtcConfig(const std::vector<ICEServer> &servers,
                                  bool forceRelay) {
    rtc::Configuration config;
    for (const auto &s : servers) {
        for (const auto &url : s.urls) {
            try {
                rtc::IceServer ice(url);
                if (!s.username.empty()) {
                    ice.username = s.username;
                    ice.password = s.credential;
                }
                config.iceServers.push_back(std::move(ice));
            } catch (const std::exception &e) {
                warn("ignoring ICE server " + url + ": " + e.what());
            }
        }
    }
    if (forceRelay)
        config.iceTransportPolicy = rtc::TransportPolicy::Relay;
    return config;
}

} // namespace

P2PGame::P2PGame() : impl_(std::make_unique<Impl>()) {}

P2PGame::~P2PGame() {
    if (impl_)
        impl_->close();
}

std::unique_ptr<P2PGame> P2PGame::connect(const ConnectOptions &opts) {
    if (!validCode(opts.code))
        throw LobbyError("invalid-code", "room code must be 4-64 chars of [A-Za-z0-9_-]");
    const std::string url = signalingUrl(opts.server); // throws invalid-server-url
    const std::string origin = opts.origin ? *opts.origin : defaultOrigin(url);
    const auto timeout = opts.connectTimeout.count() > 0 ? opts.connectTimeout
                                                         : kDefaultConnectTimeout;
    const auto deadline = std::chrono::steady_clock::now() + timeout;

    auto ctx = std::make_shared<SignalCtx>();
    auto ws = std::make_shared<rtc::WebSocket>();

    ws->onOpen([ctx] {
        std::lock_guard<std::mutex> lock(ctx->m);
        ctx->wsOpen = true;
        ctx->cv.notify_all();
    });
    ws->onError([ctx](std::string error) {
        std::lock_guard<std::mutex> lock(ctx->m);
        if (ctx->game)
            return; // onClosed carries the state change
        if (!ctx->failed) {
            ctx->failed = true;
            ctx->failCode = "connection-failed";
            ctx->failMsg = "WebSocket error: " + error;
        }
        ctx->cv.notify_all();
    });
    ws->onClosed([ctx] {
        P2PGame::Impl *g = nullptr;
        {
            std::lock_guard<std::mutex> lock(ctx->m);
            if (ctx->game) {
                g = ctx->game;
            } else if (ctx->joined) {
                ctx->closedEarly = true;
            } else if (!ctx->failed) {
                ctx->failed = true;
                ctx->failCode = "connection-closed";
                ctx->failMsg = "connection closed before join completed";
            }
            ctx->cv.notify_all();
        }
        if (g)
            g->onSignalingLost();
    });
    ws->onMessage([ctx](rtc::message_variant data) {
        if (!std::holds_alternative<std::string>(data))
            return;
        json msg = json::parse(std::get<std::string>(data), nullptr, false);
        if (msg.is_discarded() || !msg.is_object()) {
            warn("malformed server message");
            return;
        }
        P2PGame::Impl *g = nullptr;
        {
            std::lock_guard<std::mutex> lock(ctx->m);
            if (ctx->game) {
                g = ctx->game;
            } else if (ctx->joined) {
                // Between "joined" and game attach: buffer, keep order.
                ctx->backlog.push_back(std::move(msg));
                return;
            } else {
                const std::string type = msg.value("type", "");
                if (type == "joined") {
                    ctx->joined = std::move(msg);
                    ctx->cv.notify_all();
                } else if (type == "error") {
                    ctx->failed = true;
                    ctx->failCode = msg.value("code", "internal");
                    ctx->failMsg = msg.value("message", "");
                    ctx->cv.notify_all();
                }
                // Anything else before "joined" is unexpected; ignore.
                return;
            }
        }
        if (g)
            g->handleServerMessage(msg);
    });

    try {
        rtc::WebSocket::Headers headers;
        if (!origin.empty())
            headers.emplace("Origin", origin);
        ws->open(url, headers);
    } catch (const std::exception &e) {
        ws->resetCallbacks();
        throw LobbyError("connection-failed",
                         "cannot open " + url + ": " + e.what());
    }

    // Handshake: wait for open, send join, wait for joined or error —
    // all bounded by one connect deadline.
    auto fail = [&](const std::string &code, const std::string &msg) -> void {
        ws->resetCallbacks();
        try {
            ws->close();
        } catch (...) {
        }
        throw LobbyError(code, msg);
    };
    {
        std::unique_lock<std::mutex> lock(ctx->m);
        if (!ctx->cv.wait_until(lock, deadline,
                                [&] { return ctx->wsOpen || ctx->failed; })) {
            lock.unlock();
            fail("connect-timeout", "timed out connecting to " + url);
        }
        if (ctx->failed) {
            auto code = ctx->failCode, msg = ctx->failMsg;
            lock.unlock();
            fail(code, msg);
        }
    }
    try {
        ws->send(buildJoinMessage(opts).dump());
    } catch (const std::exception &e) {
        fail("connection-failed", std::string("join send failed: ") + e.what());
    }
    json joined;
    {
        std::unique_lock<std::mutex> lock(ctx->m);
        if (!ctx->cv.wait_until(lock, deadline,
                                [&] { return ctx->joined || ctx->failed; })) {
            lock.unlock();
            fail("connect-timeout", "timed out connecting to " + url);
        }
        if (ctx->failed) {
            auto code = ctx->failCode, msg = ctx->failMsg;
            lock.unlock();
            fail(code, msg);
        }
        joined = *ctx->joined;
    }

    // Joined: build the live game.
    auto game = std::unique_ptr<P2PGame>(new P2PGame());
    Impl *g = game->impl_.get();
    g->code = joined.value("code", opts.code);
    g->selfId = joined.value("selfId", 0);
    g->maxPlayers = joined.value("maxPlayers", 0);
    g->startedFlag = joined.value("started", false);
    g->resumeToken = joined.value("resumeToken", "");
    g->tokenFile = opts.tokenFile;
    g->ws = ws;
    g->ctx = ctx;
    g->roster.resize(std::max(0, g->maxPlayers));
    for (int i = 0; i < g->maxPlayers; i++)
        g->roster[i].id = i;
    if (joined.contains("players"))
        g->applyRoster(joined["players"]);
    if (joined.contains("iceServers") && joined["iceServers"].is_array()) {
        for (const auto &s : joined["iceServers"]) {
            ICEServer ice;
            if (s.contains("urls")) {
                if (s["urls"].is_string())
                    ice.urls.push_back(s["urls"].get<std::string>());
                else if (s["urls"].is_array())
                    for (const auto &u : s["urls"])
                        if (u.is_string())
                            ice.urls.push_back(u.get<std::string>());
            }
            ice.username = s.value("username", "");
            ice.credential = s.value("credential", "");
            g->iceServers.push_back(std::move(ice));
        }
    }
    g->iceServers.insert(g->iceServers.end(), opts.iceServers.begin(),
                         opts.iceServers.end());
    g->rtcConfig = buildRtcConfig(g->iceServers, opts.forceRelay);
    if (!g->tokenFile.empty())
        saveTokenFile(g->tokenFile, g->resumeToken);

    // Lower ID initiates: offer to every connected peer with a higher
    // ID. Lower-ID peers offer to us when they see player-joined.
    {
        std::vector<int> targets;
        {
            std::lock_guard<std::mutex> lock(g->mu);
            for (const auto &p : g->roster)
                if (p.id != g->selfId && p.occupied && p.connected && g->selfId < p.id)
                    targets.push_back(p.id);
        }
        for (int id : targets)
            g->initiatePeer(id);
    }

    // Attach: replay any messages that arrived since "joined" while
    // holding ctx->m so concurrent WebSocket callbacks stay ordered
    // behind the backlog.
    bool closedEarly;
    {
        std::lock_guard<std::mutex> lock(ctx->m);
        for (const auto &msg : ctx->backlog)
            g->handleServerMessage(msg);
        ctx->backlog.clear();
        ctx->game = g;
        closedEarly = ctx->closedEarly;
    }
    if (closedEarly)
        g->onSignalingLost();
    return game;
}

// ---------------------------------------------------------------------------
// Public method forwarding
// ---------------------------------------------------------------------------

int P2PGame::selfId() const { return impl_->selfId; }
const std::string &P2PGame::code() const { return impl_->code; }
int P2PGame::maxPlayers() const { return impl_->maxPlayers; }
const std::string &P2PGame::resumeToken() const { return impl_->resumeToken; }

bool P2PGame::started() const {
    std::lock_guard<std::mutex> lock(impl_->mu);
    return impl_->startedFlag;
}

std::vector<PlayerInfo> P2PGame::players() const {
    std::lock_guard<std::mutex> lock(impl_->mu);
    return impl_->roster;
}

std::vector<ICEServer> P2PGame::iceServers() const { return impl_->iceServers; }

Event P2PGame::nextEvent() {
    Impl *g = impl_.get();
    std::unique_lock<std::mutex> lock(g->evMu);
    g->evCv.wait(lock, [&] { return !g->evQueue.empty() || g->evClosed; });
    if (!g->evQueue.empty()) {
        Event ev = std::move(g->evQueue.front());
        g->evQueue.pop_front();
        return ev;
    }
    Event ev;
    ev.type = Event::Type::Closed;
    return ev;
}

std::optional<Event> P2PGame::nextEvent(std::chrono::milliseconds timeout) {
    Impl *g = impl_.get();
    std::unique_lock<std::mutex> lock(g->evMu);
    if (!g->evCv.wait_for(lock, timeout,
                          [&] { return !g->evQueue.empty() || g->evClosed; }))
        return std::nullopt;
    if (!g->evQueue.empty()) {
        Event ev = std::move(g->evQueue.front());
        g->evQueue.pop_front();
        return ev;
    }
    Event ev;
    ev.type = Event::Type::Closed;
    return ev;
}

void P2PGame::sendReliable(int to, const std::uint8_t *data, std::size_t size) {
    Impl *g = impl_.get();
    g->checkTarget(to);
    {
        std::lock_guard<std::mutex> lock(g->mu);
        if (!(to < int(g->roster.size()) && g->roster[to].occupied))
            throw LobbyError("target-unavailable",
                             "no player in slot " + std::to_string(to));
    }
    if (size > kMaxReliableMessage)
        throw LobbyError("message-too-large",
                         "reliable payload " + std::to_string(size) + " exceeds " +
                             std::to_string(kMaxReliableMessage) + " bytes");
    auto link = g->awaitLink(to);
    link->sendReliableMessage(data, size);
}

void P2PGame::sendReliable(int to, const std::vector<std::uint8_t> &data) {
    sendReliable(to, data.data(), data.size());
}

void P2PGame::sendBestEffort(int to, const std::uint8_t *data, std::size_t size) {
    Impl *g = impl_.get();
    g->checkTarget(to);
    if (size > kMaxBestEffort)
        throw LobbyError("message-too-large",
                         "best-effort payload " + std::to_string(size) + " exceeds " +
                             std::to_string(kMaxBestEffort) + " bytes");
    std::shared_ptr<PeerLink> link;
    {
        std::lock_guard<std::mutex> lock(g->mu);
        auto it = g->peers.find(to);
        if (it != g->peers.end())
            link = it->second;
    }
    if (link)
        link->sendBestEffortMessage(data, size);
}

void P2PGame::sendBestEffort(int to, const std::vector<std::uint8_t> &data) {
    sendBestEffort(to, data.data(), data.size());
}

void P2PGame::broadcastBestEffort(const std::uint8_t *data, std::size_t size) {
    Impl *g = impl_.get();
    if (size > kMaxBestEffort)
        throw LobbyError("message-too-large",
                         "best-effort payload " + std::to_string(size) + " exceeds " +
                             std::to_string(kMaxBestEffort) + " bytes");
    std::vector<std::shared_ptr<PeerLink>> links;
    {
        std::lock_guard<std::mutex> lock(g->mu);
        for (const auto &p : g->roster) {
            if (p.id != g->selfId && p.occupied) {
                auto it = g->peers.find(p.id);
                if (it != g->peers.end())
                    links.push_back(it->second);
            }
        }
    }
    for (auto &link : links)
        link->sendBestEffortMessage(data, size);
}

void P2PGame::broadcastBestEffort(const std::vector<std::uint8_t> &data) {
    broadcastBestEffort(data.data(), data.size());
}

void P2PGame::close() { impl_->close(); }

} // namespace lobbylink
