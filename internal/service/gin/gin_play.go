package gin

import (
	"errors"
	"math/big"

	"github.com/richardwooding/kibitz/internal/ginrummy"
	"github.com/richardwooding/kibitz/internal/mentalpoker"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/wire"
)

func bigFromBytes(b []byte) *big.Int { return new(big.Int).SetBytes(b) }

func toInts(h []int8) []int {
	out := make([]int, len(h))
	for i, c := range h {
		out[i] = int(c)
	}
	return out
}

// decodePartials strips this end's key from each partial (m^{myKey}) to recover
// the card index.
func (s *Service) decodePartials(partials [][]byte) []int8 {
	out := make([]int8, 0, len(partials))
	for _, p := range partials {
		out = append(out, int8(mentalpoker.Decode(s.key.Decrypt(bigFromBytes(p)))))
	}
	return out
}

// --- shuffle handshake ------------------------------------------------------

func (s *Service) handleShuffle1(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return errors.New("gin: shuffle1 from non-host")
	}
	key, err := mentalpoker.NewKey()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if m.NewMatch {
		s.scores = [2]int{}
		s.handsWon = [2]int{}
		s.matchOver = false
	}
	s.dealer = game.Side(m.Dealer)
	s.resetHandLocked(gameSeats(m), key)
	deck2 := mentalpoker.EncryptAll(key, mentalpoker.Unmarshal(m.Deck))
	if err := mentalpoker.Shuffle(deck2); err != nil {
		s.mu.Unlock()
		return err
	}
	s.deck2 = deck2
	partials := make([][]byte, 0, handSize) // host(P1) hand, stripped of my key
	for j := p1Lo; j < p1Hi; j++ {
		partials = append(partials, key.Decrypt(deck2[j]).Bytes())
	}
	up := key.Decrypt(deck2[upcardPos]).Bytes()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindShuffle2, Deck: mentalpoker.Marshal(deck2), Partials: partials, Upcard: up})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleShuffle2(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	if !s.Ctx().Host || s.ph != phShuffle {
		s.mu.Unlock()
		return nil
	}
	s.deck2 = mentalpoker.Unmarshal(m.Deck)
	s.hand = s.decodePartials(m.Partials) // my (P1) hand
	s.upcard = int8(mentalpoker.Decode(s.key.Decrypt(bigFromBytes(m.Upcard))))
	s.discard = []int8{s.upcard}
	partials := make([][]byte, 0, handSize) // P2 hand, stripped of my key
	for j := p2Lo; j < p2Hi; j++ {
		partials = append(partials, s.key.Decrypt(s.deck2[j]).Bytes())
	}
	s.handCounts = [2]int{handSize, handSize}
	s.ph = phUpcardOffer // non-dealer decides on the upcard first
	up := s.upcard
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindDeal, Partials: partials, Card: up})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleDeal(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return nil
	}
	s.mu.Lock()
	if s.ph != phShuffle {
		s.mu.Unlock()
		return nil
	}
	s.hand = s.decodePartials(m.Partials) // my (P2) hand
	s.upcard = m.Card
	s.discard = []int8{m.Card}
	s.handCounts = [2]int{handSize, handSize}
	s.ph = phUpcardOffer // non-dealer decides on the upcard first
	s.mu.Unlock()
	s.emitState()
	return nil
}

// --- opening upcard offer ---------------------------------------------------
//
// After the deal, the non-dealer may take the initial upcard or pass; if it
// passes, the dealer gets the same choice. If both pass, the non-dealer draws
// from stock to open play (for simplicity it may also re-take the upcard then).

// advanceOfferLocked records a pass and moves the offer along; after the second
// pass the opening is over and the non-dealer is on to draw.
func (s *Service) advanceOfferLocked() {
	s.offerPasses++
	if s.offerPasses >= 2 {
		s.ph = phDraw
		s.turn = s.dealer.Opponent() // non-dealer opens play
		return
	}
	s.turn = s.turn.Opponent() // offer passes to the dealer
}

