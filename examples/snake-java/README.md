# Snake — the wildlifejava worm (single-player, BSD renderer)

anderrh's [wildlifejava](https://github.com/anderrh/wildlifejava) worm, now
rendering with `MiniDraw.java` — a small, self-contained AWT drawing surface that
replaces the original's `StdDraw`, so the whole thing is permissively licensed and
depends on nothing but the JDK. **The worm itself is unchanged**;
only the handful of `StdDraw.*` calls in `Segment.main` became `MiniDraw` calls
(and `setCanvasSize`/`enableDoubleBuffering`/`setScale` folded into the MiniDraw
constructor).

```bash
javac Segment.java MiniDraw.java
java Segment
```

Move the mouse; the head follows and the body trails. Still single-player — the
next commit adds lobbylink multiplayer + interpolation.
