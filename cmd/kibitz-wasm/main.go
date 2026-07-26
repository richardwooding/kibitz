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

	"github.com/richardwooding/kibitz/internal/bot"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/battleship"
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/service/checkers"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/service/connect4"
	"github.com/richardwooding/kibitz/internal/service/dots"
	"github.com/richardwooding/kibitz/internal/service/gin"
	"github.com/richardwooding/kibitz/internal/service/gomoku"
	"github.com/richardwooding/kibitz/internal/service/gomokup"
	"github.com/richardwooding/kibitz/internal/service/hex"
	"github.com/richardwooding/kibitz/internal/service/reversi"
	"github.com/richardwooding/kibitz/internal/service/weiqi"
	"github.com/richardwooding/kibitz/internal/service/xiangqi"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/solo"
)

// command is every UI→core message; unused fields stay empty.
type command struct {
	Type     string    `json:"type"`
	Phrase   string    `json:"phrase,omitempty"`
	Text     string    `json:"text,omitempty"`
	UCI      string    `json:"uci,omitempty"`
	From     string    `json:"from,omitempty"`     // square, for chess.targets
	ID       int       `json:"id,omitempty"`       // request correlation for queries
	Hops     [][2]int8 `json:"hops,omitempty"`     // backgammon turn, player-relative
	Game     string    `json:"game,omitempty"`     // service ID for game.start
	Col      int8      `json:"col"`                // connect4 column
	Path     []int8    `json:"path,omitempty"`     // checkers move path
	Row      int8      `json:"row"`                // gomoku/hex/weiqi row
	Edge     int8      `json:"edge"`               // dots edge id
	Frm      int8      `json:"frm"`                // xiangqi from-square
	To       int8      `json:"to"`                 // xiangqi to-square
	GinCard  int8      `json:"ginCard"`            // gin discard/knock card
	Sq       int8      `json:"sq"`                 // reversi square
	Cell     uint8     `json:"cell"`               // battleship cell
	Fleet    []uint8   `json:"fleet,omitempty"`    // battleship placement
	Name     string    `json:"name,omitempty"`     // screen name for create/join
	Mode     string    `json:"mode,omitempty"`     // solo mode: "bot" | "hotseat"
	Level    string    `json:"level,omitempty"`    // solo bot difficulty: "easy" | "hard"
	PushKey  string    `json:"pushKey,omitempty"`  // host: shared session VAPID keypair blob
	Endpoint string    `json:"endpoint,omitempty"` // this client's Web Push endpoint
	Spectate bool      `json:"spectate,omitempty"` // join intent: watch instead of play
}

