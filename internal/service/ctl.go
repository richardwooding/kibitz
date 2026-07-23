package service

import (
	"fmt"

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
)

type ctlMsg struct {
	Kind      uint8             `cbor:"1,keyasint"`
	Roster    map[uint32]uint8  `cbor:"2,keyasint,omitempty"`
	Services  []ServiceInfo     `cbor:"3,keyasint,omitempty"`
	Snapshots map[string][]byte `cbor:"4,keyasint,omitempty"`
}

// ServiceInfo names one service a session end is running.
type ServiceInfo struct {
	ID      string `cbor:"1,keyasint"`
	Version int    `cbor:"2,keyasint"`
}

// Roster is the ctl service's event: current membership with roles, plus
// what services the host runs.
type Roster struct {
	Members  map[wire.ParticipantID]session.Role
	Services []ServiceInfo
}

type ctlService struct {
	mux *Mux
	ctx Context
	// roster is host-authoritative; joiners hold the last announced copy.
	roster map[wire.ParticipantID]session.Role
}

func newCtl(m *Mux) *ctlService {
	return &ctlService{mux: m, roster: map[wire.ParticipantID]session.Role{}}
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
		c.mux.emit(Roster{Members: c.rosterCopy(), Services: msg.Services})
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
	c.announce()
}

func (c *ctlService) announce() {
	roster := map[uint32]uint8{}
	for id, r := range c.roster {
		roster[uint32(id)] = uint8(r)
	}
	var infos []ServiceInfo
	for _, s := range c.mux.services {
		infos = append(infos, ServiceInfo{ID: s.ID(), Version: s.Version()})
	}
	body, err := wire.Marshal(ctlMsg{Kind: ctlKindAnnounce, Roster: roster, Services: infos})
	if err != nil {
		return
	}
	_ = c.ctx.Send.Broadcast(CtlID, body)
	// The host's own UI wants the roster too.
	c.mux.emit(Roster{Members: c.rosterCopy(), Services: infos})
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
