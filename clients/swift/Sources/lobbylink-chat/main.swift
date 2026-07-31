// A tiny multiplayer chat, in one file — the "hello world" of lobbylink.
//
// Everyone who joins the same room with the same code is in the same
// chat. Type a line and press enter to broadcast it to everyone else;
// incoming lines print as they arrive. Wire-compatible with the other
// clients' chat examples (e.g. clients/java/examples/Chat.java) — a
// Swift chat and a Java chat can share one room.
//
// Build and run (open two terminals with the same ROOM code):
//   swift run lobbylink-chat https://pqrstuvw.xyz/lobbylink ROOM

import Foundation
import LobbyLink
#if canImport(Glibc)
import Glibc
#endif

// Line-buffer stdout so piped output (tests, tee) arrives promptly.
setvbuf(stdout, nil, _IOLBF, 0)

let args = CommandLine.arguments
guard args.count >= 3 else {
    FileHandle.standardError.write(Data(
        "usage: lobbylink-chat <server> <code>   (e.g. lobbylink-chat https://pqrstuvw.xyz/lobbylink ROOM)\n".utf8))
    exit(2)
}

// Create the room (up to 8 players) if we are the first one in;
// otherwise join.
var options = ConnectOptions(server: args[1], code: args[2])
options.create = CreateOptions(maxPlayers: 8)

let game: P2PGame
do {
    game = try P2PGame.connect(options)
} catch {
    FileHandle.standardError.write(Data("connect failed: \(error)\n".utf8))
    exit(1)
}
print("You are player \(game.selfId) in room \(game.code).")
print("Type a message and press enter. Ctrl-D to quit.\n")

// One thread pumps events (people joining, messages arriving)...
let events = Thread {
    while let ev = game.nextEvent() {
        switch ev {
        case .message(let from, _, let data):
            print("player \(from): \(String(decoding: data, as: UTF8.self))")
        case .playerJoined(let id):
            print("* player \(id) joined")
        case .playerLeft(let id, _):
            print("* player \(id) left")
        default:
            break
        }
    }
}
events.start()

// ...and the main thread reads your keystrokes and broadcasts them.
while let line = readLine(strippingNewline: true) {
    try? game.broadcastBestEffort(Data(line.utf8))
}
game.close()
