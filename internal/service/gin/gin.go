// Package gin is the Gin Rummy service: a two-player card game dealt with a
// dealerless "mental poker" shuffle (internal/mentalpoker) so neither player nor
// the blind relay ever learns the deck order or the opponent's hand, yet every
// card is verifiable at showdown. Rules/scoring come from internal/ginrummy.
//
// It is NOT a both-sides-validate game: the shuffle + deal + each stock draw are
// an interactive crypto handshake (like the PAKE/commit-reveal protocols), and
// each end holds SECRET local state (its key + hand) that is never serialized —
// so, like battleship, a player's secret survives a transient reconnect (same
// in-memory instance) but not a full rejoin, and spectators/late joiners see
// only public state (discard pile, counts, scores).
package gin

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/richardwooding/kibitz/internal/mentalpoker"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

const ID = "gin"

// Deck layout over the shuffled encrypted deck (deck2) positions:
//
//	0..9   -> player 1 (host) hand    10..19 -> player 2 hand
//	20     -> initial upcard          21..51 -> stock (drawn in order)
const (
	p1Lo, p1Hi = 0, 10
	p2Lo, p2Hi = 10, 20
	upcardPos  = 20
	stockStart = 21
	handSize   = 10
)

const (
	kindStartReq uint8 = iota + 1
	kindShuffle1
	kindShuffle2
	kindDeal
	kindTakeDiscard
	kindDrawReq
	kindDrawPartial
	kindDiscard
	kindKnock
	kindReveal
	kindResign
	kindUpcardTake
	kindUpcardPass
)

type msg struct {
	Kind     uint8    `cbor:"1,keyasint"`
	P1       uint32   `cbor:"2,keyasint,omitempty"`
	P2       uint32   `cbor:"3,keyasint,omitempty"`
	Deck     [][]byte `cbor:"4,keyasint,omitempty"`
	Partials [][]byte `cbor:"5,keyasint,omitempty"`
	Upcard   []byte   `cbor:"6,keyasint,omitempty"`
	Card     int8     `cbor:"7,keyasint,omitempty"`
	Pos      int8     `cbor:"8,keyasint,omitempty"`
	Val      []byte   `cbor:"9,keyasint,omitempty"`
	Hand     []int8   `cbor:"10,keyasint,omitempty"`
	Exp      []byte   `cbor:"11,keyasint,omitempty"`
	Dealer   int8     `cbor:"12,keyasint,omitempty"` // whose deal this hand (shuffle1)
	NewMatch bool     `cbor:"13,keyasint,omitempty"` // reset match scores (shuffle1)
}

// phase is the public game phase.
type phase uint8

const (
	phIdle phase = iota
	phShuffle
	phUpcardOffer // opening: non-dealer then dealer may take the upcard or pass
	phDraw        // current player must draw (stock or discard)
	phDiscard     // current player drew, must discard (or knock)
	phShowdown    // someone knocked; awaiting reveals
	phOver
)

// Match scoring: first to matchTarget wins; each hand won is a "box".
const (
	matchTarget = 100
	boxBonus    = 25
	gameBonus   = 100
)

// State is the public, blind-safe view emitted to the UI. Hand holds THIS end's
// own cards (secret to it); everyone else sees only counts.
type State struct {
	Playing     bool
	Phase       string // "shuffling" | "draw" | "discard" | "over"
	P1ID        wire.ParticipantID
	P2ID        wire.ParticipantID
	TurnID      wire.ParticipantID
	Hand        []int8 // this end's cards (empty for spectators)
	HandCounts  [2]int // [p1, p2] card counts
	Discard     []int8 // discard pile, top last
	StockCount  int
	Deadwood    int  // this end's current deadwood (if a player)
	CanKnock    bool // this end may knock now (in discard phase, deadwood<=10)
	Scores      [2]int
	Outcome     string             // e.g. "you win the hand +18", set when over
	Verified    bool               // showdown key-check passed (fair shuffle proven)
	OppHand     []int8             // revealed opponent hand at showdown
	DealerID    wire.ParticipantID // whose deal this hand
	HandsWon    [2]int             // hands won per seat (boxes)
	MatchTarget int                // points to win the match
	MatchOver   bool               // the whole match is decided
}

var errNotYourTurn = errors.New("gin: not your turn")

// Service implements service.Service.
type Service struct {
	service.Base

	mu    sync.Mutex
	table game.Table
	ph    phase
	turn  game.Side

	// public
	deck2      []*big.Int // doubly-encrypted shuffled stock, identical on both ends
	discard    []int8
	stockNext  int
	handCounts [2]int
	scores     [2]int // running match (line) scores
	outcome    string
	verified   bool
	oppHand    []int8

	// match play
	dealer      game.Side // whose deal this hand; non-dealer plays first, alternates
	handsWon    [2]int    // hands won per seat (box bonus)
	matchOver   bool      // a player reached matchTarget; the match is finished
	offerPasses int       // upcard-offer passes so far (0..2)

	// showdown bookkeeping
	knockSeat   game.Side
	revealHands [2][]int8
	revealExp   [2][]byte

	// SECRET, never serialized
	key     mentalpoker.Key
	haveKey bool
	hand    []int8
	upcard  int8
	pendPos int8 // stock position awaiting a partial (-1 none)
}

