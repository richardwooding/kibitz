//go:build js && wasm

// Command kibitz-wasm is the browser core. It owns everything below the DOM:
// WebSocket, wire codec, PAKE + group crypto, session engine, service mux,
// and game rules. The JS layer is a dumb view.
//
// The bridge is exactly two functions, JSON both ways:
//
//	window.kibitz_send(json)   — UI → core commands (installed here)
//	window.kibitzOnEvent(json) — core → UI events (defined by app.js)
//
// This package is the ONLY place syscall/js may be imported.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"syscall/js"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/battleship"
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/service/checkers"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/session"
)

// command is every UI→core message; unused fields stay empty.
type command struct {
	Type   string    `json:"type"`
	Phrase string    `json:"phrase,omitempty"`
	Text   string    `json:"text,omitempty"`
	UCI    string    `json:"uci,omitempty"`
	From   string    `json:"from,omitempty"`  // square, for chess.targets
	ID     int       `json:"id,omitempty"`    // request correlation for queries
	Hops   [][2]int8 `json:"hops,omitempty"`  // backgammon turn, player-relative
	Game   string    `json:"game,omitempty"`  // service ID for game.start
	Col    int8      `json:"col"`             // connect4 column
	Path   []int8    `json:"path,omitempty"`  // checkers move path
	Sq     int8      `json:"sq"`              // reversi square
	Cell   uint8     `json:"cell"`            // battleship cell
	Fleet  []uint8   `json:"fleet,omitempty"` // battleship placement
}

type app struct {
	mu     sync.Mutex
	client *session.Client
	chat   *chat.Service
	chess  *chess.Service
	bg     *backgammon.Service
	c4     *connect4.Service
	ck     *checkers.Service
	rv     *reversi.Service
	bs     *battleship.Service
}

var current app

func main() {
	js.Global().Set("kibitz_send", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 {
			return nil
		}
		go dispatch(args[0].String())
		return nil
	}))
	emit("core.ready", map[string]any{})
	select {} // the core lives as long as the page
}

func emit(typ string, fields map[string]any) {
	fields["type"] = typ
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	js.Global().Call("kibitzOnEvent", string(b))
}

func emitError(msg string) {
	emit("error", map[string]any{"message": msg})
}

// commands maps UI intents to actions. Handlers run on their own goroutine.
var commands = map[string]func(command){
	"create":     func(command) { create() },
	"join":       func(c command) { join(c.Phrase) },
	"leave":      func(command) { leave() },
	"game.start": func(c command) { startGame(c.Game) },

	"chat.say": func(c command) {
		withChat(func(s *chat.Service) error { return s.Say(c.Text) })
	},

	"chess.move":      func(c command) { withChess(func(s *chess.Service) error { return s.TryMove(c.UCI) }) },
	"chess.resign":    func(command) { withChess((*chess.Service).Resign) },
	"chess.offerDraw": func(command) { withChess((*chess.Service).OfferDraw) },
	"chess.agreeDraw": func(command) { withChess((*chess.Service).AgreeDraw) },
	"chess.targets":   func(c command) { targets(c.From, c.ID) },

	"bg.roll": func(command) { withBG((*backgammon.Service).Roll) },
	"bg.move": func(c command) {
		hops := make([]backgammon.Hop, len(c.Hops))
		for i, h := range c.Hops {
			hops[i] = backgammon.Hop{From: h[0], To: h[1]}
		}
		withBG(func(s *backgammon.Service) error { return s.Move(hops) })
	},
	"bg.resign": func(command) { withBG((*backgammon.Service).Resign) },

	"c4.drop":   func(c command) { withC4(func(s *connect4.Service) error { return s.Drop(c.Col) }) },
	"c4.resign": func(command) { withC4((*connect4.Service).Resign) },

	"checkers.move":      func(c command) { withCK(func(s *checkers.Service) error { return s.TryMove(c.Path) }) },
	"checkers.resign":    func(command) { withCK((*checkers.Service).Resign) },
	"checkers.offerDraw": func(command) { withCK((*checkers.Service).OfferDraw) },
	"checkers.agreeDraw": func(command) { withCK((*checkers.Service).AgreeDraw) },

	"reversi.place":  func(c command) { withRV(func(s *reversi.Service) error { return s.PlaceDisc(c.Sq) }) },
	"reversi.resign": func(command) { withRV((*reversi.Service).Resign) },

	"bs.commit": func(c command) {
		withBS(func(s *battleship.Service) error {
			if len(c.Fleet) != 100 {
				return fmt.Errorf("battleship: fleet must be 100 cells, got %d", len(c.Fleet))
			}
			var placement [100]uint8
			copy(placement[:], c.Fleet)
			return s.Commit(placement)
		})
	},
	"bs.shot":   func(c command) { withBS(func(s *battleship.Service) error { return s.Shoot(c.Cell) }) },
	"bs.resign": func(command) { withBS((*battleship.Service).Resign) },
}

