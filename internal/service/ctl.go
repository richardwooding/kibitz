package service

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// CtlID is the reserved control service, present in every session. The host
// broadcasts the roster (roles live inside the encrypted channel — the relay
// never learns them) and answers snapshot requests so late joiners catch up.
const CtlID = "ctl"

const (
	ctlKindAnnounce    uint8 = 1
	ctlKindSnapshotReq uint8 = 2
	ctlKindSnapshot    uint8 = 3
	ctlKindIdentity    uint8 = 4 // participant → host: my screen name
)

// maxNameLen caps a screen name (runes). Names are self-asserted display
// labels, distributed inside the encrypted ctl channel — the relay never
// sees them.
const maxNameLen = 24

type ctlMsg struct {
	Kind      uint8             `cbor:"1,keyasint"`
	Roster    map[uint32]uint8  `cbor:"2,keyasint,omitempty"`
	Services  []ServiceInfo     `cbor:"3,keyasint,omitempty"`
	Snapshots map[string][]byte `cbor:"4,keyasint,omitempty"`
	Name      string            `cbor:"5,keyasint,omitempty"` // identity: sender's name
	Names     map[uint32]string `cbor:"6,keyasint,omitempty"` // announce: id → name
}

// ServiceInfo names one service a session end is running.
type ServiceInfo struct {
	ID      string `cbor:"1,keyasint"`
	Version int    `cbor:"2,keyasint"`
}

// Roster is the ctl service's event: current membership with roles and
// screen names, plus what services the host runs.
type Roster struct {
	Members  map[wire.ParticipantID]session.Role
	Names    map[wire.ParticipantID]string
	Services []ServiceInfo
}

type ctlService struct {
	mux *Mux
	ctx Context
	// roster + names are host-authoritative; joiners hold the last announced
	// copy. Names are self-asserted (each participant reports its own).
	roster   map[wire.ParticipantID]session.Role
	names    map[wire.ParticipantID]string
	selfName string
}

func newCtl(m *Mux) *ctlService {
	return &ctlService{
		mux:    m,
		roster: map[wire.ParticipantID]session.Role{},
		names:  map[wire.ParticipantID]string{},
	}
}

// sanitizeName trims, strips control characters, and caps the length.
func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len([]rune(s)) > maxNameLen {
		s = string([]rune(s)[:maxNameLen])
	}
	return s
}

// setName sets the local participant's screen name and distributes it: the
// host records it and re-announces the roster; a joiner reports it to the
// host, which folds it into the authoritative roster.
func (c *ctlService) setName(name string) {
	name = sanitizeName(name)
	if name == "" {
		return
	}
	c.selfName = name
	c.names[c.ctx.Self] = name
	if c.ctx.Host {
		c.announce()
		return
	}
	if body, err := wire.Marshal(ctlMsg{Kind: ctlKindIdentity, Name: name}); err == nil {
		_ = c.ctx.Send.SendTo(c.ctx.HostID, CtlID, body)
	}
}

func (c *ctlService) ID() string   { return CtlID }
func (c *ctlService) Version() int { return 1 }

func (c *ctlService) Attach(ctx Context) {
	c.ctx = ctx
	if ctx.Host {
		c.roster[ctx.Self] = session.RoleHost
	}
}