func New() *Service { return &Service{pendPos: -1} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.SetContext(ctx) }

func (s *Service) OnPromote() {
	s.mu.Lock()
	s.table.OnPromote()
	s.mu.Unlock()
}

func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.Ctx().Host {
		return
	}
	s.mu.Lock()
	s.table.NoteKeyed(id, role)
	s.mu.Unlock()
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	winner, forfeit := s.table.NoteLeft(id, gamePhase(s.ph))
	if forfeit {
		s.outcome = "opponent left"
		s.scoreForfeit(winner)
		s.ph = phOver
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

func (s *Service) scoreForfeit(winner game.Side) {
	s.scores[winner] += 25
}

// gamePhase maps our phase to game.Phase for the table's forfeit logic.
func gamePhase(p phase) game.Phase {
	switch p {
	case phIdle:
		return game.Idle
	case phOver:
		return game.Over
	default:
		return game.Playing
	}
}

// --- start / shuffle handshake ----------------------------------------------

// Start begins a new hand: the host generates a key, encrypts + shuffles a fresh
// deck, and sends it for the joiner to encrypt + shuffle in turn.
func (s *Service) Start() error {
	if !s.Ctx().Host {
		body, err := wire.Marshal(msg{Kind: kindStartReq})
		if err != nil {
			return err
		}
		return s.Ctx().Send.SendTo(s.Ctx().HostID, ID, body)
	}
	return s.hostStart(s.Ctx().Self)
}

func (s *Service) hostStart(from wire.ParticipantID) error {
	s.mu.Lock()
	if err := s.table.AuthorizeStart(s.Ctx().Host, from, s.Ctx().Self, gamePhase(s.ph)); err != nil {
		s.mu.Unlock()
		return err
	}
	key, err := mentalpoker.NewKey()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	// A fresh match starts from idle or after a finished match; otherwise this is
	// the next hand of the running match, so scores carry over and the deal alternates.
	newMatch := s.ph == phIdle || s.matchOver
	var seats game.Seats
	if newMatch {
		seats = s.table.NextSeats(s.Ctx().Self)
		s.scores = [2]int{}
		s.handsWon = [2]int{}
		s.matchOver = false
		s.dealer = game.P1
	} else {
		seats = s.table.Seats
		s.dealer = s.dealer.Opponent()
	}
	s.resetHandLocked(seats, key)
	deck1 := mentalpoker.EncryptAll(key, mentalpoker.FreshDeck())
	if err := mentalpoker.Shuffle(deck1); err != nil {
		s.mu.Unlock()
		return err
	}
	dealer := s.dealer
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindShuffle1, P1: uint32(seats.P1), P2: uint32(seats.P2), Deck: mentalpoker.Marshal(deck1), Dealer: int8(dealer), NewMatch: newMatch})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// resetHandLocked clears per-hand deal state for a new hand. It does NOT touch
// match-level state (scores, handsWon, matchOver, dealer) — the caller sets those.
func (s *Service) resetHandLocked(seats game.Seats, key mentalpoker.Key) {
	s.table.Seats = seats
	s.ph = phShuffle
	s.turn = s.dealer.Opponent() // non-dealer plays first
	s.deck2 = nil
	s.discard = nil
	s.stockNext = stockStart
	s.handCounts = [2]int{}
	s.outcome = ""
	s.verified = false
	s.oppHand = nil
	s.offerPasses = 0
	s.key = key
	s.haveKey = true
	s.hand = nil
	s.upcard = -1
	s.pendPos = -1
	s.knockSeat = game.P1
	s.revealHands = [2][]int8{}
	s.revealExp = [2][]byte{}
}

func gameSeats(m msg) game.Seats {
	return game.Seats{P1: wire.ParticipantID(m.P1), P2: wire.ParticipantID(m.P2)}
}

// mySeat returns this end's seat and whether it is a seated player.
func (s *Service) mySeat() (game.Side, bool) {
	return s.table.Seats.SideOf(s.Ctx().Self)
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("gin: %w", err)
	}
	switch m.Kind {
	case kindStartReq:
		if s.Ctx().Host {
			return s.hostStart(from)
		}
		return nil
	case kindShuffle1:
		return s.handleShuffle1(from, m)
	case kindShuffle2:
		return s.handleShuffle2(from, m)
	case kindDeal:
		return s.handleDeal(from, m)
	case kindTakeDiscard:
		return s.handleTakeDiscard(from)
	case kindDrawReq:
		return s.handleDrawReq(from, m)
	case kindDrawPartial:
		return s.handleDrawPartial(from, m)
	case kindDiscard:
		return s.handleDiscard(from, m)
	case kindKnock:
		return s.handleKnock(from, m)
	case kindReveal:
		return s.handleReveal(from, m)
	case kindResign:
		return s.handleResign(from)
	case kindUpcardTake:
		return s.handleUpcardTake(from)
	case kindUpcardPass:
		return s.handleUpcardPass(from)
	}
	return fmt.Errorf("gin: unknown kind %d", m.Kind)
}
