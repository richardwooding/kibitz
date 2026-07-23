package service

import (
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Mux drains a session client's events, routes envelopes to services by ID
// (unknown IDs are ignored for forward compatibility), watches per-sender
// sequence numbers, and merges everything the UI cares about into one event
// stream.
type Mux struct {
	client   *session.Client
	services map[string]Service
	events   chan any
	lastSeq  map[seqKey]uint64
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

// NewMux attaches services to the client and starts routing. The ctl service
// is always registered. Call Events for the merged stream; it closes when
// the session ends.
func NewMux(c *session.Client, svcs ...Service) *Mux {
	m := &Mux{
		client:   c,
		services: map[string]Service{},
		events:   make(chan any, 256),
		lastSeq:  map[seqKey]uint64{},
	}
	ctl := newCtl(m)
	all := append([]Service{ctl}, svcs...)
	ctx := Context{Send: c, Emit: m.emit, Self: c.Self(), HostID: c.HostID(), Host: c.Role() == session.RoleHost}
	for _, s := range all {
		m.services[s.ID()] = s
		s.Attach(ctx)
	}
	go m.run(ctl)
	return m
}

// Events is the merged stream: SessionEvent, Desync, and every service's
// own event types (chat.Message, ctl Roster, …).
func (m *Mux) Events() <-chan any { return m.events }

func (m *Mux) emit(e any) { m.events <- e }

func (m *Mux) run(ctl *ctlService) {
	defer close(m.events)

	// A fresh joiner asks the host for state it missed.
	if m.client.Role() != session.RoleHost {
		ctl.requestSnapshot()
	}

	for ev := range m.client.Events() {
		switch e := ev.(type) {
		case session.Frame:
			m.handleFrame(e)
		case session.MemberKeyed:
			for _, s := range m.services {
				if o, ok := s.(MemberObserver); ok {
					o.MemberKeyed(e.ID, e.Role)
				}
			}
			m.emit(SessionEvent{Event: e})
		case session.MemberLeft:
			for _, s := range m.services {
				if o, ok := s.(MemberObserver); ok {
					o.MemberLeft(e.ID)
				}
			}
			m.emit(SessionEvent{Event: e})
		case session.Closed:
			m.emit(SessionEvent{Event: e})
			return
		default:
			m.emit(SessionEvent{Event: ev})
		}
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