func (c *ctlService) HandleFrame(from wire.ParticipantID, body []byte) error {
	msg, err := wire.Body[ctlMsg](body)
	if err != nil {
		return fmt.Errorf("ctl: %w", err)
	}
	switch msg.Kind {
	case ctlKindAnnounce:
		if from != c.ctx.HostID {
			return fmt.Errorf("ctl: announce from non-host %d", from)
		}
		c.roster = map[wire.ParticipantID]session.Role{}
		for id, r := range msg.Roster {
			c.roster[wire.ParticipantID(id)] = session.Role(r)
		}
		c.names = map[wire.ParticipantID]string{}
		for id, n := range msg.Names {
			c.names[wire.ParticipantID(id)] = n
		}
		c.mux.emit(Roster{Members: c.rosterCopy(), Names: c.namesCopy(), Services: msg.Services})
	case ctlKindIdentity:
		// Only the host aggregates names; a participant reports its own.
		if !c.ctx.Host {
			return nil
		}
		c.names[from] = sanitizeName(msg.Name)
		c.announce()
	case ctlKindSnapshotReq:
		if !c.ctx.Host {
			return nil
		}
		return c.sendSnapshot(from)
	case ctlKindSnapshot:
		if from != c.ctx.HostID {
			return fmt.Errorf("ctl: snapshot from non-host %d", from)
		}
		for id, blob := range msg.Snapshots {
			if svc, ok := c.mux.services[id]; ok && id != CtlID {
				if err := svc.Restore(blob); err != nil {
					return fmt.Errorf("ctl: restore %s: %w", id, err)
				}
			}
		}
	}
	return nil
}

// Snapshot/Restore: the ctl's own state (the roster) travels in announces,
// not snapshots.
func (c *ctlService) Snapshot() ([]byte, error) { return nil, nil }
func (c *ctlService) Restore([]byte) error      { return nil }

// MemberKeyed / MemberLeft: host-side roster maintenance + announce.
func (c *ctlService) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !c.ctx.Host {
		return
	}
	c.roster[id] = role
	c.announce()
}

func (c *ctlService) MemberLeft(id wire.ParticipantID) {
	if !c.ctx.Host {
		return
	}
	delete(c.roster, id)
	delete(c.names, id)
	c.announce()
}

func (c *ctlService) announce() {
	roster := map[uint32]uint8{}
	for id, r := range c.roster {
		roster[uint32(id)] = uint8(r)
	}
	names := map[uint32]string{}
	for id, n := range c.names {
		if _, seated := c.roster[id]; seated { // only announce names of current members
			names[uint32(id)] = n
		}
	}
	var infos []ServiceInfo
	for _, s := range c.mux.services {
		infos = append(infos, ServiceInfo{ID: s.ID(), Version: s.Version()})
	}
	body, err := wire.Marshal(ctlMsg{Kind: ctlKindAnnounce, Roster: roster, Names: names, Services: infos})
	if err != nil {
		return
	}
	_ = c.ctx.Send.Broadcast(CtlID, body)
	// The host's own UI wants the roster too.
	c.mux.emit(Roster{Members: c.rosterCopy(), Names: c.namesCopy(), Services: infos})
}

func (c *ctlService) requestSnapshot() {
	body, err := wire.Marshal(ctlMsg{Kind: ctlKindSnapshotReq})
	if err != nil {
		return
	}
	_ = c.ctx.Send.SendTo(c.ctx.HostID, CtlID, body)
}

func (c *ctlService) sendSnapshot(to wire.ParticipantID) error {
	blobs := map[string][]byte{}
	for id, svc := range c.mux.services {
		if id == CtlID {
			continue
		}
		b, err := svc.Snapshot()
		if err != nil {
			return fmt.Errorf("ctl: snapshot %s: %w", id, err)
		}
		if len(b) > 0 {
			blobs[id] = b
		}
	}
	body, err := wire.Marshal(ctlMsg{Kind: ctlKindSnapshot, Snapshots: blobs})
	if err != nil {
		return err
	}
	return c.ctx.Send.SendTo(to, CtlID, body)
}

func (c *ctlService) rosterCopy() map[wire.ParticipantID]session.Role {
	out := make(map[wire.ParticipantID]session.Role, len(c.roster))
	for k, v := range c.roster {
		out[k] = v
	}
	return out
}

func (c *ctlService) namesCopy() map[wire.ParticipantID]string {
	out := make(map[wire.ParticipantID]string, len(c.names))
	for k, v := range c.names {
		out[k] = v
	}
	return out
}
