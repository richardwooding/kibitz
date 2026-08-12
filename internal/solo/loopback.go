// Package solo provides a relay-free, in-memory transport for local "try a
// game" sessions: N session ends (a host and one or more players) wired to a
// shared in-memory hub with no WebSocket, PAKE, or crypto. Each *Endpoint
// structurally satisfies service.Conn, so the real service.Mux and every game
// service run unchanged — the only thing swapped out is the network.
//
// A frame one end "sends" is delivered synchronously to the other ends' event
// channels as a session.Frame, exactly as the relay would after decryption.
// Because every end runs the real both-sides-validate services, game state
// stays mirrored; the UI drives whichever end is on turn and reads the host end.
package solo

import (
	"sync"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

const eventBuffer = 256

// hub is the shared in-memory switch every Endpoint delivers through. Broadcast
// fans a frame to all other ends; SendTo routes to one by participant id.
type hub struct {
	mu   sync.Mutex
	ends map[wire.ParticipantID]*Endpoint
}

func newHub() *hub { return &hub{ends: map[wire.ParticipantID]*Endpoint{}} }

func (h *hub) add(id, hostID wire.ParticipantID, role session.Role) *Endpoint {
	e := &Endpoint{
		self: id, hostID: hostID, role: role,
		events: make(chan session.Event, eventBuffer),
		seqs:   map[string]uint64{}, hub: h,
	}
	h.mu.Lock()
	h.ends[id] = e
	h.mu.Unlock()
	return e
}

func (h *hub) get(id wire.ParticipantID) *Endpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ends[id]
}

func (h *hub) others(self wire.ParticipantID) []*Endpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Endpoint, 0, len(h.ends))
	for id, e := range h.ends {
		if id != self {
			out = append(out, e)
		}
	}
	return out
}

// Endpoint is one side of the loopback. It satisfies service.Conn.
type Endpoint struct {
	self   wire.ParticipantID
	hostID wire.ParticipantID
	role   session.Role
	events chan session.Event
	hub    *hub

	mu   sync.Mutex
	seqs map[string]uint64 // per-service send sequence, mirrors session.Client
}

// New wires two relay-free ends: host (id 1) and guest (id 2, player). Build a
// service.Mux over each, then call seat() — it delivers the membership event
// that seats the guest on the host (as a real join would), kicking off the ctl
// roster announce and snapshot handshake.
func New() (host, guest *Endpoint, seat func()) {
	h := newHub()
	host = h.add(1, 1, session.RoleHost)
	guest = h.add(2, 1, proto.RolePlayer)
	seat = func() {
		host.events <- session.MemberKeyed{ID: guest.self, Role: proto.RolePlayer}
	}
	return host, guest, seat
}

// NewParty wires a host (id 1) plus `guests` player ends (ids 2..1+guests) to a
// shared hub — for N-player local play (e.g. solo vs several bots). seat()
// delivers one membership event per guest so the host seats them all and
// announces the full roster.
func NewParty(guests int) (host *Endpoint, gs []*Endpoint, seat func()) {
	h := newHub()
	host = h.add(1, 1, session.RoleHost)
	for i := 0; i < guests; i++ {
		gs = append(gs, h.add(wire.ParticipantID(2+i), 1, proto.RolePlayer))
	}
	captured := gs
	seat = func() {
		for _, g := range captured {
			host.events <- session.MemberKeyed{ID: g.self, Role: proto.RolePlayer}
		}
	}
	return host, gs, seat
}

func (e *Endpoint) Self() wire.ParticipantID     { return e.self }
func (e *Endpoint) HostID() wire.ParticipantID   { return e.hostID }
func (e *Endpoint) Role() session.Role           { return e.role }
func (e *Endpoint) Events() <-chan session.Event { return e.events }

// Broadcast delivers to every other end. The per-service sequence bumps once,
// so every recipient sees the same seq (as a relay broadcast would).
func (e *Endpoint) Broadcast(serviceID string, body []byte) error {
	seq := e.nextSeq(serviceID)
	for _, d := range e.hub.others(e.self) {
		e.push(d, serviceID, seq, body)
	}
	return nil
}

// SendTo delivers to the addressed participant (to==self loops back). An unknown
// target is dropped (cannot happen in a well-formed solo session).
func (e *Endpoint) SendTo(to wire.ParticipantID, serviceID string, body []byte) error {
	seq := e.nextSeq(serviceID)
	dst := e
	if to != e.self {
		dst = e.hub.get(to)
	}
	if dst == nil {
		return nil
	}
	e.push(dst, serviceID, seq, body)
	return nil
}

func (e *Endpoint) nextSeq(serviceID string) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seqs[serviceID]++
	return e.seqs[serviceID]
}

func (e *Endpoint) push(dst *Endpoint, serviceID string, seq uint64, body []byte) {
	// Copy: callers may reuse the buffer after we return.
	b := make([]byte, len(body))
	copy(b, body)
	dst.events <- session.Frame{
		From:     e.self,
		Envelope: wire.Envelope{ServiceID: serviceID, Seq: seq, Body: b},
	}
}

// Close ends the endpoint's event stream so its mux goroutine exits.
func (e *Endpoint) Close() { close(e.events) }
