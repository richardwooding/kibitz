// Package session is the client-side engine: it dials the relay, runs the
// create/join handshake, performs the PAKE + group-key exchange, and moves
// encrypted service envelopes. It compiles natively (tests, headless tools)
// and to WASM (the browser core) — no syscall/js here, ever.
package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/richardwooding/kibitz/internal/crypto"
	"github.com/richardwooding/kibitz/internal/phrase"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Role is assigned by the host when it wraps the group key for a joiner.
// The zero value means "not keyed yet".
type Role uint8

const (
	RoleNone      Role = 0
	RoleHost      Role = 1
	RolePlayer    Role = 2
	RoleSpectator Role = 3
)

// Event is anything the session surfaces to the layer above (service mux or
// UI bridge): Ready, MemberJoined, MemberLeft, Frame, Closed.
type Event any

type (
	// Ready fires once the client is keyed and can send/receive frames.
	Ready struct{ Self wire.ParticipantID }
	// MemberJoined fires when the relay announces a new participant. For the
	// host it fires before that member is keyed.
	MemberJoined struct{ ID wire.ParticipantID }
	// MemberKeyed fires on the host once a joiner completes the handshake.
	MemberKeyed struct {
		ID   wire.ParticipantID
		Role Role
	}
	// MemberLeft fires when a participant disconnects.
	MemberLeft struct{ ID wire.ParticipantID }
	// Frame is one decrypted service envelope from a peer.
	Frame struct {
		From     wire.ParticipantID
		Envelope wire.Envelope
	}
	// Closed fires last: the session is over.
	Closed struct{ Reason string }
)

// Client is one end of a live session.
type Client struct {
	conn     *websocket.Conn
	relayURL string // kept so Reconnect can re-dial the same relay
	sid      wire.SessionID
	phraseC  string // canonical phrase — the PAKE secret
	self     wire.ParticipantID
	hostID   wire.ParticipantID
	role     Role

	resumeToken []byte // opaque relay-issued secret to reclaim this slot after a drop
	spectate    bool   // joiner intent: ask the host to seat us as a spectator

	groupKey crypto.Key
	keyed    bool

	events chan Event

	writeMu sync.Mutex // coder/websocket allows one concurrent writer

	mu      sync.Mutex
	seqs    map[string]uint64 // per-service send sequence
	joiners map[wire.ParticipantID]Role
}

const (
	eventBuffer = 256
	// pingInterval elicits a pong that keeps both the relay's idle timeout from
	// firing and the read deadline below refreshed. readTimeout is how long the
	// read loop waits for ANY frame before declaring the connection dead — this
	// is what catches a half-open drop (offline, NAT eviction) where reads would
	// otherwise block forever. readTimeout > pingInterval so a healthy quiet
	// connection (pongs every pingInterval) never times out.
	pingInterval = 8 * time.Second
	readTimeout  = 20 * time.Second
)

// Host creates a new session on the relay and returns a keyed client plus
// the freshly generated code phrase. The first joiner becomes the player;
// later joiners are spectators.
func Host(ctx context.Context, relayURL string) (*Client, string, error) {
	p := phrase.New()
	c, err := dial(ctx, relayURL, p)
	if err != nil {
		return nil, "", err
	}
	if err := c.hostHello(ctx); err != nil {
		_ = c.conn.CloseNow()
		return nil, "", err
	}
	go c.readLoop(c.conn)
	go c.pingLoop(c.conn)
	return c, p, nil
}

// Join connects to an existing session with its phrase. It returns once the
// handshake completes and the client is keyed; a wrong phrase surfaces as
// crypto.ErrUnwrap. When spectate is true the joiner asks to be seated as a
// spectator regardless of join order (it never takes the open player seat).
func Join(ctx context.Context, relayURL, phraseText string, spectate bool) (*Client, error) {
	c, err := dial(ctx, relayURL, phraseText)
	if err != nil {
		return nil, err
	}
	c.spectate = spectate
	if err := c.joinHello(ctx); err != nil {
		_ = c.conn.CloseNow()
		return nil, err
	}
	go c.readLoop(c.conn)
	go c.pingLoop(c.conn)
	return c, nil
}

// Reconnect re-dials the relay and reclaims this client's slot after an
// unexpected drop, preserving the participant id, role, group key, and per-
// service send sequence — so peers see an uninterrupted sender and no re-key is
// needed. It returns once keyed traffic can flow again; the caller then rebinds
// its mux (see service.Mux.Rebind). A rejected reclaim (grace expired, relay
// restarted, bad token) returns an error the caller should treat as terminal.
//
// Precondition: the previous readLoop has ended (the events channel closed),
// which is exactly the state after a Closed event — so no goroutine is touching
// the client when Reconnect runs.
func (c *Client) Reconnect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.relayURL, nil)
	if err != nil {
		return fmt.Errorf("session: redial relay: %w", err)
	}
	conn.SetReadLimit(wire.MaxFrame + 16)
	c.conn = conn
	if err := c.writeFrame(wire.MsgResumeSession, wire.ResumeSession{
		SessionID: c.sid, ParticipantID: c.self, Token: c.resumeToken,
	}); err != nil {
		_ = conn.CloseNow()
		return err
	}
	raw, err := c.awaitReply(ctx, wire.MsgJoinResult)
	if err != nil {
		_ = conn.CloseNow()
		return err
	}
	jr, err := wire.Body[wire.JoinResult](raw)
	if err != nil {
		_ = conn.CloseNow()
		return err
	}
	if !jr.OK {
		_ = conn.CloseNow()
		return fmt.Errorf("session: resume refused: %s", jr.Err)
	}
	if len(jr.ResumeToken) > 0 {
		c.resumeToken = jr.ResumeToken
	}
	// Fresh event stream for the new connection; the mux picks it up on Rebind.
	c.events = make(chan Event, eventBuffer)
	go c.readLoop(c.conn)
	go c.pingLoop(c.conn)
	return nil
}

