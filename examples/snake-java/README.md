# Snake — the original wildlifejava worm

This is anderrh's [wildlifejava](https://github.com/anderrh/wildlifejava) worm,
vendored here **unchanged** as the starting point for a lobbylink multiplayer
example. `Segment.java` is the whole game: a chain of segments that trail a lead
point (your mouse), drawn as hollow circles — run `Segment.main`.

BSD 2-Clause (see `LICENSE`), Copyright (c) 2026 anderrh.

## This commit does not compile on its own — on purpose

Upstream renders with Princeton's `StdDraw.java`, which is **GPLv3**. lobbylink is
permissively licensed, so we deliberately do **not** vendor `StdDraw.java`, and
without it `Segment.java` won't compile yet. This commit exists only to record
the exact original as the base of the diff that follows:

1. **this commit** — the untouched single-player worm (needs upstream StdDraw);
2. the next commit swaps the renderer to a small BSD `MiniDraw.java` so it builds
   and runs single-player with no GPL code;
3. the commit after that makes the worm multiplayer over lobbylink.
