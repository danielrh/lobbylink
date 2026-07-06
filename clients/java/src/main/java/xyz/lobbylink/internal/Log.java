package xyz.lobbylink.internal;

/** Internal warnings. Off by default; enable via ConnectOptions.verbose(true). */
public final class Log {
    private Log() {}

    private static volatile boolean verbose =
            Boolean.parseBoolean(System.getProperty("lobbylink.verbose", "false"));

    public static void setVerbose(boolean v) {
        verbose = v;
    }

    public static void warn(String msg) {
        if (verbose) {
            System.err.println("[lobbylink] " + msg);
        }
    }
}
