package xyz.lobbylink;

/**
 * Public snapshot of one room slot. Player ids are slot indices
 * {@code 0..maxPlayers}.
 *
 * @param id        slot index / player id
 * @param occupied  a player currently holds this slot
 * @param connected that player's signaling is currently up (a P2P DataChannel
 *                  may still be alive even when this is false)
 */
public record PlayerInfo(int id, boolean occupied, boolean connected) {
}