// pingLoop heartbeats every pingInterval so the relay's idle timeout never
// fires, NAT mappings stay warm, and — most importantly — the read loop's
// deadline keeps getting refreshed by the returning pongs on a healthy but
// quiet connection. It writes to its own conn so a later Reconnect swapping
// c.conn never makes a stale loop touch the new connection; a write failure
// (dead socket) force-closes conn to unblock the read loop.
func (c *Client) pingLoop(conn *websocket.Conn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	var nonce uint32
	for range t.C {
		nonce++
		frame, err := wire.Encode(wire.MsgPing, wire.Ping{Nonce: nonce})
		if err != nil {
			continue
		}
		c.writeMu.Lock()
		err = conn.Write(context.Background(), websocket.MessageBinary, frame)
		c.writeMu.Unlock()
		if err != nil {
			_ = conn.CloseNow()
			return
		}
	}
}

func dial(ctx context.Context, relayURL, phraseText string) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, relayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("session: dial relay: %w", err)
	}
	conn.SetReadLimit(wire.MaxFrame + 16)
	canonical := phrase.Canonical(phraseText)
	return &Client{
		conn:     conn,
		relayURL: relayURL,
		sid:      phrase.SessionID(canonical),
		phraseC:  canonical,
		events:   make(chan Event, eventBuffer),
		seqs:     map[string]uint64{},
		joiners:  map[wire.ParticipantID]Role{},
	}, nil
}

// Events delivers session events. The channel closes after Closed.
func (c *Client) Events() <-chan Event { return c.events }

// Self returns this client's participant ID (valid after construction).
func (c *Client) Self() wire.ParticipantID { return c.self }

// HostID returns the session host's participant ID.
func (c *Client) HostID() wire.ParticipantID { return c.hostID }

// Role returns this client's role (RoleHost, or as assigned by the host).
func (c *Client) Role() Role { return c.role }

// Close tears the connection down gracefully (a normal-closure "bye"); the
// relay treats this as leaving for good — no grace, no reconnect.
func (c *Client) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "bye")
}

// CloseNow drops the connection abruptly, with no close handshake — as a real
// network loss does. The relay classifies this as unexpected and holds the slot
// for its grace window, so a subsequent Reconnect can reclaim it. The read loop
// emits Closed("connection lost") and exits.
func (c *Client) CloseNow() error {
	return c.conn.CloseNow()
}

// Broadcast seals one service message to every other participant.
func (c *Client) Broadcast(serviceID string, body []byte) error {
	payload, err := c.seal(serviceID, body)
	if err != nil {
		return err
	}
	return c.writeFrame(wire.MsgBroadcast, wire.Broadcast{Payload: payload})
}

// SendTo seals one service message to a single participant.
func (c *Client) SendTo(to wire.ParticipantID, serviceID string, body []byte) error {
	payload, err := c.seal(serviceID, body)
	if err != nil {
		return err
	}
	return c.writeFrame(wire.MsgDirect, wire.Direct{To: to, Payload: payload})
}

func (c *Client) seal(serviceID string, body []byte) ([]byte, error) {
	c.mu.Lock()
	if !c.keyed {
		c.mu.Unlock()
		return nil, errors.New("session: not keyed yet")
	}
	c.seqs[serviceID]++
	env := wire.Envelope{ServiceID: serviceID, Seq: c.seqs[serviceID], Body: body}
	key := c.groupKey
	c.mu.Unlock()

	plain, err := wire.Marshal(env)
	if err != nil {
		return nil, err
	}
	sf, err := crypto.Seal(key, plain, c.sid, c.self)
	if err != nil {
		return nil, err
	}
	return wire.EncodePayload(wire.KindSealed, sf)
}

func (c *Client) writeFrame(t wire.MsgType, body any) error {
	frame, err := wire.Encode(t, body)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(context.Background(), websocket.MessageBinary, frame)
}

func (c *Client) readFrame(ctx context.Context) (wire.MsgType, []byte, error) {
	return c.readFrameConn(ctx, c.conn)
}

// readFrameConn reads one frame from a specific connection. The read loop
// passes its own conn so a later Reconnect swapping c.conn never races it.
func (c *Client) readFrameConn(ctx context.Context, conn *websocket.Conn) (wire.MsgType, []byte, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	return wire.Decode(data)
}

func (c *Client) emit(e Event) {
	c.events <- e
}
