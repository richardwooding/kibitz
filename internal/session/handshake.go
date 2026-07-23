package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/richardwooding/kibitz/internal/crypto"
	"github.com/richardwooding/kibitz/internal/wire"
)

// hostHello creates the session on the relay and generates the group key.
func (c *Client) hostHello(ctx context.Context) error {
	if err := c.writeFrame(wire.MsgCreateSession, wire.CreateSession{SessionID: c.sid}); err != nil {
		return err
	}
	raw, err := c.awaitReply(ctx, wire.MsgSessionCreated)
	if err != nil {
		return err
	}
	sc, err := wire.Body[wire.SessionCreated](raw)
	if err != nil {
		return err
	}
	c.self = sc.ParticipantID
	c.hostID = sc.ParticipantID

	key, err := crypto.NewGroupKey()
	if err != nil {
		return err
	}
	c.groupKey = key
	c.keyed = true
	c.role = RoleHost
	return nil
}

// joinHello joins the session and runs the PAKE + group-key handshake to
// completion, so Join returns a usable client or a clean error.
func (c *Client) joinHello(ctx context.Context) error {
	if err := c.writeFrame(wire.MsgJoinSession, wire.JoinSession{SessionID: c.sid}); err != nil {
		return err
	}
	raw, err := c.awaitReply(ctx, wire.MsgJoinResult)
	if err != nil {
		return err
	}
	jr, err := wire.Body[wire.JoinResult](raw)
	if err != nil {
		return err
	}
	if !jr.OK {
		return fmt.Errorf("session: join refused: %s", jr.Err)
	}
	c.self = jr.ParticipantID
	c.hostID = jr.HostID

	j, err := crypto.NewJoiner(c.phraseC)
	if err != nil {
		return err
	}
	payload, err := wire.EncodePayload(wire.KindPake1, wire.Pake{Data: j.Flight1})
	if err != nil {
		return err
	}
	if err := c.writeFrame(wire.MsgDirect, wire.Direct{To: c.hostID, Payload: payload}); err != nil {
		return err
	}

	// Drive the read side until keyed: Pake2 then GroupKey, both from the
	// host. Anything else that arrives mid-handshake (membership notices,
	// early broadcasts we can't decrypt yet) is skipped — the ctl snapshot
	// catches joiners up once keyed.
	var pairwise crypto.Key
	havePairwise := false
	for !c.keyed {
		typ, raw, err := c.readFrame(ctx)
		if err != nil {
			return err
		}
		if typ == wire.MsgSessionClosed {
			return errors.New("session: closed during handshake")
		}
		if typ != wire.MsgDirect {
			continue
		}
		d, err := wire.Body[wire.Direct](raw)
		if err != nil || d.From != c.hostID {
			continue
		}
		kind, praw, err := wire.DecodePayload(d.Payload)
		if err != nil {
			continue
		}
		switch kind {
		case wire.KindPake2:
			p, err := wire.Body[wire.Pake](praw)
			if err != nil {
				return err
			}
			pairwise, err = j.Finish(p.Data, c.sid, c.self, c.hostID)
			if err != nil {
				return err
			}
			havePairwise = true
		case wire.KindGroupKey:
			if !havePairwise {
				return errors.New("session: group key arrived before pake reply")
			}
			gk, err := wire.Body[wire.GroupKey](praw)
			if err != nil {
				return err
			}
			key, role, err := crypto.UnwrapGroupKey(pairwise, gk, c.sid, c.self)
			if err != nil {
				return err // crypto.ErrUnwrap: wrong phrase
			}
			c.groupKey = key
			c.role = Role(role)
			c.keyed = true
		}
	}
	return nil
}

// awaitReply reads until the wanted hello reply arrives, returning its raw
// body. Session traffic that races ahead of the reply (undecryptable this
// early; the ctl snapshot catches us up) is skipped; relay errors surface.
func (c *Client) awaitReply(ctx context.Context, want wire.MsgType) ([]byte, error) {
	for {
		typ, raw, err := c.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		switch typ {
		case want:
			return raw, nil
		case wire.MsgError:
			return nil, relayError(raw)
		case wire.MsgBroadcast, wire.MsgDirect, wire.MsgParticipantJoined, wire.MsgParticipantLeft:
			continue
		default:
			return nil, fmt.Errorf("session: unexpected reply %v awaiting %v", typ, want)
		}
	}
}