// TakeUpcardOffer accepts the opening upcard (equivalent to a first-turn take).
func (s *Service) TakeUpcardOffer() error {
	s.mu.Lock()
	side, ok := s.mySeat()
	if !ok || s.ph != phUpcardOffer || s.turn != side {
		s.mu.Unlock()
		return errNotYourTurn
	}
	if len(s.discard) == 0 {
		s.mu.Unlock()
		return errors.New("gin: empty discard")
	}
	card := s.discard[len(s.discard)-1]
	s.discard = s.discard[:len(s.discard)-1]
	s.hand = append(s.hand, card)
	s.handCounts[side]++
	s.ph = phDiscard
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindUpcardTake})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// PassUpcard declines the opening upcard.
func (s *Service) PassUpcard() error {
	s.mu.Lock()
	side, ok := s.mySeat()
	if !ok || s.ph != phUpcardOffer || s.turn != side {
		s.mu.Unlock()
		return errNotYourTurn
	}
	s.advanceOfferLocked()
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindUpcardPass})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleUpcardTake(from wire.ParticipantID) error {
	s.mu.Lock()
	side, ok := s.table.Seats.SideOf(from)
	if !ok || s.ph != phUpcardOffer || s.turn != side || len(s.discard) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.discard = s.discard[:len(s.discard)-1]
	s.handCounts[side]++
	s.ph = phDiscard
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleUpcardPass(from wire.ParticipantID) error {
	s.mu.Lock()
	side, ok := s.table.Seats.SideOf(from)
	if !ok || s.ph != phUpcardOffer || s.turn != side {
		s.mu.Unlock()
		return nil
	}
	s.advanceOfferLocked()
	s.mu.Unlock()
	s.emitState()
	return nil
}

// --- draw -------------------------------------------------------------------

func (s *Service) checkDrawLocked() error {
	side, ok := s.mySeat()
	if !ok || s.ph != phDraw || s.turn != side || s.pendPos != -1 {
		return errNotYourTurn
	}
	return nil
}

// DrawStock requests the next stock card; the opponent strips its key and
// replies with a partial that only this end can finish decrypting.
func (s *Service) DrawStock() error {
	s.mu.Lock()
	if err := s.checkDrawLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.stockNext >= len(s.deck2) {
		s.mu.Unlock()
		return errors.New("gin: stock empty")
	}
	pos := int8(s.stockNext)
	s.pendPos = pos
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindDrawReq, Pos: pos})
	if err != nil {
		return err
	}
	return s.Ctx().Send.Broadcast(ID, body)
}

// TakeUpcard draws the (public) top of the discard pile.
func (s *Service) TakeUpcard() error {
	s.mu.Lock()
	if err := s.checkDrawLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if len(s.discard) == 0 {
		s.mu.Unlock()
		return errors.New("gin: empty discard")
	}
	card := s.discard[len(s.discard)-1]
	s.discard = s.discard[:len(s.discard)-1]
	s.hand = append(s.hand, card)
	side, _ := s.mySeat()
	s.handCounts[side]++
	s.ph = phDiscard
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakeDiscard})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleTakeDiscard(from wire.ParticipantID) error {
	s.mu.Lock()
	side, ok := s.table.Seats.SideOf(from)
	if !ok || s.ph != phDraw || s.turn != side || len(s.discard) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.discard = s.discard[:len(s.discard)-1]
	s.handCounts[side]++
	s.ph = phDiscard
	s.mu.Unlock()
	s.emitState()
	return nil
}

