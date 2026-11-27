// Loopback interop self-test: two P2PGame instances in one process
// against a local lobbylink server. Asserts the full contract surface:
// create+join slot assignment, peer connection, reliable single- and
// multi-chunk messages both directions, a best-effort burst, and an
// explicit leave freeing the slot.
//
// Run a server first, e.g.:
//   go build -o /tmp/server ./cmd/p2p-lobby-server
//   /tmp/server --listen-http 127.0.0.1:8787
//     --allowed-origin http://127.0.0.1:8787 --public-url http://127.0.0.1:8787
// then:
//   ./interop http://127.0.0.1:8787          (or env LOBBYLINK_SERVER)
//
// Exit codes: 0 = pass, 1 = fail, 77 = skipped (no server configured).

#include <lobbylink/lobbylink.hpp>

#include <chrono>
#include <cstdlib>
#include <iostream>
#include <random>
#include <string>
#include <vector>

using namespace lobbylink;
using Clock = std::chrono::steady_clock;

namespace {

[[noreturn]] void fail(const std::string &msg) {
    std::cerr << "FAIL: " << msg << std::endl;
    std::exit(1);
}

void check(bool ok, const std::string &what) {
    if (!ok)
        fail(what);
    std::cout << "ok: " << what << std::endl;
}

// Drains events until one matches pred; unrelated events are discarded.
template <typename Pred>
Event waitEvent(P2PGame &game, Pred pred, const std::string &what,
                std::chrono::seconds timeout = std::chrono::seconds(20)) {
    const auto deadline = Clock::now() + timeout;
    while (Clock::now() < deadline) {
        auto remaining = std::chrono::duration_cast<std::chrono::milliseconds>(
            deadline - Clock::now());
        auto ev = game.nextEvent(std::max(remaining, std::chrono::milliseconds(1)));
        if (!ev)
            break;
        if (pred(*ev))
            return *ev;
        if (ev->type == Event::Type::Closed)
            break;
    }
    fail("timed out waiting for " + what);
}

std::string randomCode() {
    std::mt19937_64 rng(std::random_device{}());
    static const char cs[] = "abcdefghijklmnopqrstuvwxyz0123456789";
    std::string code = "CPPINTEROP-";
    for (int i = 0; i < 10; i++)
        code += cs[rng() % (sizeof(cs) - 1)];
    return code;
}

std::vector<std::uint8_t> bytesOf(const std::string &s) {
    return std::vector<std::uint8_t>(s.begin(), s.end());
}

} // namespace

int main(int argc, char **argv) {
    std::string server;
    if (argc > 1)
        server = argv[1];
    else if (const char *env = std::getenv("LOBBYLINK_SERVER"))
        server = env;
    if (server.empty()) {
        std::cerr << "SKIP: no server (pass a URL or set LOBBYLINK_SERVER)"
                  << std::endl;
        return 77;
    }
    const std::string code = randomCode();
    std::cout << "server " << server << ", room " << code << std::endl;

    // -- create + join: slots 0 and 1 ------------------------------------
    ConnectOptions optsA(server, code);
    optsA.create = CreateOptions(2);
    auto a = P2PGame::connect(optsA);
    check(a->selfId() == 0, "creator got selfId 0");
    check(a->maxPlayers() == 2, "maxPlayers is 2");
    check(a->code() == code, "room code echoes back");
    check(!a->resumeToken().empty(), "resume token issued");

    auto b = P2PGame::connect(ConnectOptions(server, code)); // plain join
    check(b->selfId() == 1, "joiner got selfId 1");

    // -- both sides reach peer state "connected" -------------------------
    waitEvent(*a, [](const Event &ev) {
        return ev.type == Event::Type::PeerState && ev.playerId == 1 &&
               ev.state == "connected";
    }, "A: peer 1 connected");
    waitEvent(*b, [](const Event &ev) {
        return ev.type == Event::Type::PeerState && ev.playerId == 0 &&
               ev.state == "connected";
    }, "B: peer 0 connected");
    check(true, "both peers connected");

    // -- reliable, single chunk, both directions -------------------------
    const auto helloAB = bytesOf("hello from A");
    a->sendReliable(1, helloAB);
    auto ev = waitEvent(*b, [](const Event &e) {
        return e.type == Event::Type::Message && e.kind == MessageKind::Reliable;
    }, "B: reliable message from A");
    check(ev.playerId == 0 && ev.data == helloAB, "reliable A->B intact");

    const auto helloBA = bytesOf("hello from B");
    b->sendReliable(0, helloBA);
    ev = waitEvent(*a, [](const Event &e) {
        return e.type == Event::Type::Message && e.kind == MessageKind::Reliable;
    }, "A: reliable message from B");
    check(ev.playerId == 1 && ev.data == helloBA, "reliable B->A intact");

    // -- reliable, multi-chunk (100 KiB spans 7 frames) ------------------
    std::vector<std::uint8_t> big(100 * 1024);
    for (std::size_t i = 0; i < big.size(); i++)
        big[i] = std::uint8_t((i * 31 + 7) & 0xff);
    a->sendReliable(1, big);
    ev = waitEvent(*b, [](const Event &e) {
        return e.type == Event::Type::Message && e.kind == MessageKind::Reliable;
    }, "B: 100 KiB reliable message");
    check(ev.data == big, "100 KiB multi-chunk message intact");

    // -- best-effort burst (loopback: effectively lossless) --------------
    const int burst = 20;
    for (int i = 0; i < burst; i++)
        b->sendBestEffort(0, bytesOf("be-" + std::to_string(i)));
    int got = 0;
    while (got < burst) {
        ev = waitEvent(*a, [](const Event &e) {
            return e.type == Event::Type::Message &&
                   e.kind == MessageKind::BestEffort;
        }, "A: best-effort datagram " + std::to_string(got));
        check(ev.playerId == 1, "best-effort datagram from B");
        got++;
    }
    check(got == burst, "best-effort burst of " + std::to_string(burst) +
                            " datagrams arrived");

    // -- explicit close frees the slot -----------------------------------
    a->close();
    ev = waitEvent(*b, [](const Event &e) {
        return e.type == Event::Type::PlayerLeft;
    }, "B: player-left after A closes");
    check(ev.playerId == 0 && ev.reason == "explicit-leave",
          "player-left reason is explicit-leave");
    auto players = b->players();
    check(players.size() == 2 && !players[0].occupied,
          "slot 0 freed after explicit leave");

    // A's stream ends after close: drains, then Closed.
    for (;;) {
        auto last = a->nextEvent(std::chrono::seconds(5));
        if (!last)
            fail("A: no Closed event after close()");
        if (last->type == Event::Type::Closed)
            break;
    }
    check(true, "A: event stream ended with Closed");

    b->close();
    std::cout << "PASS" << std::endl;
    return 0;
}
