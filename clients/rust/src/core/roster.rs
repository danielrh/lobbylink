use super::error::LobbyError;
use super::protocol::WirePlayer;

/// Player ids are slot indices 0..maxPlayers.
pub type PlayerId = u16;

/// Public snapshot of one room slot.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PlayerInfo {
    pub id: PlayerId,
    pub occupied: bool,
    pub connected: bool,
}

/// Build the full roster (one entry per slot, id == index) from a
/// server snapshot.
pub fn roster_from_snapshot(max_players: u16, players: &[WirePlayer]) -> Vec<PlayerInfo> {
    let mut roster: Vec<PlayerInfo> = (0..max_players)
        .map(|id| PlayerInfo { id, occupied: false, connected: false })
        .collect();
    for p in players {
        if let Some(slot) = roster.get_mut(p.id as usize) {
            slot.occupied = p.occupied;
            slot.connected = p.connected;
        }
    }
    roster
}

/// Validate a send target (shared by both backends).
pub fn check_target(to: PlayerId, self_id: PlayerId, max_players: u16) -> Result<(), LobbyError> {
    if to >= max_players {
        return Err(LobbyError::new(
            "invalid-target",
            format!("player id {to} out of range 0..{}", max_players.saturating_sub(1)),
        ));
    }
    if to == self_id {
        return Err(LobbyError::new("invalid-target", "cannot send to yourself"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snapshot_fills_all_slots() {
        let wire = vec![
            WirePlayer { id: 1, occupied: true, connected: false },
        ];
        let roster = roster_from_snapshot(3, &wire);
        assert_eq!(roster.len(), 3);
        assert!(!roster[0].occupied);
        assert!(roster[1].occupied && !roster[1].connected);
        assert_eq!(roster[2].id, 2);
    }

    #[test]
    fn target_checks() {
        assert!(check_target(1, 0, 4).is_ok());
        assert_eq!(check_target(4, 0, 4).unwrap_err().code, "invalid-target");
        assert_eq!(check_target(0, 0, 4).unwrap_err().code, "invalid-target");
    }
}