// handleHandshakeDirect is the host side: a Pake1 from a joiner triggers the
// stateless exchange — reply Pake2, then the wrapped group key with the
// joiner's assigned role.
func (c *Client) handleHandshakeDirect(from wire.ParticipantID, kind wire.PayloadKind, praw []byte) {
	if c.role != RoleHost || kind != wire.KindPake1 {
		return
	}
	p, err := wire.Body[wire.Pake](praw)
	if err != nil {
		return
	}
	pairwise, flight2, err := crypto.HostExchange(c.phraseC, p.Data, c.sid, from, c.self)
	if err != nil {
		return
	}

	role := RoleSpectator
	c.mu.Lock()
	hasPlayer := false
	for _, r := range c.joiners {
		if r == RolePlayer {
			hasPlayer = true
			break
		}
	}
	if !hasPlayer {
		role = RolePlayer
	}
	c.joiners[from] = role
	key := c.groupKey
	c.mu.Unlock()

	reply, err := wire.EncodePayload(wire.KindPake2, wire.Pake{Data: flight2})
	if err != nil {
		return
	}
	if c.writeFrame(wire.MsgDirect, wire.Direct{To: from, Payload: reply}) != nil {
		return
	}
	wrapped, err := crypto.WrapGroupKey(pairwise, key, byte(role), c.sid, from)
	if err != nil {
		return
	}
	gkPayload, err := wire.EncodePayload(wire.KindGroupKey, wrapped)
	if err != nil {
		return
	}
	if c.writeFrame(wire.MsgDirect, wire.Direct{To: from, Payload: gkPayload}) != nil {
		return
	}
	c.emit(MemberKeyed{ID: from, Role: role})
}

// readLoop pumps relay frames into events until the connection dies.
func (c *Client) readLoop() {
	defer close(c.events)
	ctx := context.Background()
	for {
		typ, raw, err := c.readFrame(ctx)
		if err != nil {
			c.emit(Closed{Reason: "connection lost"})
			return
		}
		switch typ {
		case wire.MsgParticipantJoined:
			if pj, err := wire.Body[wire.ParticipantJoined](raw); err == nil {
				c.emit(MemberJoined{ID: pj.ParticipantID})
			}
		case wire.MsgParticipantLeft:
			if pl, err := wire.Body[wire.ParticipantLeft](raw); err == nil {
				c.mu.Lock()
				delete(c.joiners, pl.ParticipantID)
				c.mu.Unlock()
				c.emit(MemberLeft{ID: pl.ParticipantID})
			}
		case wire.MsgSessionClosed:
			reason := ""
			if sc, err := wire.Body[wire.SessionClosed](raw); err == nil {
				reason = sc.Reason
			}
			c.emit(Closed{Reason: reason})
			return
		case wire.MsgDirect:
			if d, err := wire.Body[wire.Direct](raw); err == nil {
				c.handlePayload(d.From, d.Payload)
			}
		case wire.MsgBroadcast:
			if b, err := wire.Body[wire.Broadcast](raw); err == nil {
				c.handlePayload(b.From, b.Payload)
			}
		}
	}
}

// handlePayload routes one inner payload: handshake kinds to the host
// responder, sealed frames through decryption to a Frame event.
func (c *Client) handlePayload(from wire.ParticipantID, payload []byte) {
	kind, praw, err := wire.DecodePayload(payload)
	if err != nil {
		return
	}
	if kind != wire.KindSealed {
		c.handleHandshakeDirect(from, kind, praw)
		return
	}
	sf, err := wire.Body[wire.SealedFrame](praw)
	if err != nil {
		return
	}
	c.mu.Lock()
	key, keyed := c.groupKey, c.keyed
	c.mu.Unlock()
	if !keyed {
		return
	}
	plain, err := crypto.Open(key, sf, c.sid, from)
	if err != nil {
		return // tampered or not-for-this-session; drop silently
	}
	env, err := wire.Body[wire.Envelope](plain)
	if err != nil {
		return
	}
	c.emit(Frame{From: from, Envelope: env})
}

func relayError(raw []byte) error {
	e, err := wire.Body[wire.Error](raw)
	if err != nil {
		return errors.New("session: relay error")
	}
	return fmt.Errorf("session: relay error %d: %s", e.Code, e.Msg)
}