func dispatch(raw string) {
	var cmd command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		emitError("bad command: " + err.Error())
		return
	}
	h, ok := commands[cmd.Type]
	if !ok {
		emitError("unknown command " + cmd.Type)
		return
	}
	h(cmd)
}

// relayURL derives ws(s)://<host>/ws from the page location, so the client
// always talks to the relay that served it.
func relayURL() string {
	loc := js.Global().Get("location")
	scheme := "ws"
	if loc.Get("protocol").String() == "https:" {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/ws", scheme, loc.Get("host").String())
}

func shareURL(phrase string) string {
	loc := js.Global().Get("location")
	return fmt.Sprintf("%s//%s/#%s", loc.Get("protocol").String(), loc.Get("host").String(), phrase)
}

func create() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, phrase, err := session.Host(ctx, relayURL())
	if err != nil {
		emitError("couldn't start a table: " + err.Error())
		return
	}
	start(client)

	url := shareURL(phrase)
	qrB64 := ""
	if png, err := qrcode.Encode(url, qrcode.Medium, 220); err == nil {
		qrB64 = base64.StdEncoding.EncodeToString(png)
	}
	emit("session.created", map[string]any{
		"phrase": phrase,
		"url":    url,
		"qr":     qrB64,
		"self":   uint32(client.Self()),
	})
}

func join(phrase string) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		emitError("enter a code phrase")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := session.Join(ctx, relayURL(), phrase)
	if err != nil {
		msg := "couldn't join: " + err.Error()
		if strings.Contains(err.Error(), "not found") {
			msg = "no table with that phrase — check for typos"
		} else if strings.Contains(err.Error(), "unwrap") {
			msg = "wrong phrase"
		}
		emitError(msg)
		return
	}
	start(client)
	emit("session.joined", map[string]any{
		"self": uint32(client.Self()),
		"role": roleName(client.Role()),
	})
}

// start attaches services and begins pumping mux events to the UI.
func start(client *session.Client) {
	ch := chat.New()
	cs := chess.New()
	bg := backgammon.New()
	c4 := connect4.New()
	ck := checkers.New()
	rv := reversi.New()
	bs := battleship.New()
	mux := service.NewMux(client, ch, cs, bg, c4, ck, rv, bs)

	current.mu.Lock()
	if current.client != nil {
		_ = current.client.Close()
	}
	current.client, current.chat, current.chess = client, ch, cs
	current.bg, current.c4, current.ck, current.rv, current.bs = bg, c4, ck, rv, bs
	current.mu.Unlock()

	go pump(mux)
}

// startGame launches (or rematches) a game by service ID.
func startGame(id string) {
	current.mu.Lock()
	starters := map[string]func() error{}
	if current.chess != nil {
		starters[chess.ID] = current.chess.Start
	}
	if current.bg != nil {
		starters[backgammon.ID] = current.bg.Start
	}
	if current.c4 != nil {
		starters[connect4.ID] = current.c4.Start
	}
	if current.ck != nil {
		starters[checkers.ID] = current.ck.Start
	}
	if current.rv != nil {
		starters[reversi.ID] = current.rv.Start
	}
	if current.bs != nil {
		starters[battleship.ID] = current.bs.Start
	}
	startFn, ok := starters[id]
	current.mu.Unlock()
	if !ok {
		emitError("unknown game " + id)
		return
	}
	if err := startFn(); err != nil {
		emitError(err.Error())
	}
}

func pump(mux *service.Mux) {
	for ev := range mux.Events() {
		switch e := ev.(type) {
		case service.Roster:
			emitRoster(e)
		case chat.Message:
			emit("chat.msg", map[string]any{"from": uint32(e.From), "text": e.Text})
		case chess.State:
			emitChessState(e)
		case chess.DrawOffered:
			emit("chess.drawOffered", map[string]any{"from": uint32(e.From)})
		case chess.Desync:
			emitError("game desynchronized: " + e.Reason)
		case backgammon.State:
			emitBGState(e)
		case backgammon.Danced:
			emit("bg.danced", map[string]any{"by": uint32(e.By)})
		case backgammon.CheatDetected:
			emitError(fmt.Sprintf("dice cheat detected from participant %d — game voided", e.By))
		case connect4.State:
			emitC4State(e)
		case checkers.State:
			emitCKState(e)
		case checkers.DrawOffered:
			emit("checkers.drawOffered", map[string]any{"from": uint32(e.From)})
		case reversi.State:
			emitRVState(e)
		case battleship.State:
			emitBSState(e)
		case battleship.CheatDetected:
			emitError(fmt.Sprintf("battleship: cheating detected from participant %d — game voided", e.By))
		case service.ServiceError:
			emitError(fmt.Sprintf("%s: %v", e.Service, e.Err))
		case service.SessionEvent:
			if closed, ok := e.Event.(session.Closed); ok {
				emit("session.closed", map[string]any{"reason": closed.Reason})
				return
			}
		}
	}
}

