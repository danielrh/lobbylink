// Echo player: joins a room and sends every message it receives straight back
// to its sender on the same channel kind. The simplest possible peer.
//
//   javac -cp lobbylink-client-all.jar EchoGame.java
//   java  -cp lobbylink-client-all.jar:. EchoGame https://pqrstuvw.xyz/lobbylink ROOM --create 2
//
// (On Windows use ';' instead of ':' in the classpath.)

import xyz.lobbylink.ConnectOptions;
import xyz.lobbylink.CreateOptions;
import xyz.lobbylink.Event;
import xyz.lobbylink.MessageKind;
import xyz.lobbylink.P2PGame;

public class EchoGame {
    public static void main(String[] args) throws Exception {
        if (args.length < 2) {
            System.err.println("usage: EchoGame <server> <code> [--create N]");
            System.exit(2);
        }
        ConnectOptions opts = new ConnectOptions(args[0], args[1]);
        for (int i = 2; i < args.length; i++) {
            if (args[i].equals("--create") && i + 1 < args.length) {
                opts.create(new CreateOptions(Integer.parseInt(args[++i])));
            }
        }

        P2PGame game = P2PGame.connect(opts);
        System.out.println("echoing in room " + game.code() + " as player " + game.selfId());

        Event ev;
        while ((ev = game.nextEvent()) != null) {
            if (ev instanceof Event.Message msg) {
                if (msg.kind() == MessageKind.RELIABLE) {
                    System.out.println("echoing " + msg.data().length + " reliable bytes to player " + msg.from());
                    game.sendReliable(msg.from(), msg.data());
                } else {
                    game.sendBestEffort(msg.from(), msg.data());
                }
            } else {
                System.out.println(ev);
            }
        }
        game.close();
    }
}
