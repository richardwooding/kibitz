package service

import (
	"sync/atomic"

	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Conn is the transport a Mux drives: one keyed session end. *session.Client
// satisfies it over the relay; internal/solo provides an in-memory loopback
// end for the no-relay hot-seat mode. It is exactly the surface the mux uses.
type Conn interface {
	Sender
	Self() wire.ParticipantID
	HostID() wire.ParticipantID
	Role() session.Role
	Events() <-chan session.Event
}

// Mux drains a session client's events, routes envelopes to services by ID
// (unknown IDs are ignored for forward compatibility), watches per-sender
// sequence numbers, and merges everything the UI cares about into one event
// stream.
type Mux struct {
	client   Conn
	services map[string]Service
	ctl      *ctlService
	events   chan any
	cmds     chan func() // local actions run on the mux goroutine
	lastSeq  map[seqKey]uint64

	// reconnectable, when set, keeps the merged event stream open across a
	// connection drop so the caller can Reconnect the client and Rebind this
	// mux onto it. When unset (solo, bot, tests), run() closes events on exit
	// as before, so `range mux.Events()` consumers terminate.
	reconnectable atomic.Bool
}

type seqKey struct {
	from    wire.ParticipantID
	service string
}

// Desync is emitted when a sender's per-service sequence gaps or repeats —
// a dropped or replayed frame the UI should surface.
type Desync struct {
	From    wire.ParticipantID
	Service string
	Want    uint64
	Got     uint64
}

// ServiceError is emitted when a service rejects a frame (illegal move,
// malformed body) — usually a peer running incompatible rules.
type ServiceError struct {
	From    wire.ParticipantID
	Service string
	Err     error
}

// SessionEvent re-surfaces session-level events (MemberJoined, MemberKeyed,
// MemberLeft, Closed) on the mux stream.
type SessionEvent struct{ Event session.Event }

// Promoted is emitted when THIS end becomes the session host via migration
// (the previous host left), so the UI can show host affordances.
type Promoted struct{ Self wire.ParticipantID }

// Promoter is the subset of the client the mux needs for host migration.
// *session.Client satisfies it; the solo loopback does NOT — so solo/bot
// sessions never migrate (they have no host concept). Asserted at runtime, so
// the Conn interface stays unchanged.
type Promoter interface {
	BecomeHost()
	SetHostID(wire.ParticipantID)
	ClaimHost() error
	RotateForMigration()     // new host: mint a fresh group key the departed host lacks
	RekeyWithNewHost() error // survivor: re-PAKE with the new host to fetch that key
}

// NewMux attaches services to the client and starts routing. The ctl service
// is always registered. Call Events for the merged stream; it closes when
// the session ends.
func NewMux(c Conn, svcs ...Service) *Mux {
	m := &Mux{
		client:   c,
		services: map[string]Service{},
		events:   make(chan any, 256),
		cmds:     make(chan func(), 8),
		lastSeq:  map[seqKey]uint64{},
	}
	ctl := newCtl(m)
	m.ctl = ctl
	all := append([]Service{ctl}, svcs...)
	ctx := Context{Send: c, Emit: m.emit, Self: c.Self(), HostID: c.HostID(), Host: c.Role() == session.RoleHost}
	for _, s := range all {
		m.services[s.ID()] = s
		s.Attach(ctx)
	}
	go m.run(true)
	return m
}

// SetReconnectable marks this mux as surviving connection drops: its event
// stream stays open when the client disconnects, so the caller can
// Client.Reconnect and then Rebind. Call once right after NewMux. Terminal
// teardown then goes through Close instead of the client closing the stream.
func (m *Mux) SetReconnectable() { m.reconnectable.Store(true) }

// Close finalizes a reconnectable mux, closing the merged event stream. No-op
// semantics are the caller's responsibility (call exactly once).
func (m *Mux) Close() { close(m.events) }

// Rebind re-points the mux at a freshly reconnected Conn (same participant id,
// key, and services — see Client.Reconnect) and resumes routing. It re-derives
// Context and re-Attaches services (Attach only stores ctx; game state is
// preserved), and does NOT request a snapshot: the in-memory state is intact.
// The prior run goroutine has already returned (its client's stream closed).
func (m *Mux) Rebind(c Conn) {
	m.client = c
	ctx := Context{Send: c, Emit: m.emit, Self: c.Self(), HostID: c.HostID(), Host: c.Role() == session.RoleHost}
	for _, s := range m.services {
		s.Attach(ctx)
	}
	go m.run(false)
}

// SetName sets the local participant's screen name and distributes it over
// the encrypted ctl channel. Safe to call from any goroutine once the
// session is keyed (right after NewMux): the work runs on the mux goroutine,
// serialized with frame handling. A blank name is ignored (peers see "#id").
func (m *Mux) SetName(name string) {
	select {
	case m.cmds <- func() { m.ctl.setName(name) }:
	default: // mux gone or backed up; dropping a name update is harmless
	}
}

// SetEndpoint distributes the local participant's Web Push endpoint over the
// encrypted ctl channel so peers can send it "your turn" notifications. Same
// goroutine discipline as SetName.
func (m *Mux) SetEndpoint(endpoint string) {
	select {
	case m.cmds <- func() { m.ctl.setEndpoint(endpoint) }:
	default:
	}
}

// SetPushKey (host) distributes the shared session VAPID keypair over ctl.
func (m *Mux) SetPushKey(key string) {
	select {
	case m.cmds <- func() { m.ctl.setPushKey(key) }:
	default:
	}
}

// Events is the merged stream: SessionEvent, Desync, and every service's
// own event types (chat.Message, ctl Roster, …).
func (m *Mux) Events() <-chan any { return m.events }

func (m *Mux) emit(e any) { m.events <- e }

func (m *Mux) run(requestSnap bool) {
	// A reconnectable mux keeps its stream open across drops; the caller
	// finalizes with Close. Otherwise close on exit so range-consumers stop.
	defer func() {
		if !m.reconnectable.Load() {
			close(m.events)
		}
	}()

	// A fresh joiner asks the host for state it missed. On a Rebind the state
	// is already in memory, so the caller passes requestSnap=false.
	if requestSnap && m.client.Role() != session.RoleHost {
		m.ctl.requestSnapshot()
	}

	events := m.client.Events()
	for {
		select {
		case fn := <-m.cmds: // local action (e.g. SetName), on this goroutine
			fn()
		case ev, ok := <-events:
			if !ok || m.dispatch(ev) {
				return // stream closed, or a terminal session.Closed
			}
		}
	}
}

// dispatch routes one session event to services and the merged stream. It
// returns true when the run loop should stop (a terminal Closed).
func (m *Mux) dispatch(ev session.Event) (stop bool) {
	switch e := ev.(type) {
	case session.Frame:
		m.handleFrame(e)
	case session.MemberKeyed:
		m.observeMembers(func(o MemberObserver) { o.MemberKeyed(e.ID, e.Role) })
		m.emit(SessionEvent{Event: e})
	case session.MemberLeft:
		m.observeMembers(func(o MemberObserver) { o.MemberLeft(e.ID) })
		m.emit(SessionEvent{Event: e})
		m.maybeMigrate(e.ID) // if the host left, elect + promote a successor
	case session.Closed:
		m.emit(SessionEvent{Event: e})
		return true
	default:
		m.emit(SessionEvent{Event: ev})
	}
	return false
}

func (m *Mux) observeMembers(fn func(MemberObserver)) {
	for _, s := range m.services {
		if o, ok := s.(MemberObserver); ok {
			fn(o)
		}
	}
}

// maybeMigrate promotes a successor when the current host leaves. Runs on the
// mux goroutine (from dispatch), so re-Attaching services is serialized with
// frame handling — no new run loop (unlike Rebind; the connection is alive).
// Solo/tests whose Conn isn't a Promoter simply skip this.
func (m *Mux) maybeMigrate(left wire.ParticipantID) {
	p, ok := m.client.(Promoter)
	if !ok || left != m.client.HostID() {
		return
	}
	successor := m.ctl.electSuccessor(left)
	if successor == 0 {
		return
	}
	amNew := successor == m.client.Self()
	if amNew {
		p.BecomeHost()
		p.RotateForMigration() // fresh key before any survivor's re-PAKE can arrive
	} else {
		p.SetHostID(successor)
	}
	// Re-derive Context from the now-updated client and re-Attach so every
	// service picks up the new Host/HostID (Attach only stores ctx; game state
	// and the ctl roster are preserved — the Rebind discipline).
	ctx := Context{Send: m.client, Emit: m.emit, Self: m.client.Self(), HostID: m.client.HostID(), Host: m.client.Role() == session.RoleHost}
	for _, s := range m.services {
		s.Attach(ctx)
	}
	if amNew {
		for _, s := range m.services {
			if pr, ok := s.(Promotable); ok {
				pr.OnPromote()
			}
		}
		m.ctl.assumeHost(left)
		_ = p.ClaimHost()
		m.emit(Promoted{Self: m.client.Self()})
	} else {
		_ = p.RekeyWithNewHost() // fetch the rotated key from the promoted host
	}
}

func (m *Mux) handleFrame(f session.Frame) {
	svc, ok := m.services[f.Envelope.ServiceID]
	if !ok {
		return // unknown service: a newer peer is running something we don't have
	}
	k := seqKey{from: f.From, service: f.Envelope.ServiceID}
	if last := m.lastSeq[k]; f.Envelope.Seq != last+1 {
		m.emit(Desync{From: f.From, Service: k.service, Want: last + 1, Got: f.Envelope.Seq})
	}
	m.lastSeq[k] = f.Envelope.Seq
	if err := svc.HandleFrame(f.From, f.Envelope.Body); err != nil {
		m.emit(ServiceError{From: f.From, Service: k.service, Err: err})
	}
}
