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
	conn    *websocket.Conn
	sid     wire.SessionID
	phraseC string // canonical phrase — the PAKE secret
	self    wire.ParticipantID
	hostID  wire.ParticipantID
	role    Role

	groupKey crypto.Key
	keyed    bool

	events chan Event

	writeMu sync.Mutex // coder/websocket allows one concurrent writer

	mu      sync.Mutex
	seqs    map[string]uint64 // per-service send sequence
	joiners map[wire.ParticipantID]Role
	pending *crypto.Joiner // joiner-side handshake state until Pake2 lands
}

const eventBuffer = 256

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
	go c.readLoop()
	return c, p, nil
}

// Join connects to an existing session with its phrase. It returns once the
// handshake completes and the client is keyed; a wrong phrase surfaces as
// crypto.ErrUnwrap.
func Join(ctx context.Context, relayURL, phraseText string) (*Client, error) {
	c, err := dial(ctx, relayURL, phraseText)
	if err != nil {
		return nil, err
	}
	if err := c.joinHello(ctx); err != nil {
		_ = c.conn.CloseNow()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

func dial(ctx context.Context, relayURL, phraseText string) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, relayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("session: dial relay: %w", err)
	}
	conn.SetReadLimit(wire.MaxFrame + 16)
	canonical := phrase.Canonical(phraseText)
	return &Client{
		conn:    conn,
		sid:     phrase.SessionID(canonical),
		phraseC: canonical,
		events:  make(chan Event, eventBuffer),
		seqs:    map[string]uint64{},
		joiners: map[wire.ParticipantID]Role{},
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

// Close tears the connection down; the read loop emits Closed and exits.
func (c *Client) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "bye")
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
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	return wire.Decode(data)
}

func (c *Client) emit(e Event) {
	c.events <- e
}
