package gin

import (
	"fmt"

	"github.com/richardwooding/kibitz/internal/ginrummy"
	"github.com/richardwooding/kibitz/internal/mentalpoker"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/wire"
)

// snapshot is the PUBLIC state for late joiners / spectators — no keys, no
// hands, no deck. A late joiner can watch the discard pile and counts only.
type snapshot struct {
	P1         uint32 `cbor:"1,keyasint"`
	P2         uint32 `cbor:"2,keyasint"`
	Phase      uint8  `cbor:"3,keyasint"`
	Turn       uint8  `cbor:"4,keyasint"`
	Discard    []int8 `cbor:"5,keyasint,omitempty"`
	HandCounts [2]int `cbor:"6,keyasint"`
	StockNext  int    `cbor:"7,keyasint"`
	Scores     [2]int `cbor:"8,keyasint"`
	Outcome    string `cbor:"9,keyasint,omitempty"`
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == phIdle {
		return nil, nil
	}
	return wire.Marshal(snapshot{
		P1: uint32(s.table.Seats.P1), P2: uint32(s.table.Seats.P2),
		Phase: uint8(s.ph), Turn: uint8(s.turn), Discard: s.discard,
		HandCounts: s.handCounts, StockNext: s.stockNext, Scores: s.scores,
		Outcome: s.outcome,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("gin: restore: %w", err)
	}
	s.mu.Lock()
	if s.ph != phIdle { // late-joiner catch-up only; a live player keeps its secrets
		s.mu.Unlock()
		return nil
	}
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.ph = phase(snap.Phase)
	s.turn = game.Side(snap.Turn)
	s.discard = snap.Discard
	s.handCounts = snap.HandCounts
	s.stockNext = snap.StockNext
	s.scores = snap.Scores
	s.outcome = snap.Outcome
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.ctx.Emit(st)
}

var phaseNames = map[phase]string{
	phShuffle: "shuffling", phDraw: "draw", phDiscard: "discard",
	phShowdown: "showdown", phOver: "over",
}

func (s *Service) stateLocked() State {
	if s.ph == phIdle {
		return State{}
	}
	side, seated := s.mySeat()
	st := State{
		Playing:    true,
		Phase:      phaseNames[s.ph],
		P1ID:       s.table.Seats.P1,
		P2ID:       s.table.Seats.P2,
		Discard:    append([]int8(nil), s.discard...),
		HandCounts: s.handCounts,
		StockCount: stockRemaining(s.stockNext),
		Scores:     s.scores,
		Outcome:    s.outcome,
		Verified:   s.verified,
	}
	if s.ph != phOver && s.ph != phShowdown {
		st.TurnID = s.table.Seats.IDOf(s.turn)
	}
	if seated {
		st.Hand = append([]int8(nil), s.hand...)
		st.Deadwood = ginrummy.Deadwood(toInts(s.hand))
		st.CanKnock = s.canKnockLocked(side)
	}
	if s.ph == phOver {
		st.OppHand = s.revealHands[opponentOf(side, seated)]
	}
	return st
}

func stockRemaining(next int) int {
	r := (stockStart + 31) - next // 31 stock cards total (52 - 20 - 1)
	if r < 0 {
		return 0
	}
	return r
}

func opponentOf(side game.Side, seated bool) game.Side {
	if !seated {
		return game.P2 // spectator: show P2's revealed hand as "opp" arbitrarily
	}
	return side.Opponent()
}

// canKnockLocked reports whether this end may knock: it is in the discard phase
// on its turn holding 11 cards, and some discard leaves deadwood <= 10.
func (s *Service) canKnockLocked(side game.Side) bool {
	if s.ph != phDiscard || s.turn != side || len(s.hand) != handSize+1 {
		return false
	}
	for _, c := range s.hand {
		if ginrummy.CanKnock(toInts(removeCard(append([]int8(nil), s.hand...), c))) {
			return true
		}
	}
	return false
}

// finalizeShowdownLocked scores + verifies once both hands are revealed.
func (s *Service) finalizeShowdownLocked() {
	if s.revealHands[game.P1] == nil || s.revealHands[game.P2] == nil {
		return
	}
	k := s.knockSeat
	o := k.Opponent()
	gin := ginrummy.IsGin(toInts(s.revealHands[k]))
	kp, op := ginrummy.Score(toInts(s.revealHands[k]), toInts(s.revealHands[o]), gin)
	s.scores[k] += kp
	s.scores[o] += op
	s.verified = s.verifyLocked()
	s.outcome = s.outcomeTextLocked(k, kp, op, gin)
	s.ph = phOver
}

// verifyLocked re-derives both keys from the revealed exponents and checks the
// shuffled deck decrypts to exactly the 52 tokens — the fairness proof. Only
// ends holding deck2 (the players) can run it.
func (s *Service) verifyLocked() bool {
	if len(s.deck2) != mentalpoker.DeckSize || s.revealExp[game.P1] == nil || s.revealExp[game.P2] == nil {
		return false
	}
	ka, ok1 := mentalpoker.KeyFromExponent(bigFromBytes(s.revealExp[game.P1]))
	kb, ok2 := mentalpoker.KeyFromExponent(bigFromBytes(s.revealExp[game.P2]))
	if !ok1 || !ok2 {
		return false
	}
	return mentalpoker.VerifyDeck(s.deck2, ka, kb)
}

func (s *Service) outcomeTextLocked(knocker game.Side, kp, op int, gin bool) string {
	side, seated := s.mySeat()
	tag := "Knock"
	if gin {
		tag = "Gin!"
	}
	if !seated {
		return fmt.Sprintf("%s — %s +%d", tag, seatName(knocker), kp+op)
	}
	won := (side == knocker && kp > 0) || (side != knocker && op > 0)
	pts := kp
	if side != knocker {
		pts = op
	}
	if op > 0 && side != knocker {
		tag = "Undercut!"
	}
	if won {
		return fmt.Sprintf("%s You win the hand +%d", tag, pts)
	}
	return fmt.Sprintf("%s You lose the hand", tag)
}

func seatName(side game.Side) string {
	if side == game.P1 {
		return "P1"
	}
	return "P2"
}
