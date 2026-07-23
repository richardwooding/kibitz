// Package game holds the seat/lifecycle logic every two-player game service
// shares: who sits where, who may start a game, rematch seat-swapping, and
// forfeit-on-leave. Logic only — no Sender, no mutex, no message I/O. The
// M1/M2 bugs all lived in send/receive ordering, so that stays explicit and
// visible in each game's service.go; this package is the part that is safe
// to share.
package game

import (
	"errors"

	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// Side identifies a seat, not a color: P1 is whichever role moves first in
// the specific game (white in chess, black in checkers/reversi, the opening
// roller in backgammon).
type Side uint8

const (
	P1 Side = 0
	P2 Side = 1
)

func (s Side) Opponent() Side { return 1 - s }

// Phase is the coarse lifecycle every game shares. Games with richer
// internal phases (backgammon's dice exchange, battleship's placement) map
// them onto Playing.
type Phase uint8

const (
	Idle    Phase = 0 // no game started yet (or between rematches)
	Playing Phase = 1
	Over    Phase = 2
)

// Seats maps sides to participants.
type Seats struct {
	P1 wire.ParticipantID
	P2 wire.ParticipantID
}

// SideOf reports which seat a participant holds.
func (s Seats) SideOf(id wire.ParticipantID) (Side, bool) {
	switch id {
	case s.P1:
		return P1, true
	case s.P2:
		return P2, true
	default:
		return 0, false
	}
}

// IDOf returns the participant in a seat.
func (s Seats) IDOf(side Side) wire.ParticipantID {
	if side == P1 {
		return s.P1
	}
	return s.P2
}

var (
	ErrNoOpponent   = errors.New("game: no opponent seated yet")
	ErrInProgress   = errors.New("game: a game is already in progress")
	ErrNotAuthority = errors.New("game: only the host starts games")
	ErrNotSeated    = errors.New("game: you are not seated at this game")
)

// Table is the per-service seat state, embedded by value in each game
// service (callers hold their own lock around it).
type Table struct {
	Seats    Seats
	Opponent wire.ParticipantID // host side: the first RolePlayer to key in
	Games    int                // completed games; drives rematch seat swap
}

// NoteKeyed records the first player to complete the handshake (host side).
// It no longer starts anything — games launch on demand via Start().
func (t *Table) NoteKeyed(id wire.ParticipantID, role session.Role) {
	if role == session.RolePlayer && t.Opponent == 0 {
		t.Opponent = id
	}
}

// NoteLeft handles a departure: if the leaver held a seat during a live
// game, the remaining side wins by forfeit. Reports (winningSide, true) when
// a forfeit applies.
func (t *Table) NoteLeft(id wire.ParticipantID, ph Phase) (Side, bool) {
	if id == t.Opponent {
		t.Opponent = 0
	}
	if ph != Playing {
		return 0, false
	}
	side, seated := t.Seats.SideOf(id)
	if !seated {
		return 0, false
	}
	return side.Opponent(), true
}

// AuthorizeStart validates a start attempt. The host is the only authority
// that actually seats players; a non-host player triggers the host's path
// via a startReq message, which lands here with host=true, from=the player.
func (t *Table) AuthorizeStart(host bool, from, hostID wire.ParticipantID, ph Phase) error {
	if !host {
		return ErrNotAuthority
	}
	if ph == Playing {
		return ErrInProgress
	}
	if t.Opponent == 0 {
		return ErrNoOpponent
	}
	if from != hostID && from != t.Opponent {
		return ErrNotSeated
	}
	return nil
}

// NextSeats assigns seats for the next game, alternating who takes P1 on
// each rematch, and bumps the games counter.
func (t *Table) NextSeats(hostID wire.ParticipantID) Seats {
	if t.Games%2 == 0 {
		t.Seats = Seats{P1: hostID, P2: t.Opponent}
	} else {
		t.Seats = Seats{P1: t.Opponent, P2: hostID}
	}
	t.Games++
	return t.Seats
}
