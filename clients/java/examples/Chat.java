// A tiny multiplayer chat, in one file — the "hello world" of lobbylink.
//
// Everyone who joins the same room with the same code is in the same chat.
// Type a line and press enter to broadcast it to everyone else; incoming lines
// print as they arrive. This is all it takes to wire N players together with
// direct peer-to-peer connections.
//
// Build (drop lobbylink-client-all.jar next to this file, or into a lib/ folder):
//   javac -cp lobbylink-client-all.jar Chat.java
// Run (open two terminals with the same ROOM code):
//   java -cp lobbylink-client-all.jar:. Chat https://pqrstuvw.xyz/lobbylink ROOM
// On Windows, use ';' instead of ':' in the classpath.

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;

import xyz.lobbylink.ConnectOptions;
import xyz.lobbylink.CreateOptions;
import xyz.lobbylink.Event;
import xyz.lobbylink.P2PGame;

public class Chat {
    public static void main(String[] args) throws Exception {
        if (args.length < 2) {
            System.err.println("usage: Chat <server> <code>   (e.g. Chat https://pqrstuvw.xyz/lobbylink ROOM)");
            System.exit(2);
        }
        String server = args[0];
        String code = args[1];

        // Create the room (up to 8 players) if we are the first one in; otherwise join.
        P2PGame game = P2PGame.connect(
                new ConnectOptions(server, code).create(new CreateOptions(8)));
        System.out.println("You are player " + game.selfId() + " in room " + game.code() + ".");
        System.out.println("Type a message and press enter. Ctrl-D to quit.\n");

        // One thread pumps events (people joining, messages arriving)...
        Thread events = new Thread(() -> {
            try {
                Event ev;
                while ((ev = game.nextEvent()) != null) {
                    if (ev instanceof Event.Message msg) {
                        System.out.println("player " + msg.from() + ": "
                                + new String(msg.data(), StandardCharsets.UTF_8));
                    } else if (ev instanceof Event.PlayerJoined pj) {
                        System.out.println("* player " + pj.playerId() + " joined");
                    } else if (ev instanceof Event.PlayerLeft pl) {
                        System.out.println("* player " + pl.playerId() + " left");
                    }
                }
            } catch (InterruptedException ignored) {
            }
        });
        events.setDaemon(true);
        events.start();

        // ...and the main thread reads your keystrokes and broadcasts them.
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8));
        String line;
        while ((line = in.readLine()) != null) {
            game.broadcastBestEffort(line.getBytes(StandardCharsets.UTF_8));
        }
        game.close();
    }
}