// handleDrawReq: only the seated opponent (with a key) strips and replies.
func (s *Service) handleDrawReq(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, seatedSelf := s.mySeat()
	drawer, ok := s.table.Seats.SideOf(from)
	pos := int(m.Pos)
	if !ok || !seatedSelf || !s.haveKey || drawer == side || s.ph != phDraw || s.turn != drawer || pos < 0 || pos >= len(s.deck2) {
		s.mu.Unlock()
		return nil
	}
	val := s.key.Decrypt(s.deck2[pos]).Bytes()
	s.handCounts[drawer]++
	s.stockNext = pos + 1
	s.ph = phDiscard
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindDrawPartial, Pos: m.Pos, Val: val})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// handleDrawPartial: drawer + spectators apply the public delta; the drawer also
// finishes decrypting its new card.
func (s *Service) handleDrawPartial(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	drawer := s.turn
	pos := int(m.Pos)
	s.handCounts[drawer]++
	s.stockNext = pos + 1
	s.ph = phDiscard
	if side, ok := s.mySeat(); ok && side == drawer && s.pendPos == int8(pos) {
		card := int8(mentalpoker.Decode(s.key.Decrypt(bigFromBytes(m.Val))))
		s.hand = append(s.hand, card)
		s.pendPos = -1
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// --- discard ----------------------------------------------------------------

func (s *Service) checkDiscardLocked(card int8) error {
	side, ok := s.mySeat()
	if !ok || s.ph != phDiscard || s.turn != side {
		return errNotYourTurn
	}
	if !contains(s.hand, card) {
		return errors.New("gin: card not in hand")
	}
	return nil
}

func (s *Service) Discard(card int8) error {
	s.mu.Lock()
	if err := s.checkDiscardLocked(card); err != nil {
		s.mu.Unlock()
		return err
	}
	s.hand = removeCard(s.hand, card)
	side, _ := s.mySeat()
	s.handCounts[side]--
	s.discard = append(s.discard, card)
	s.turn = s.turn.Opponent()
	s.ph = phDraw
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindDiscard, Card: card})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleDiscard(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, ok := s.table.Seats.SideOf(from)
	if !ok || s.ph != phDiscard || s.turn != side {
		s.mu.Unlock()
		return nil
	}
	s.handCounts[side]--
	s.discard = append(s.discard, m.Card)
	s.turn = s.turn.Opponent()
	s.ph = phDraw
	s.mu.Unlock()
	s.emitState()
	return nil
}

// --- knock / showdown -------------------------------------------------------

// Knock ends the hand: discard `card`, revealing a 10-card hand with deadwood
// <= 10, and reveal this end's key for verification.
func (s *Service) Knock(card int8) error {
	s.mu.Lock()
	if err := s.checkDiscardLocked(card); err != nil {
		s.mu.Unlock()
		return err
	}
	tentative := removeCard(append([]int8(nil), s.hand...), card)
	if !ginrummy.CanKnock(toInts(tentative)) {
		s.mu.Unlock()
		return errors.New("gin: deadwood too high to knock")
	}
	side, _ := s.mySeat()
	s.hand = tentative
	s.handCounts[side]--
	s.discard = append(s.discard, card)
	exp := s.key.Exponent().Bytes()
	s.knockSeat = side
	s.revealHands[side] = append([]int8(nil), tentative...)
	s.revealExp[side] = exp
	s.ph = phShowdown
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindKnock, Card: card, Hand: s.revealHands[side], Exp: exp})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleKnock(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	kSide, ok := s.table.Seats.SideOf(from)
	if !ok {
		s.mu.Unlock()
		return nil
	}
	s.discard = append(s.discard, m.Card)
	s.handCounts[kSide]--
	s.knockSeat = kSide
	s.revealHands[kSide] = m.Hand
	s.revealExp[kSide] = m.Exp
	s.ph = phShowdown
	side, seated := s.mySeat()
	amOpp := seated && side != kSide
	if amOpp {
		s.revealHands[side] = append([]int8(nil), s.hand...)
		s.revealExp[side] = s.key.Exponent().Bytes()
	}
	s.finalizeShowdownLocked()
	revealHand := s.revealHands[side]
	revealExp := s.revealExp[side]
	s.mu.Unlock()

	if amOpp {
		body, err := wire.Marshal(msg{Kind: kindReveal, Hand: revealHand, Exp: revealExp})
		if err != nil {
			return err
		}
		if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
			return err
		}
	}
	s.emitState()
	return nil
}

func (s *Service) handleReveal(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, ok := s.table.Seats.SideOf(from)
	if !ok {
		s.mu.Unlock()
		return nil
	}
	s.revealHands[side] = m.Hand
	s.revealExp[side] = m.Exp
	s.finalizeShowdownLocked()
	s.mu.Unlock()
	s.emitState()
	return nil
}

// Resign concedes the hand.
func (s *Service) Resign() error {
	s.mu.Lock()
	side, seated := s.mySeat()
	if !seated || s.ph == phIdle || s.ph == phOver {
		s.mu.Unlock()
		return errors.New("gin: nothing to resign")
	}
	s.scores[side.Opponent()] += 25
	s.outcome = "you resigned"
	s.ph = phOver
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph == phOver || s.ph == phIdle {
		s.mu.Unlock()
		return nil
	}
	s.scores[side.Opponent()] += 25
	s.outcome = "opponent resigned"
	s.ph = phOver
	s.mu.Unlock()
	s.emitState()
	return nil
}

func contains(h []int8, c int8) bool {
	for _, x := range h {
		if x == c {
			return true
		}
	}
	return false
}

func removeCard(h []int8, c int8) []int8 {
	for i, x := range h {
		if x == c {
			return append(h[:i], h[i+1:]...)
		}
	}
	return h
}
