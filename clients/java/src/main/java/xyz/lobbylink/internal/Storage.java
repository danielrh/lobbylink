package xyz.lobbylink.internal;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

/**
 * Best-effort resume-token persistence to a file. Storage failures just mean
 * resume won't work, mirroring the other clients.
 */
public final class Storage {
    private Storage() {}

    public static String load(Path path) {
        if (path == null) return null;
        try {
            String token = Files.readString(path, StandardCharsets.UTF_8).trim();
            return token.isEmpty() ? null : token;
        } catch (Exception e) {
            return null;
        }
    }

    public static void save(Path path, String token) {
        if (path == null) return;
        try {
            Files.writeString(path, token, StandardCharsets.UTF_8);
        } catch (Exception ignore) {
            // best effort
        }
    }

    public static void clear(Path path) {
        if (path == null) return;
        try {
            Files.deleteIfExists(path);
        } catch (Exception ignore) {
            // best effort
        }
    }
}