type app struct {
	mu     sync.Mutex
	gen    int          // bumped per session start; lets a superseded pump exit quietly
	mux    *service.Mux // the networked mux (nil for solo); closed on teardown
	client *session.Client
	chat   *chat.Service
	chess  *chess.Service
	bg     *backgammon.Service
	c4     *connect4.Service
	gm     *gomoku.Service
	hx     *hex.Service
	dt     *dots.Service
	wq     *weiqi.Service
	xq     *xiangqi.Service
	gn     *gin.Service
	gp     *gomokup.Service // Gomoku Party (2–4 players) — networked-only, no solo twin
	ck     *checkers.Service
	rv     *reversi.Service
	bs     *battleship.Service

	// Solo hot-seat: a relay-free loopback runs two ends in one browser. The
	// fields above are end A (host) — the set the UI reads and control actions
	// use. The *B fields are end B (the synthetic opponent). Turn-gated moves
	// try end A, then end B (exactly one is on turn). See internal/solo.
	solo                bool
	soloHost, soloGuest *solo.Endpoint
	chatB               *chat.Service
	chessB              *chess.Service
	bgB                 *backgammon.Service
	c4B                 *connect4.Service
	gmB                 *gomoku.Service
	hxB                 *hex.Service
	dtB                 *dots.Service
	wqB                 *weiqi.Service
	xqB                 *xiangqi.Service
	gnB                 *gin.Service
	ckB                 *checkers.Service
	rvB                 *reversi.Service
	bsB                 *battleship.Service
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
	"create":     func(c command) { create(c.Name) },
	"join":       func(c command) { join(c.Phrase, c.Name, c.Spectate) },
	"solo":       func(c command) { startSolo(c.Name, c.Mode == "bot", c.Level) },
	"leave":      func(command) { leave() },
	"game.start": func(c command) { startGame(c.Game) },

	// Turn-notification plumbing (networked only): the host shares the session
	// VAPID keypair; each client shares its Web Push endpoint. Both ride the
	// encrypted ctl channel — the relay never sees them.
	"push.key":      func(c command) { withMux(func(m *service.Mux) { m.SetPushKey(c.PushKey) }) },
	"push.endpoint": func(c command) { withMux(func(m *service.Mux) { m.SetEndpoint(c.Endpoint) }) },

	"chat.say": func(c command) {
		withChat(func(s *chat.Service) error { return s.Say(c.Text) })
	},

	"chess.move":      func(c command) { moveChess(func(s *chess.Service) error { return s.TryMove(c.UCI) }) },
	"chess.resign":    func(command) { withChess((*chess.Service).Resign) },
	"chess.offerDraw": func(command) { withChess((*chess.Service).OfferDraw) },
	"chess.agreeDraw": func(command) { withChess((*chess.Service).AgreeDraw) },
	"chess.targets":   func(c command) { targets(c.From, c.ID) },

	"bg.roll": func(command) { moveBG((*backgammon.Service).Roll) },
	"bg.move": func(c command) {
		hops := make([]backgammon.Hop, len(c.Hops))
		for i, h := range c.Hops {
			hops[i] = backgammon.Hop{From: h[0], To: h[1]}
		}
		moveBG(func(s *backgammon.Service) error { return s.Move(hops) })
	},
	"bg.resign": func(command) { withBG((*backgammon.Service).Resign) },

	"c4.drop":   func(c command) { moveC4(func(s *connect4.Service) error { return s.Drop(c.Col) }) },
	"c4.resign": func(command) { withC4((*connect4.Service).Resign) },

	"gomoku.place":  func(c command) { moveGM(func(s *gomoku.Service) error { return s.Place(c.Row, c.Col) }) },
	"gomoku.resign": func(command) { withGM((*gomoku.Service).Resign) },

	"gomokup.place":  func(c command) { moveGP(func(s *gomokup.Service) error { return s.Place(c.Row, c.Col) }) },
	"gomokup.resign": func(command) { withGP((*gomokup.Service).Resign) },

	"hex.place":  func(c command) { moveHex(func(s *hex.Service) error { return s.Place(c.Row, c.Col) }) },
	"hex.resign": func(command) { withHex((*hex.Service).Resign) },

	"dots.draw":   func(c command) { moveDots(func(s *dots.Service) error { return s.DrawEdge(c.Edge) }) },
	"dots.resign": func(command) { withDots((*dots.Service).Resign) },

	"weiqi.place":  func(c command) { moveWeiqi(func(s *weiqi.Service) error { return s.Place(c.Row, c.Col) }) },
	"weiqi.pass":   func(command) { withWeiqi((*weiqi.Service).Pass) },
	"weiqi.resign": func(command) { withWeiqi((*weiqi.Service).Resign) },

	"xiangqi.move":   func(c command) { moveXiangqi(func(s *xiangqi.Service) error { return s.Move(c.Frm, c.To) }) },
	"xiangqi.resign": func(command) { withXiangqi((*xiangqi.Service).Resign) },

	"gin.drawStock":       func(command) { withGin((*gin.Service).DrawStock) },
	"gin.takeUpcard":      func(command) { withGin((*gin.Service).TakeUpcard) },
	"gin.takeUpcardOffer": func(command) { withGin((*gin.Service).TakeUpcardOffer) },
	"gin.passUpcard":      func(command) { withGin((*gin.Service).PassUpcard) },
	"gin.discard":         func(c command) { withGin(func(s *gin.Service) error { return s.Discard(c.GinCard) }) },
	"gin.knock":           func(c command) { withGin(func(s *gin.Service) error { return s.Knock(c.GinCard) }) },
	"gin.resign":          func(command) { withGin((*gin.Service).Resign) },

	// Takeback (1-level undo of the last move) for every deterministic game.
	"connect4.offerTakeback":  func(command) { withC4((*connect4.Service).OfferTakeback) },
	"connect4.acceptTakeback": func(command) { withC4((*connect4.Service).AcceptTakeback) },
	"gomoku.offerTakeback":    func(command) { withGM((*gomoku.Service).OfferTakeback) },
	"gomoku.acceptTakeback":   func(command) { withGM((*gomoku.Service).AcceptTakeback) },
	"hex.offerTakeback":       func(command) { withHex((*hex.Service).OfferTakeback) },
	"hex.acceptTakeback":      func(command) { withHex((*hex.Service).AcceptTakeback) },
	"dots.offerTakeback":      func(command) { withDots((*dots.Service).OfferTakeback) },
	"dots.acceptTakeback":     func(command) { withDots((*dots.Service).AcceptTakeback) },
	"weiqi.offerTakeback":     func(command) { withWeiqi((*weiqi.Service).OfferTakeback) },
	"weiqi.acceptTakeback":    func(command) { withWeiqi((*weiqi.Service).AcceptTakeback) },
	"xiangqi.offerTakeback":   func(command) { withXiangqi((*xiangqi.Service).OfferTakeback) },
	"xiangqi.acceptTakeback":  func(command) { withXiangqi((*xiangqi.Service).AcceptTakeback) },
	"checkers.offerTakeback":  func(command) { withCK((*checkers.Service).OfferTakeback) },
	"checkers.acceptTakeback": func(command) { withCK((*checkers.Service).AcceptTakeback) },
	"reversi.offerTakeback":   func(command) { withRV((*reversi.Service).OfferTakeback) },
	"reversi.acceptTakeback":  func(command) { withRV((*reversi.Service).AcceptTakeback) },
	"chess.offerTakeback":     func(command) { withChess((*chess.Service).OfferTakeback) },
	"chess.acceptTakeback":    func(command) { withChess((*chess.Service).AcceptTakeback) },

	"checkers.move":      func(c command) { moveCK(func(s *checkers.Service) error { return s.TryMove(c.Path) }) },
	"checkers.resign":    func(command) { withCK((*checkers.Service).Resign) },
	"checkers.offerDraw": func(command) { withCK((*checkers.Service).OfferDraw) },
	"checkers.agreeDraw": func(command) { withCK((*checkers.Service).AgreeDraw) },

	"reversi.place":  func(c command) { moveRV(func(s *reversi.Service) error { return s.PlaceDisc(c.Sq) }) },
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

func create(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, phrase, err := session.Host(ctx, relayURL())
	if err != nil {
		emitError("couldn't start a table: " + err.Error())
		return
	}
	start(client, name)

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

func join(phrase, name string, spectate bool) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		emitError("enter a code phrase")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := session.Join(ctx, relayURL(), phrase, spectate)
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
	start(client, name)
	emit("session.joined", map[string]any{
		"self": uint32(client.Self()),
		"role": roleName(client.Role()),
	})
}

// newServices builds a fresh set of the eight layered services.
func newServices() (ch *chat.Service, cs *chess.Service, bg *backgammon.Service,
	c4 *connect4.Service, gm *gomoku.Service, ck *checkers.Service, rv *reversi.Service, bs *battleship.Service) {
	return chat.New(), chess.New(), backgammon.New(), connect4.New(),
		gomoku.New(), checkers.New(), reversi.New(), battleship.New()
}

// start attaches services and begins pumping mux events to the UI.
func start(client *session.Client, name string) {
	ch, cs, bg, c4, gm, ck, rv, bs := newServices()
	hx, dt, wq, xq, gn := hex.New(), dots.New(), weiqi.New(), xiangqi.New(), gin.New()
	gp := gomokup.New() // Gomoku Party — networked-only (>2 players)
	mux := service.NewMux(client, ch, cs, bg, c4, gm, hx, dt, wq, xq, gn, gp, ck, rv, bs)
	mux.SetName(name)      // no-op for a blank name; peers then see "#id"
	mux.SetReconnectable() // survive transient drops; see reconnectNet

	closePrev() // bumps gen, superseding any prior pump before its socket drops
	current.mu.Lock()
	myGen := current.gen
	current.solo = false
	current.mux = mux
	current.client, current.chat, current.chess = client, ch, cs
	current.bg, current.c4, current.gm = bg, c4, gm
	current.hx, current.dt, current.wq, current.xq, current.gn = hx, dt, wq, xq, gn
	current.gp = gp
	current.ck, current.rv, current.bs = ck, rv, bs
	current.mu.Unlock()

	go pump(mux, myGen, false, false)
}

// startSolo runs a relay-free local session: two loopback ends, each with its
// own service mux. The UI reads/controls end A (host, the user). In pass-and-play
// (vsBot=false) the user drives both sides and turn-gated moves route to whichever
// end is on turn. In "play the computer" (vsBot=true) the user is end A and a bot
// drives end B. No network, no partner. See internal/solo and internal/bot.
func startSolo(name string, vsBot bool, level string) {
	host, guest, seat := solo.New()
	chA, csA, bgA, c4A, gmA, ckA, rvA, bsA := newServices()
	hxA, dtA, wqA, xqA, gnA := hex.New(), dots.New(), weiqi.New(), xiangqi.New(), gin.New()
	muxA := service.NewMux(host, chA, csA, bgA, c4A, gmA, hxA, dtA, wqA, xqA, gnA, ckA, rvA, bsA)
	muxA.SetName(name)
	chB, csB, bgB, c4B, gmB, ckB, rvB, bsB := newServices()
	hxB, dtB, wqB, xqB, gnB := hex.New(), dots.New(), weiqi.New(), xiangqi.New(), gin.New()
	muxB := service.NewMux(guest, chB, csB, bgB, c4B, gmB, hxB, dtB, wqB, xqB, gnB, ckB, rvB, bsB)
	if vsBot {
		muxB.SetName("Computer")
	} else {
		muxB.SetName("Player 2")
	}

	closePrev() // bumps gen, superseding any prior pump before its socket drops
	current.mu.Lock()
	myGen := current.gen
	current.solo = true
	current.mux = nil // solo muxes are not reconnectable and never dropped
	current.soloHost, current.soloGuest = host, guest
	current.chat, current.chess, current.bg = chA, csA, bgA
	current.c4, current.gm = c4A, gmA
	current.hx, current.dt, current.wq, current.xq, current.gn = hxA, dtA, wqA, xqA, gnA
	current.ck, current.rv, current.bs = ckA, rvA, bsA
	current.chatB, current.chessB, current.bgB = chB, csB, bgB
	current.c4B, current.gmB = c4B, gmB
	current.hxB, current.dtB, current.wqB, current.xqB, current.gnB = hxB, dtB, wqB, xqB, gnB
	current.ckB, current.rvB, current.bsB = ckB, rvB, bsB
	current.mu.Unlock()

	go pump(muxA, myGen, true, vsBot) // end A drives the UI
	if vsBot {
		// The bot plays end B; Drive also drains it.
		lvl := bot.Easy
		switch level {
		case "hard":
			lvl = bot.Hard
		case "medium":
			lvl = bot.Medium
		}
		go bot.Drive(muxB.Events(), bot.Services{
			Self: guest.Self(), Chess: csB, BG: bgB, C4: c4B, GM: gmB,
			HEX: hxB, DOTS: dtB, GO: wqB, XQ: xqB,
			CK: ckB, RV: rvB, BS: bsB,
		}, 500*time.Millisecond, lvl)
	} else {
		go drainMux(muxB) // end B stays in sync silently
	}
	seat() // seat the guest on the host → roster announce → UI joins
}

// closePrev tears down any prior session (networked client or solo loopback).
func closePrev() {
	current.mu.Lock()
	// Bump the generation FIRST: any running pump captured the old gen, so once
	// we drop its socket below it sees the drop as superseded (gen mismatch) and
	// exits quietly instead of trying to reconnect a session the user replaced.
	current.gen++
	c, host, guest := current.client, current.soloHost, current.soloGuest
	current.client, current.soloHost, current.soloGuest = nil, nil, nil
	current.mux = nil
	current.gp = nil // networked-only; absent in solo
	current.chatB, current.chessB, current.bgB = nil, nil, nil
	current.c4B, current.gmB = nil, nil
	current.hxB, current.dtB, current.wqB, current.xqB, current.gnB = nil, nil, nil, nil, nil
	current.ckB, current.rvB, current.bsB = nil, nil, nil
	current.mu.Unlock()
	// A clean Close (normal closure) tells the relay we left for good — no grace.
	// We do NOT close the mux stream here: its run goroutine may still emit the
	// Closed event, and the pump's gen check retires it without a stray notice.
	if c != nil {
		_ = c.Close()
	}
	if host != nil {
		host.Close()
	}
	if guest != nil {
		guest.Close()
	}
}

// drainMux discards end B's events — it must be drained (buffered) or B's mux
// goroutine would block; the UI only ever renders end A.
func drainMux(mux *service.Mux) {
	for range mux.Events() {
	}
}

// routeMove runs a turn-gated action. Networked: end A only. Solo: try end A,
// and if it errors (not this end's turn), try end B — exactly one end is on
// turn, so the move lands on the right side; a genuinely illegal move is
// rejected by both and surfaced.
func routeMove[T any](a, b *T, solo bool, f func(*T) error) {
	if a == nil {
		emitError("not in a session")
		return
	}
	err := f(a)
	if solo && err != nil && b != nil {
		err = f(b)
	}
	if err != nil {
		emitError(err.Error())
	}
}

func moveChess(f func(*chess.Service) error) {
	current.mu.Lock()
	a, b, s := current.chess, current.chessB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveBG(f func(*backgammon.Service) error) {
	current.mu.Lock()
	a, b, s := current.bg, current.bgB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveC4(f func(*connect4.Service) error) {
	current.mu.Lock()
	a, b, s := current.c4, current.c4B, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveGM(f func(*gomoku.Service) error) {
	current.mu.Lock()
	a, b, s := current.gm, current.gmB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

// moveGP routes a Gomoku Party action. Networked-only: end A, no solo twin.
func moveGP(f func(*gomokup.Service) error) {
	current.mu.Lock()
	a := current.gp
	current.mu.Unlock()
	routeMove(a, (*gomokup.Service)(nil), false, f)
}

func moveHex(f func(*hex.Service) error) {
	current.mu.Lock()
	a, b, s := current.hx, current.hxB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveDots(f func(*dots.Service) error) {
	current.mu.Lock()
	a, b, s := current.dt, current.dtB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveWeiqi(f func(*weiqi.Service) error) {
	current.mu.Lock()
	a, b, s := current.wq, current.wqB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveXiangqi(f func(*xiangqi.Service) error) {
	current.mu.Lock()
	a, b, s := current.xq, current.xqB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveCK(f func(*checkers.Service) error) {
	current.mu.Lock()
	a, b, s := current.ck, current.ckB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

func moveRV(f func(*reversi.Service) error) {
	current.mu.Lock()
	a, b, s := current.rv, current.rvB, current.solo
	current.mu.Unlock()
	routeMove(a, b, s, f)
}

// startGame launches (or rematches) a game by service ID.
func startGame(id string) {
	current.mu.Lock()
	// Keys are the UI-facing game ids (the same prefixes used in the
	// "<id>.state" events), NOT the Go service IDs — they differ for
	// backgammon ("bg" vs "backgammon").
	starters := map[string]func() error{}
	if current.chess != nil {
		starters["chess"] = current.chess.Start
	}
	if current.bg != nil {
		starters["bg"] = current.bg.Start
	}
	if current.c4 != nil {
		starters["connect4"] = current.c4.Start
	}
	if current.gm != nil {
		starters["gomoku"] = current.gm.Start
	}
	if current.hx != nil {
		starters["hex"] = current.hx.Start
	}
	if current.dt != nil {
		starters["dots"] = current.dt.Start
	}
	if current.wq != nil {
		starters["weiqi"] = current.wq.Start
	}
	if current.xq != nil {
		starters["xiangqi"] = current.xq.Start
	}
	if current.gn != nil {
		starters["gin"] = current.gn.Start
	}
	if current.gp != nil {
		starters["gomokup"] = current.gp.Start
	}
	if current.ck != nil {
		starters["checkers"] = current.ck.Start
	}
	if current.rv != nil {
		starters["reversi"] = current.rv.Start
	}
	if current.bs != nil {
		starters["battleship"] = current.bs.Start
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

func pump(mux *service.Mux, gen int, isSolo, vsBot bool) {
	joined := false
	for ev := range mux.Events() {
		switch e := ev.(type) {
		case service.Roster:
			// Solo has no lobby: once the loopback guest is seated (roster shows
			// both ends), tell the UI to open the table — self is the host end.
			if isSolo && !joined && len(e.Members) >= 2 {
				joined = true
				emit("session.joined", map[string]any{"self": uint32(1), "role": "host", "solo": true, "bot": vsBot})
			}
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
		case gomoku.State:
			emitGomokuState(e)
		case gomokup.State:
			emitGomokupState(e)
		case hex.State:
			emitHexState(e)
		case dots.State:
			emitDotsState(e)
		case weiqi.State:
			emitWeiqiState(e)
		case xiangqi.State:
			emitXiangqiState(e)
		case gin.State:
			emitGinState(e)
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
		case service.Promoted:
			// Host migration: this end just became the session host.
			emit("session.promoted", map[string]any{"self": uint32(e.Self)})
		case service.ServiceError:
			emitError(fmt.Sprintf("%s: %v", e.Service, e.Err))
		case service.SessionEvent:
			if closed, ok := e.Event.(session.Closed); ok {
				if pumpClosed(mux, gen, isSolo, closed.Reason) {
					continue // resumed via reconnect — keep pumping the same mux
				}
				return
			}
		}
	}
}

// pumpClosed handles a session.Closed. It returns true when the session was
// transparently resumed (the caller keeps pumping) and false when the pump
// should exit. A superseded pump (a newer session started) exits silently; a
// solo or terminal close surfaces session.closed; an unexpected drop on a live
// networked session triggers reconnectNet.
func pumpClosed(mux *service.Mux, gen int, isSolo bool, reason string) bool {
	if !isCurrent(gen) {
		return false // a newer session replaced us; leave quietly
	}
	if !isSolo && reason == "connection lost" {
		if reconnectNet(mux, gen) {
			return true
		}
		if !isCurrent(gen) {
			return false // superseded while we were retrying
		}
	}
	emit("session.closed", map[string]any{"reason": reason})
	return false
}

// isCurrent reports whether gen is still the active session generation.
func isCurrent(gen int) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.gen == gen
}

// reconnectNet re-establishes a dropped networked session in place: it emits
// session.reconnecting, retries Client.Reconnect with capped backoff for up to
// a minute, and on success rebinds the mux (same id, key, and services — the
// in-memory game state is untouched) and emits session.resumed. It bails early
// if the session is superseded. Returns whether the session resumed.
func reconnectNet(mux *service.Mux, gen int) bool {
	current.mu.Lock()
	c := current.client
	current.mu.Unlock()
	if c == nil {
		return false
	}
	emit("session.reconnecting", map[string]any{})
	delay := 500 * time.Millisecond
	for attempts := 0; attempts < 40 && isCurrent(gen); attempts++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := c.Reconnect(ctx)
		cancel()
		if err == nil {
			mux.Rebind(c)
			emit("session.resumed", map[string]any{})
			return true
		}
		time.Sleep(delay)
		if delay < 4*time.Second {
			delay *= 2
		}
	}
	return false
}

// withMux runs fn with the current networked mux, if any (solo has none).
func withMux(fn func(*service.Mux)) {
	current.mu.Lock()
	m := current.mux
	current.mu.Unlock()
	if m != nil {
		fn(m)
	}
}

func emitRoster(e service.Roster) {
	members := map[string]string{}
	for id, role := range e.Members {
		members[fmt.Sprint(uint32(id))] = roleName(role)
	}
	names := map[string]string{}
	for id, n := range e.Names {
		names[fmt.Sprint(uint32(id))] = n
	}
	endpoints := map[string]string{}
	for id, ep := range e.Endpoints {
		endpoints[fmt.Sprint(uint32(id))] = ep
	}
	emit("roster", map[string]any{
		"members": members, "names": names,
		"endpoints": endpoints, "pushKey": e.PushKey,
	})
}

func emitChessState(e chess.State) {
	emit("chess.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"fen": e.FEN, "whiteId": uint32(e.WhiteID), "blackId": uint32(e.BlackID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome, "method": e.Method,
		"lastUci": e.LastUCI, "playing": e.Playing,
		"history": e.History, "pgn": e.PGN,
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
		"playing": e.Playing, "history": e.History,
	})
}

func emitCKState(e checkers.State) {
	legal := make([][]int8, len(e.Legal))
	for i, m := range e.Legal {
		legal[i] = []int8(m)
	}
	emit("checkers.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"legal": legal, "lastPath": e.LastPath, "playing": e.Playing,
		"history": e.History,
	})
}

func emitBSState(e battleship.State) {
	emit("battleship.state", map[string]any{
		"phase": e.Phase, "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "myFleet": u8ints(e.MyFleet[:]),
		"committed": e.Committed[:],
		"reveals":   [][]int8{e.Reveals[0][:], e.Reveals[1][:]},
		"sunk":      [][]int{u8ints(e.Sunk[0]), u8ints(e.Sunk[1])},
		"outcome":   e.Outcome, "cheatBy": uint32(e.CheatBy), "playing": e.Playing,
		"history": e.History,
	})
}

// u8ints copies a []uint8 to a []int so it JSON-marshals as a number array —
// encoding/json renders []uint8 (== []byte) as a base64 string, which the JS
// board would then choke on (e.g. sunk[side].map is not a function). Always
// non-nil, so an empty fleet/sunk list becomes [] rather than null.
func u8ints(b []uint8) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

func emitRVState(e reversi.State) {
	emit("reversi.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome, "legal": e.Legal,
		"passed": e.Passed, "black": e.Black, "white": e.White,
		"lastSq": e.LastSq, "playing": e.Playing, "history": e.History,
	})
}

func emitC4State(e connect4.State) {
	emit("connect4.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"winCells": e.WinCells, "lastCol": e.LastCol, "playing": e.Playing,
		"history": e.History,
	})
}

func emitGomokuState(e gomoku.State) {
	emit("gomoku.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"winCells": e.WinCells, "last": e.Last, "playing": e.Playing,
		"history": e.History,
	})
}

func emitGomokupState(e gomokup.State) {
	seats := make([]uint32, len(e.Seats))
	for i, id := range e.Seats {
		seats[i] = uint32(id)
	}
	emit("gomokup.state", map[string]any{
		"board": e.Board[:], "seats": seats, "gone": e.Gone,
		"turnId": uint32(e.TurnID), "winnerId": uint32(e.WinnerID),
		"outcome": e.Outcome, "draw": e.Draw, "winCells": e.WinCells,
		"last": e.Last, "playing": e.Playing, "history": e.History,
	})
}

func emitHexState(e hex.State) {
	emit("hex.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"winCells": e.WinCells, "last": e.Last, "legal": e.Legal,
		"playing": e.Playing, "history": e.History,
	})
}

func emitDotsState(e dots.State) {
	emit("dots.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"edges": e.Edges[:], "boxes": e.Boxes[:],
		"scoreP1": e.ScoreP1, "scoreP2": e.ScoreP2,
		"p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"last": e.Last, "playing": e.Playing,
		"history": e.History, "legal": e.Legal,
	})
}

func emitWeiqiState(e weiqi.State) {
	emit("weiqi.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"last": e.Last, "legal": e.Legal, "playing": e.Playing,
		"history": e.History, "capturesB": e.CapturesB, "capturesW": e.CapturesW,
		"passed": e.Passed, "passes": e.Passes,
		"scoreB": e.ScoreB, "scoreW": e.ScoreW,
	})
}

func emitGinState(e gin.State) {
	emit("gin.state", map[string]any{
		"playing": e.Playing, "phase": e.Phase,
		"p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID), "turnId": uint32(e.TurnID),
		"hand": e.Hand, "handCounts": e.HandCounts[:], "discard": e.Discard,
		"stockCount": e.StockCount, "deadwood": e.Deadwood, "canKnock": e.CanKnock,
		"scores": e.Scores[:], "outcome": e.Outcome, "verified": e.Verified,
		"oppHand": e.OppHand, "dealerId": uint32(e.DealerID), "handsWon": e.HandsWon[:],
		"matchTarget": e.MatchTarget, "matchOver": e.MatchOver,
	})
}

func emitXiangqiState(e xiangqi.State) {
	legal := make([][2]int8, len(e.Legal))
	copy(legal, e.Legal)
	emit("xiangqi.state", map[string]any{
		"canTakeback": e.CanTakeback, "takebackBy": uint32(e.TakebackBy),
		"board": e.Board[:], "p1Id": uint32(e.P1ID), "p2Id": uint32(e.P2ID),
		"turnId": uint32(e.TurnID), "outcome": e.Outcome,
		"legal": legal, "lastFrom": e.LastFrom, "lastTo": e.LastTo,
		"inCheck": e.InCheck, "playing": e.Playing, "history": e.History,
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
	closePrev()
	current.mu.Lock()
	current.solo = false
	current.chat, current.chess, current.bg = nil, nil, nil
	current.c4, current.gm = nil, nil
	current.hx, current.dt, current.wq, current.xq, current.gn = nil, nil, nil, nil, nil
	current.ck, current.rv, current.bs = nil, nil, nil
	current.mu.Unlock()
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

func withGM(f func(*gomoku.Service) error) {
	current.mu.Lock()
	s := current.gm
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withGP(f func(*gomokup.Service) error) {
	current.mu.Lock()
	s := current.gp
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withHex(f func(*hex.Service) error) {
	current.mu.Lock()
	s := current.hx
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withDots(f func(*dots.Service) error) {
	current.mu.Lock()
	s := current.dt
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withWeiqi(f func(*weiqi.Service) error) {
	current.mu.Lock()
	s := current.wq
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withXiangqi(f func(*xiangqi.Service) error) {
	current.mu.Lock()
	s := current.xq
	current.mu.Unlock()
	callService(s == nil, func() error { return f(s) })
}

func withGin(f func(*gin.Service) error) {
	current.mu.Lock()
	s := current.gn
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
