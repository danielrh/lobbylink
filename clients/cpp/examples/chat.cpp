// A tiny multiplayer chat, in one file — the "hello world" of lobbylink.
//
// Everyone who joins the same room with the same code is in the same
// chat. Type a line and press enter to broadcast it to everyone else;
// incoming lines print as they arrive. This is all it takes to wire N
// players together with direct peer-to-peer connections.
//
// Wire-compatible with the other clients' chat examples (e.g.
// clients/java/examples/Chat.java): run this against a Java or browser
// chat in the same room and type at each other.
//
// Build (from clients/cpp):
//   cmake -B build && cmake --build build
// Run (open two terminals with the same ROOM code):
//   ./build/chat https://pqrstuvw.xyz/lobbylink ROOM
// Ctrl-D quits cleanly, freeing the slot.

#include <lobbylink/lobbylink.hpp>

#include <iostream>
#include <string>
#include <thread>

using lobbylink::Event;

int main(int argc, char **argv) {
    if (argc < 3) {
        std::cerr << "usage: chat <server> <code>   "
                     "(e.g. chat https://pqrstuvw.xyz/lobbylink ROOM)\n";
        return 2;
    }

    // Create the room (up to 8 players) if we are the first one in;
    // otherwise join (the server treats join+create as join-or-create).
    lobbylink::ConnectOptions opts(argv[1], argv[2]);
    opts.create = lobbylink::CreateOptions(8);

    std::unique_ptr<lobbylink::P2PGame> game;
    try {
        game = lobbylink::P2PGame::connect(opts);
    } catch (const lobbylink::LobbyError &e) {
        std::cerr << "cannot join: " << e.what() << "\n";
        return 1;
    }
    std::cout << "You are player " << game->selfId() << " in room "
              << game->code() << "." << std::endl;
    std::cout << "Type a message and press enter. Ctrl-D to quit.\n" << std::endl;

    // One thread pumps events (people joining, messages arriving)...
    std::thread events([&game] {
        for (;;) {
            Event ev = game->nextEvent();
            switch (ev.type) {
            case Event::Type::Message:
                std::cout << "player " << ev.playerId << ": "
                          << std::string(ev.data.begin(), ev.data.end())
                          << std::endl;
                break;
            case Event::Type::PlayerJoined:
                std::cout << "* player " << ev.playerId << " joined" << std::endl;
                break;
            case Event::Type::PlayerLeft:
                std::cout << "* player " << ev.playerId << " left" << std::endl;
                break;
            case Event::Type::Closed:
                return;
            default:
                break;
            }
        }
    });

    // ...and the main thread reads your keystrokes and broadcasts them.
    std::string line;
    while (std::getline(std::cin, line)) {
        game->broadcastBestEffort(
            reinterpret_cast<const std::uint8_t *>(line.data()), line.size());
    }
    game->close();
    events.join();
    return 0;
}
