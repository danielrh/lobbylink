// Console tetris on lobbylink: single player out of the box, and with
// a server + room code every line you clear bumps gray garbage rows
// into every opponent's board (and theirs into yours).
//
//	tetris                                  # single player
//	tetris https://host/lobbylink MYROOM    # multiplayer (join or create)
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/user"
	"time"

	lobbylink "github.com/danielrh/lobbylink/clients/go"
	"golang.org/x/term"
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: tetris [flags] [server code]

  tetris                                    single player
  tetris https://host/lobbylink MYROOM      multiplayer: join MYROOM,
                                            creating it if absent

flags:
`)
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	name := flag.String("name", "", "display name shown to opponents (default: OS user)")
	players := flag.Int("players", 4, "room size when creating the room")
	relay := flag.Bool("relay", false, "force TURN relay (for testing)")
	flag.Usage = usage
	flag.Parse()

	if *name == "" {
		if u, err := user.Current(); err == nil {
			*name = u.Username
		} else {
			*name = "anon"
		}
	}

	var game *lobbylink.Game
	switch flag.NArg() {
	case 0:
	case 2:
		var err error
		game, err = lobbylink.Connect(context.Background(), lobbylink.Options{
			Server:     flag.Arg(0),
			Code:       flag.Arg(1),
			Create:     lobbylink.NewCreateOptions(*players),
			ForceRelay: *relay,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot join room:", err)
			os.Exit(1)
		}
	default:
		usage()
	}

	// Raw mode when stdin is a terminal; piped stdin (tests) still works.
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, old)
		}
	}
	os.Stdout.WriteString(ansiClear + ansiHideCur)
	defer os.Stdout.WriteString(ansiShowCur + ansiReset + "\r\n")

	run(game, *name)
	if game != nil {
		game.Close()
	}
}

type key int

const (
	keyLeft key = iota
	keyRight
	keyDown
	keyRotate
	keyDrop
	keyPause
	keyRestart
	keyQuit
)

// readKeys turns raw stdin bytes (arrows arrive as ESC [ A..D) into
// key events. The channel closes on stdin EOF.
func readKeys(ch chan<- key) {
	defer close(ch)
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return
		}
		for i := 0; i < n; i++ {
			var k key
			switch buf[i] {
			case 0x1b:
				if i+2 < n && buf[i+1] == '[' {
					switch buf[i+2] {
					case 'A':
						k = keyRotate
					case 'B':
						k = keyDown
					case 'C':
						k = keyRight
					case 'D':
						k = keyLeft
					default:
						i += 2
						continue
					}
					i += 2
				} else {
					continue
				}
			case 'a', 'h':
				k = keyLeft
			case 'd', 'l':
				k = keyRight
			case 's', 'j':
				k = keyDown
			case 'w', 'k', 'x':
				k = keyRotate
			case ' ':
				k = keyDrop
			case 'p':
				k = keyPause
			case 'r':
				k = keyRestart
			case 'q', 3, 4: // q, Ctrl-C, Ctrl-D
				k = keyQuit
			default:
				continue
			}
			ch <- k
		}
	}
}

func gravityInterval(level int) time.Duration {
	ms := 1000 - 90*level
	if ms < 100 {
		ms = 100
	}
	return time.Duration(ms) * time.Millisecond
}

func run(game *lobbylink.Game, name string) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rs := &renderState{
		t:         newTetris(rng),
		name:      name,
		opponents: map[int]*opponent{},
	}
	var events <-chan lobbylink.Event // nil in single player: blocks forever
	if game != nil {
		rs.room = game.Code()
		rs.selfID = game.SelfID()
		events = game.Events()
		for _, p := range game.Players() {
			if p.ID != game.SelfID() && p.Occupied {
				rs.opponents[p.ID] = &opponent{present: p.Connected}
			}
		}
	}

	keys := make(chan key, 16)
	go readKeys(keys)

	gravity := time.NewTimer(gravityInterval(0))
	defer gravity.Stop()
	stateTick := time.NewTicker(200 * time.Millisecond)
	defer stateTick.Stop()

	broadcast := func() {
		if game == nil {
			return
		}
		board := snapshotBoard(rs.t)
		msg := encodeState(&stateMsg{
			alive: !rs.t.over,
			score: rs.t.score,
			lines: uint16(rs.t.lines),
			level: uint8(rs.t.level()),
			board: board,
			name:  name,
		})
		_ = game.BroadcastBestEffort(msg)
	}

	// afterLock handles the piece having settled: in multiplayer a
	// clear bumps every opponent by that many garbage rows.
	afterLock := func() {
		if n := rs.t.linesCleared; n > 0 && game != nil {
			attack := encodeAttack(n)
			for _, p := range game.Players() {
				if p.ID != game.SelfID() && p.Occupied {
					go game.SendReliable(p.ID, attack) //nolint:errcheck // opponent may be gone
				}
			}
			rs.status = fmt.Sprintf("cleared %d — bumped everyone %d rows!", n, n)
		}
		rs.t.linesCleared = 0
		broadcast()
	}

	draw := func() { os.Stdout.WriteString(render(rs)) }
	broadcast()
	draw()

	for {
		select {
		case k, ok := <-keys:
			if !ok {
				keys = nil // piped stdin ended; keep playing/watching
				continue
			}
			t := rs.t
			switch k {
			case keyQuit:
				return
			case keyPause:
				rs.paused = !rs.paused
			case keyRestart:
				if t.over {
					rs.t = newTetris(rng)
					rs.status = ""
					broadcast()
				}
			case keyLeft:
				if !t.over && !rs.paused {
					t.move(-1, 0)
				}
			case keyRight:
				if !t.over && !rs.paused {
					t.move(1, 0)
				}
			case keyRotate:
				if !t.over && !rs.paused {
					t.rotate()
				}
			case keyDown:
				if !t.over && !rs.paused {
					if t.move(0, 1) {
						t.score++
					} else if t.stepDown() {
						afterLock()
					}
				}
			case keyDrop:
				if !t.over && !rs.paused {
					t.hardDrop()
					afterLock()
				}
			}

		case <-gravity.C:
			if !rs.t.over && !rs.paused {
				if rs.t.stepDown() {
					afterLock()
				}
			}
			gravity.Reset(gravityInterval(rs.t.level()))

		case <-stateTick.C:
			broadcast()

		case ev, ok := <-events:
			if !ok {
				events = nil
				rs.status = "connection closed"
				continue
			}
			switch ev := ev.(type) {
			case lobbylink.MessageEvent:
				if st, ok := decodeState(ev.Data); ok {
					op := rs.opponents[ev.From]
					if op == nil {
						op = &opponent{}
						rs.opponents[ev.From] = op
					}
					op.present = true
					op.state = st
				} else if n, ok := decodeAttack(ev.Data); ok {
					rs.t.addGarbage(n)
					who := fmt.Sprintf("player %d", ev.From)
					if op := rs.opponents[ev.From]; op != nil && op.state.name != "" {
						who = op.state.name
					}
					rs.status = fmt.Sprintf("%s bumped you up %d rows!", who, n)
					broadcast()
				}
			case lobbylink.PlayerJoinedEvent:
				rs.opponents[ev.PlayerID] = &opponent{present: true}
			case lobbylink.PlayerLeftEvent:
				if ev.Reason == "explicit-leave" {
					delete(rs.opponents, ev.PlayerID)
				} else if op := rs.opponents[ev.PlayerID]; op != nil {
					op.present = false
				}
			case lobbylink.PlayerRejoinedEvent:
				rs.opponents[ev.PlayerID] = &opponent{present: true}
			case lobbylink.PlayerReplacedEvent:
				rs.opponents[ev.PlayerID] = &opponent{present: true}
			case lobbylink.PeerStateEvent:
				if ev.State == "connected" {
					// Fresh link: make sure they see us right away.
					broadcast()
				}
			case lobbylink.SignalingClosedEvent:
				rs.status = "signaling lost (" + ev.Code + ") — existing peers stay up"
			case lobbylink.LobbyErrorEvent:
				rs.status = "lobby error: " + ev.Code
			}
		}
		draw()
	}
}
