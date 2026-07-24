// Package solo provides a relay-free, in-memory transport for the local
// "try a game" hot-seat: two session ends (a host and one player) wired to each
// other with no WebSocket, PAKE, or crypto. Each *Endpoint structurally
// satisfies service.Conn, so the real service.Mux and every game service run
// unchanged — the only thing swapped out is the network.
//
// A frame one end "sends" is delivered synchronously to the peer's event
// channel as a session.Frame, exactly as the relay would after decryption.
// Because both ends run the real both-sides-validate services, game state stays
// mirrored; the UI drives whichever end is on turn and reads from the host end.
package solo

import (
	"sync"

	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Endpoint is one side of the loopback. It satisfies service.Conn.
type Endpoint struct {
	self   wire.ParticipantID
	hostID wire.ParticipantID
	role   session.Role
	events chan session.Event
	peer   *Endpoint

	mu   sync.Mutex
	seqs map[string]uint64 // per-service send sequence, mirrors session.Client
}

const eventBuffer = 256

// New wires two relay-free ends: host (id 1) and guest (id 2, player). Build a
// service.Mux over each, then call seat() — it delivers the membership event
// that seats the guest on the host (as a real join would), kicking off the ctl
// roster announce and snapshot handshake.
func New() (host, guest *Endpoint, seat func()) {
	host = &Endpoint{self: 1, hostID: 1, role: session.RoleHost, events: make(chan session.Event, eventBuffer), seqs: map[string]uint64{}}
	guest = &Endpoint{self: 2, hostID: 1, role: session.RolePlayer, events: make(chan session.Event, eventBuffer), seqs: map[string]uint64{}}
	host.peer = guest
	guest.peer = host
	seat = func() {
		// The host learns the guest completed the handshake — ctl seats them and
		// announces the roster; games note the opponent for seating.
		host.events <- session.MemberKeyed{ID: guest.self, Role: session.RolePlayer}
	}
	return host, guest, seat
}

func (e *Endpoint) Self() wire.ParticipantID     { return e.self }
func (e *Endpoint) HostID() wire.ParticipantID   { return e.hostID }
func (e *Endpoint) Role() session.Role           { return e.role }
func (e *Endpoint) Events() <-chan session.Event { return e.events }

// Broadcast delivers to the peer (a two-party session — "everyone else" is the
// other end).
func (e *Endpoint) Broadcast(serviceID string, body []byte) error {
	return e.deliver(e.peer, serviceID, body)
}

// SendTo delivers to the addressed participant; to==self loops back to this end.
func (e *Endpoint) SendTo(to wire.ParticipantID, serviceID string, body []byte) error {
	dst := e.peer
	if to == e.self {
		dst = e
	}
	return e.deliver(dst, serviceID, body)
}

func (e *Endpoint) deliver(dst *Endpoint, serviceID string, body []byte) error {
	e.mu.Lock()
	e.seqs[serviceID]++
	seq := e.seqs[serviceID]
	e.mu.Unlock()
	// Copy: callers may reuse the buffer after we return.
	b := make([]byte, len(body))
	copy(b, body)
	dst.events <- session.Frame{
		From:     e.self,
		Envelope: wire.Envelope{ServiceID: serviceID, Seq: seq, Body: b},
	}
	return nil
}

// Close ends the endpoint's event stream so its mux goroutine exits.
func (e *Endpoint) Close() { close(e.events) }