func emitRoster(e service.Roster) {
	members := map[string]string{}
	for id, role := range e.Members {
		members[fmt.Sprint(uint32(id))] = roleName(role)
	}
	emit("roster", map[string]any{"members": members})
}

func emitChessState(e chess.State) {
	emit("chess.state", map[string]any{
		"fen": e.FEN, "whiteId": uint32(e.WhiteID), "blackId": uint32(e.BlackID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome, "method": e.Method,
		"lastUci": e.LastUCI, "playing": e.Playing,
	})
}

func emitBGState(e backgammon.State) {
	legal := make([][][2]int8, len(e.Legal))
	for i, turn := range e.Legal {
		legal[i] = make([][2]int8, len(turn))
		for j, h := range turn {
			legal[i][j] = [2]int8{h.From, h.To}
		}
	}
	emit("bg.state", map[string]any{
		"points": e.Board.Points[:], "barW": e.Board.Bar[backgammon.White],
		"barB": e.Board.Bar[backgammon.Black], "offW": e.Board.Off[backgammon.White],
		"offB":    e.Board.Off[backgammon.Black],
		"whiteId": uint32(e.WhiteID), "blackId": uint32(e.BlackID),
		"turnId": uint32(e.TurnID), "phase": e.Phase,
		"dice": []int8{e.Dice[0], e.Dice[1]}, "legal": legal,
		"outcome": e.Outcome, "pipsW": e.PipsW, "pipsB": e.PipsB,
		"playing": e.Playing,
	})
}

func emitCKState(e checkers.State) {
	legal := make([][]int8, len(e.Legal))
	for i, m := range e.Legal {
		legal[i] = []int8(m)
	}
	emit("checkers.state", map[string]any{
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"legal": legal, "lastPath": e.LastPath, "playing": e.Playing,
	})
}

func emitBSState(e battleship.State) {
	emit("battleship.state", map[string]any{
		"phase": e.Phase, "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "myFleet": e.MyFleet[:],
		"committed": e.Committed[:],
		"reveals":   [][]int8{e.Reveals[0][:], e.Reveals[1][:]},
		"sunk":      [][]uint8{orEmpty(e.Sunk[0]), orEmpty(e.Sunk[1])},
		"outcome":   e.Outcome, "cheatBy": uint32(e.CheatBy), "playing": e.Playing,
	})
}

func orEmpty(v []uint8) []uint8 {
	if v == nil {
		return []uint8{}
	}
	return v
}

func emitRVState(e reversi.State) {
	emit("reversi.state", map[string]any{
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome, "legal": e.Legal,
		"passed": e.Passed, "black": e.Black, "white": e.White,
		"lastSq": e.LastSq, "playing": e.Playing,
	})
}

func emitC4State(e connect4.State) {
	emit("connect4.state", map[string]any{
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"winCells": e.WinCells, "lastCol": e.LastCol, "playing": e.Playing,
	})
}

func targets(from string, id int) {
	current.mu.Lock()
	cs := current.chess
	current.mu.Unlock()
	if cs == nil {
		return
	}
	list := cs.LegalTargets(from)
	if list == nil {
		list = []string{}
	}
	emit("chess.targets", map[string]any{"from": from, "targets": list, "id": id})
}

func leave() {
	current.mu.Lock()
	c := current.client
	current.client, current.chat, current.chess, current.bg, current.c4 = nil, nil, nil, nil, nil
	current.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func withChat(f func(*chat.Service) error) {
	current.mu.Lock()
	s := current.chat
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withChess(f func(*chess.Service) error) {
	current.mu.Lock()
	s := current.chess
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withBG(f func(*backgammon.Service) error) {
	current.mu.Lock()
	s := current.bg
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withC4(f func(*connect4.Service) error) {
	current.mu.Lock()
	s := current.c4
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withCK(f func(*checkers.Service) error) {
	current.mu.Lock()
	s := current.ck
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withRV(f func(*reversi.Service) error) {
	current.mu.Lock()
	s := current.rv
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withBS(f func(*battleship.Service) error) {
	current.mu.Lock()
	s := current.bs
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func callService(missing bool, f func() error) {
	if missing {
		emitError("not in a session")
		return
	}
	if err := f(); err != nil {
		emitError(err.Error())
	}
}

func roleName(r session.Role) string {
	switch r {
	case session.RoleHost:
		return "host"
	case session.RolePlayer:
		return "player"
	case session.RoleSpectator:
		return "spectator"
	default:
		return "unknown"
	}
}
