package relay

import (
	"time"

	"github.com/richardwooding/kibitz/internal/wire"
)

// hub owns one session. All session state is confined to the run goroutine;
// connection handlers talk to it exclusively through the inbox channel.
type hub struct {
	id      wire.SessionID
	max     int
	created time.Time
	inbox   chan any // joinCmd | leaveCmd | frameCmd | closeCmd
	done    chan struct{}

	// Owned by run() — never touched from outside.
	clients map[wire.ParticipantID]*client
	nextID  wire.ParticipantID
}

// client is the hub's view of one connected participant. out is drained by
// the connection's writer goroutine; if it fills up (slow reader), the hub
// drops the client rather than blocking the whole session.
type client struct {
	id   wire.ParticipantID
	out  chan []byte
	kick func() // closes the underlying connection
}

// sendBuffer is the per-client fan-out buffer. A full buffer means a reader
// slower than the session's traffic — disconnecting it beats stalling everyone.
const sendBuffer = 64

type joinCmd struct {
	out   chan []byte
	kick  func()
	reply chan joinReply
}

type joinReply struct {
	ok    bool
	errC  uint16
	errS  string
	id    wire.ParticipantID
	peers []wire.ParticipantID
}

type leaveCmd struct{ id wire.ParticipantID }

type frameCmd struct {
	from wire.ParticipantID
	typ  wire.MsgType
	raw  []byte // CBOR body as received
}

type closeCmd struct{ reason string }

const hostID wire.ParticipantID = 1

func newHub(id wire.SessionID, maxParticipants int, onEmpty func()) *hub {
	h := &hub{
		id:      id,
		max:     maxParticipants,
		created: time.Now(),
		inbox:   make(chan any, 16),
		done:    make(chan struct{}),
		clients: map[wire.ParticipantID]*client{},
		nextID:  hostID,
	}
	go h.run(onEmpty)
	return h
}

func (h *hub) run(onEmpty func()) {
	defer func() {
		for _, c := range h.clients {
			c.kick()
		}
		close(h.done)
		onEmpty()
	}()
	for cmd := range h.inbox {
		switch cmd := cmd.(type) {
		case joinCmd:
			h.handleJoin(cmd)
		case leaveCmd:
			if h.handleLeave(cmd.id) {
				return
			}
		case frameCmd:
			h.route(cmd)
		case closeCmd:
			h.broadcastFrame(wire.MsgSessionClosed, wire.SessionClosed{Reason: cmd.reason}, 0)
			return
		}
	}
}

func (h *hub) handleJoin(cmd joinCmd) {
	if len(h.clients) >= h.max {
		cmd.reply <- joinReply{errC: wire.ErrCodeSessionFull, errS: "session full"}
		return
	}
	id := h.nextID
	h.nextID++
	peers := make([]wire.ParticipantID, 0, len(h.clients))
	for pid := range h.clients {
		peers = append(peers, pid)
	}
	h.clients[id] = &client{id: id, out: cmd.out, kick: cmd.kick}
	cmd.reply <- joinReply{ok: true, id: id, peers: peers}
	h.broadcastFrame(wire.MsgParticipantJoined, wire.ParticipantJoined{ParticipantID: id}, id)
}

// handleLeave removes a participant. Returns true when the hub must shut
// down: the host left, or nobody is left.
func (h *hub) handleLeave(id wire.ParticipantID) bool {
	c, ok := h.clients[id]
	if !ok {
		return false
	}
	delete(h.clients, id)
	c.kick()
	if id == hostID {
		h.broadcastFrame(wire.MsgSessionClosed, wire.SessionClosed{Reason: "host left"}, 0)
		return true
	}
	if len(h.clients) == 0 {
		return true
	}
	h.broadcastFrame(wire.MsgParticipantLeft, wire.ParticipantLeft{ParticipantID: id}, 0)
	return false
}

// route forwards Direct/Broadcast frames, re-encoding with From stamped by
// the relay so a client can never spoof its sender ID.
func (h *hub) route(cmd frameCmd) {
	switch cmd.typ {
	case wire.MsgDirect:
		d, err := wire.Body[wire.Direct](cmd.raw)
		if err != nil {
			h.sendTo(cmd.from, wire.MsgError, wire.Error{Code: wire.ErrCodeBadFrame, Msg: "bad direct frame"})
			return
		}
		d.From = cmd.from
		if _, ok := h.clients[d.To]; !ok {
			h.sendTo(cmd.from, wire.MsgError, wire.Error{Code: wire.ErrCodeUnknownPeer, Msg: "unknown peer"})
			return
		}
		h.sendTo(d.To, wire.MsgDirect, d)
	case wire.MsgBroadcast:
		b, err := wire.Body[wire.Broadcast](cmd.raw)
		if err != nil {
			h.sendTo(cmd.from, wire.MsgError, wire.Error{Code: wire.ErrCodeBadFrame, Msg: "bad broadcast frame"})
			return
		}
		b.From = cmd.from
		h.broadcastFrame(wire.MsgBroadcast, b, cmd.from)
	}
}

func (h *hub) sendTo(id wire.ParticipantID, t wire.MsgType, body any) {
	c, ok := h.clients[id]
	if !ok {
		return
	}
	frame, err := wire.Encode(t, body)
	if err != nil {
		return
	}
	select {
	case c.out <- frame:
	default:
		// Slow consumer: drop the client, not the session.
		delete(h.clients, id)
		c.kick()
	}
}

func (h *hub) broadcastFrame(t wire.MsgType, body any, except wire.ParticipantID) {
	frame, err := wire.Encode(t, body)
	if err != nil {
		return
	}
	for id, c := range h.clients {
		if id == except {
			continue
		}
		select {
		case c.out <- frame:
		default:
			delete(h.clients, id)
			c.kick()
		}
	}
}
