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
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/session"
)

// command is every UI→core message; unused fields stay empty.
type command struct {
	Type   string `json:"type"`
	Phrase string `json:"phrase,omitempty"`
	Text   string `json:"text,omitempty"`
	UCI    string `json:"uci,omitempty"`
	From   string `json:"from,omitempty"` // square, for chess.targets
	ID     int    `json:"id,omitempty"`   // request correlation for queries
}

type app struct {
	mu     sync.Mutex
	client *session.Client
	chat   *chat.Service
	chess  *chess.Service
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

func dispatch(raw string) {
	var cmd command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		emitError("bad command: " + err.Error())
		return
	}
	switch cmd.Type {
	case "create":
		create()
	case "join":
		join(cmd.Phrase)
	case "chat.say":
		withChat(func(c *chat.Service) error { return c.Say(cmd.Text) })
	case "chess.move":
		withChess(func(c *chess.Service) error { return c.TryMove(cmd.UCI) })
	case "chess.resign":
		withChess((*chess.Service).Resign)
	case "chess.offerDraw":
		withChess((*chess.Service).OfferDraw)
	case "chess.agreeDraw":
		withChess((*chess.Service).AgreeDraw)
	case "chess.targets":
		targets(cmd.From, cmd.ID)
	case "leave":
		leave()
	default:
		emitError("unknown command " + cmd.Type)
	}
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
	mux := service.NewMux(client, ch, cs)

	current.mu.Lock()
	if current.client != nil {
		_ = current.client.Close()
	}
	current.client, current.chat, current.chess = client, ch, cs
	current.mu.Unlock()

	go pump(mux)
}

func pump(mux *service.Mux) {
	for ev := range mux.Events() {
		switch e := ev.(type) {
		case service.Roster:
			members := map[string]string{}
			for id, role := range e.Members {
				members[fmt.Sprint(uint32(id))] = roleName(role)
			}
			emit("roster", map[string]any{"members": members})
		case chat.Message:
			emit("chat.msg", map[string]any{"from": uint32(e.From), "text": e.Text})
		case chess.State:
			emit("chess.state", map[string]any{
				"fen": e.FEN, "whiteId": uint32(e.WhiteID), "blackId": uint32(e.BlackID),
				"turnId": uint32(e.TurnID), "outcome": e.Outcome, "method": e.Method,
				"lastUci": e.LastUCI, "playing": e.Playing,
			})
		case chess.DrawOffered:
			emit("chess.drawOffered", map[string]any{"from": uint32(e.From)})
		case chess.Desync:
			emitError("game desynchronized: " + e.Reason)
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
	current.client, current.chat, current.chess = nil, nil, nil
	current.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func withChat(f func(*chat.Service) error) {
	current.mu.Lock()
	c := current.chat
	current.mu.Unlock()
	if c == nil {
		emitError("not in a session")
		return
	}
	if err := f(c); err != nil {
		emitError(err.Error())
	}
}

func withChess(f func(*chess.Service) error) {
	current.mu.Lock()
	c := current.chess
	current.mu.Unlock()
	if c == nil {
		emitError("not in a session")
		return
	}
	if err := f(c); err != nil {
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
