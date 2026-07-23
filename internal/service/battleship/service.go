// The battleship service — kibitz's flagship: hidden ship placement with NO
// trusted server anywhere. Each player commits to all 100 cells before the
// first shot (internal/shipcommit); every shot is answered by an
// auto-reveal that everyone (spectators included) verifies against the
// commitment; at game end BOTH players open their full boards and everyone
// checks fleet legality. Cheating is not "against the rules" — it is
// detectable by every participant, and a detected cheat freezes the game.
//
// Phases: placing → shooting → validating → over. "Sunk" is never declared:
// it is derived publicly (revealed hits of ship k == its length), so there
// is nothing to lie in.
package battleship

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/shipcommit"
	"github.com/richardwooding/kibitz/internal/wire"
)

const ID = "battleship"

const (
	kindNewGame     uint8 = 1
	kindStartReq    uint8 = 2
	kindCommitBoard uint8 = 3
	kindShot        uint8 = 4
	kindReveal      uint8 = 5
	kindFullReveal  uint8 = 6
	kindResign      uint8 = 7
)

type msg struct {
	Kind      uint8                   `cbor:"1,keyasint"`
	P1        uint32                  `cbor:"2,keyasint,omitempty"`
	P2        uint32                  `cbor:"3,keyasint,omitempty"`
	Commits   [][]byte                `cbor:"4,keyasint,omitempty"` // 100 × 32B
	Cell      uint8                   `cbor:"5,keyasint,omitempty"`
	Reveal    *shipcommit.CellReveal  `cbor:"6,keyasint,omitempty"`
	Cells     []shipcommit.CellReveal `cbor:"7,keyasint,omitempty"` // fullReveal
	StateHash []byte                  `cbor:"8,keyasint,omitempty"`
}

type phase uint8

const (
	phaseIdle       phase = 0
	phasePlacing    phase = 1
	phaseShooting   phase = 2
	phaseValidating phase = 3
	phaseOver       phase = 4
)

func (p phase) String() string {
	return [...]string{"idle", "placing", "shooting", "validating", "over"}[p]
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing   bool
	Phase     string
	P1ID      wire.ParticipantID
	P2ID      wire.ParticipantID
	TurnID    wire.ParticipantID // shooter to act (shooting phase only)
	MyFleet   [100]uint8         // local player's own placement (zeros otherwise)
	Committed [2]bool            // per seat
	// Public reveal grids, per seat's OWN board: -1 unknown, 0 revealed
	// water, 1..5 revealed ship cell.
	Reveals [2][100]int8
	Sunk    [2][]uint8 // ship ids fully sunk on each seat's board
	Outcome string
	CheatBy wire.ParticipantID
}

// CheatDetected freezes the game: a reveal failed verification or a final
// board was illegal.
type CheatDetected struct{ By wire.ParticipantID }

// Service implements service.Service.
type Service struct {
	ctx service.Context

	mu    sync.Mutex
	table game.Table
	ph    phase

	myBoard   *shipcommit.Board // local secret; never serialized
	myFleet   [100]uint8
	commits   [2][100][32]byte
	committed [2]bool
	reveals   [2][100]int8 // -1 unknown
	validated [2]bool
	turn      game.Side // shooter
	pending   int8      // cell awaiting reveal, -1 none
	winner    int8      // -1 undecided, 0/1 seat
	cheatBy   wire.ParticipantID
}

func New() *Service {
	s := &Service{pending: -1, winner: -1}
	s.clearReveals()
	return s
}

func (s *Service) clearReveals() {
	for side := 0; side < 2; side++ {
		for i := 0; i < 100; i++ {
			s.reveals[side][i] = -1
		}
	}
}

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.ctx = ctx }

func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.ctx.Host {
		return
	}
	s.mu.Lock()
	s.table.NoteKeyed(id, role)
	s.mu.Unlock()
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	winner, forfeit := s.table.NoteLeft(id, s.lifecycleLocked())
	if forfeit {
		s.winner = int8(winner)
		s.ph = phaseOver
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

func (s *Service) lifecycleLocked() game.Phase {
	switch s.ph {
	case phaseIdle:
		return game.Idle
	case phaseOver:
		return game.Over
	default:
		return game.Playing
	}
}

// Start launches a game or rematch.
func (s *Service) Start() error {
	if !s.ctx.Host {
		body, err := wire.Marshal(msg{Kind: kindStartReq})
		if err != nil {
			return err
		}
		return s.ctx.Send.SendTo(s.ctx.HostID, ID, body)
	}
	return s.hostStart(s.ctx.Self)
}

func (s *Service) hostStart(from wire.ParticipantID) error {
	s.mu.Lock()
	if err := s.table.AuthorizeStart(s.ctx.Host, from, s.ctx.Self, s.lifecycleLocked()); err != nil {
		s.mu.Unlock()
		return err
	}
	seats := s.table.NextSeats(s.ctx.Self)
	s.resetLocked(seats)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, P1: uint32(seats.P1), P2: uint32(seats.P2)})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) resetLocked(seats game.Seats) {
	s.table.Seats = seats
	s.ph = phasePlacing
	s.myBoard = nil
	s.myFleet = [100]uint8{}
	s.commits = [2][100][32]byte{}
	s.committed = [2]bool{}
	s.validated = [2]bool{}
	s.clearReveals()
	s.turn = game.P1
	s.pending = -1
	s.winner = -1
	s.cheatBy = 0
}

// Commit locks in the local player's fleet and broadcasts the commitments.
func (s *Service) Commit(placement [100]uint8) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	if !seated || s.ph != phasePlacing {
		s.mu.Unlock()
		return errors.New("battleship: not placing")
	}
	if s.committed[side] {
		s.mu.Unlock()
		return errors.New("battleship: already committed")
	}
	board, commits, err := shipcommit.NewBoard(placement)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.myBoard = &board
	s.myFleet = placement
	s.commits[side] = commits
	s.committed[side] = true
	s.maybeStartShootingLocked()
	s.mu.Unlock()

	flat := make([][]byte, 100)
	for i := range commits {
		flat[i] = append([]byte{}, commits[i][:]...)
	}
	body, err := wire.Marshal(msg{Kind: kindCommitBoard, Commits: flat})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Shoot fires at a cell of the opponent's board.
func (s *Service) Shoot(cell uint8) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	if !seated || s.ph != phaseShooting || side != s.turn {
		s.mu.Unlock()
		return errors.New("battleship: not your shot")
	}
	if s.pending != -1 {
		s.mu.Unlock()
		return errors.New("battleship: awaiting reveal")
	}
	target := side.Opponent()
	if cell >= 100 || s.reveals[target][cell] != -1 {
		s.mu.Unlock()
		return errors.New("battleship: cell already shot")
	}
	s.pending = int8(cell)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindShot, Cell: cell})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Resign concedes; validation still runs so the result is honest.
func (s *Service) Resign() error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	if !seated || (s.ph != phaseShooting && s.ph != phasePlacing) {
		s.mu.Unlock()
		return errors.New("battleship: no game to resign")
	}
	s.winner = int8(side.Opponent())
	s.enterValidationLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.afterValidationEntry()
	return nil
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("battleship: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		if from != s.ctx.HostID {
			return fmt.Errorf("battleship: new game from non-host %d", from)
		}
		s.mu.Lock()
		s.resetLocked(game.Seats{P1: wire.ParticipantID(m.P1), P2: wire.ParticipantID(m.P2)})
		s.mu.Unlock()
		s.emitState()
		return nil
	case kindStartReq:
		if !s.ctx.Host {
			return nil
		}
		return s.hostStart(from)
	case kindCommitBoard:
		return s.handleCommitBoard(from, m)
	case kindShot:
		return s.handleShot(from, m)
	case kindReveal:
		return s.handleReveal(from, m)
	case kindFullReveal:
		return s.handleFullReveal(from, m)
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("battleship: unknown message kind %d", m.Kind)
}

func (s *Service) handleCommitBoard(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != phasePlacing || s.committed[side] || len(m.Commits) != 100 {
		s.mu.Unlock()
		return errors.New("battleship: unexpected board commitment")
	}
	for i, c := range m.Commits {
		if len(c) != 32 {
			s.mu.Unlock()
			return errors.New("battleship: malformed commitment")
		}
		copy(s.commits[side][i][:], c)
	}
	s.committed[side] = true
	s.maybeStartShootingLocked()
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) maybeStartShootingLocked() {
	if s.committed[0] && s.committed[1] && s.ph == phasePlacing {
		s.ph = phaseShooting
		s.turn = game.P1
	}
}

func (s *Service) handleShot(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	target := side.Opponent()
	valid := seated && s.ph == phaseShooting && side == s.turn && s.pending == -1 &&
		m.Cell < 100 && s.reveals[target][m.Cell] == -1
	if !valid {
		s.mu.Unlock()
		return fmt.Errorf("battleship: invalid shot from %d", from)
	}
	s.pending = int8(m.Cell)
	iDefend := s.table.Seats.IDOf(target) == s.ctx.Self && s.myBoard != nil
	var reveal shipcommit.CellReveal
	if iDefend {
		reveal = s.myBoard.Cells[m.Cell]
	}
	s.mu.Unlock()
	s.emitState()

	// The board owner answers automatically.
	if iDefend {
		return s.sendReveal(reveal)
	}
	return nil
}

// sendReveal applies the defender's own reveal locally (it won't hear its
// broadcast echoed) and ships it with the post-apply state hash.
func (s *Service) sendReveal(reveal shipcommit.CellReveal) error {
	if err := s.applyReveal(s.ctx.Self, reveal); err != nil {
		return err
	}
	s.mu.Lock()
	hash := s.stateHashLocked()
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindReveal, Reveal: &reveal, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.ctx.Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.afterValidationEntry()
	return nil
}

func (s *Service) handleReveal(from wire.ParticipantID, m msg) error {
	if m.Reveal == nil {
		return errors.New("battleship: empty reveal")
	}
	if err := s.applyReveal(from, *m.Reveal); err != nil {
		return err
	}
	s.mu.Lock()
	ok := bytes.Equal(s.stateHashLocked(), m.StateHash)
	if !ok {
		s.ph = phaseOver
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("battleship: state hash mismatch")
	}
	s.afterValidationEntry()
	s.emitState()
	return nil
}

// applyReveal verifies and records one cell reveal from the board owner.
func (s *Service) applyReveal(from wire.ParticipantID, reveal shipcommit.CellReveal) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	valid := seated && s.ph == phaseShooting && s.pending != -1 &&
		reveal.Cell == uint8(s.pending) && side == s.turn.Opponent()
	if !valid {
		s.mu.Unlock()
		return fmt.Errorf("battleship: unexpected reveal from %d", from)
	}
	if !shipcommit.Verify(s.commits[side][reveal.Cell], reveal) {
		s.ph = phaseOver
		s.cheatBy = from
		s.mu.Unlock()
		s.ctx.Emit(CheatDetected{By: from})
		s.emitState()
		return fmt.Errorf("battleship: reveal fails commitment from %d", from)
	}
	s.reveals[side][reveal.Cell] = int8(reveal.ShipID)
	s.pending = -1
	if s.hitsLocked(side) >= shipcommit.TotalShipCells {
		// The shooter sank the fleet.
		s.winner = int8(s.turn)
		s.enterValidationLocked()
	} else {
		s.turn = s.turn.Opponent()
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) hitsLocked(side game.Side) int {
	n := 0
	for _, v := range s.reveals[side] {
		if v > 0 {
			n++
		}
	}
	return n
}

func (s *Service) enterValidationLocked() {
	s.ph = phaseValidating
	s.pending = -1
}

// afterValidationEntry runs outside the lock: if validation just began and
// this end is a player, it opens its full board.
func (s *Service) afterValidationEntry() {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(s.ctx.Self)
	fire := s.ph == phaseValidating && seated && s.myBoard != nil && !s.validated[side]
	var cells []shipcommit.CellReveal
	if fire {
		cells = append(cells, s.myBoard.Cells[:]...)
	}
	s.mu.Unlock()
	if !fire {
		return
	}
	// Apply locally first (no echo), then broadcast.
	if err := s.recordFullReveal(s.ctx.Self, cells); err != nil {
		return
	}
	body, err := wire.Marshal(msg{Kind: kindFullReveal, Cells: cells})
	if err != nil {
		return
	}
	_ = s.ctx.Send.Broadcast(ID, body)
}

func (s *Service) handleFullReveal(from wire.ParticipantID, m msg) error {
	if err := s.recordFullReveal(from, m.Cells); err != nil {
		return err
	}
	s.afterValidationEntry() // a resign may reach validating via this message
	return nil
}

// recordFullReveal verifies a complete board opening: every commitment and
// fleet legality. Both boards verified → game over.
func (s *Service) recordFullReveal(from wire.ParticipantID, cells []shipcommit.CellReveal) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || s.ph != phaseValidating || s.validated[side] || len(cells) != 100 {
		s.mu.Unlock()
		return errors.New("battleship: unexpected full reveal")
	}
	var placement [100]uint8
	for i, r := range cells {
		if int(r.Cell) != i || !shipcommit.Verify(s.commits[side][i], r) {
			s.ph = phaseOver
			s.cheatBy = from
			s.mu.Unlock()
			s.ctx.Emit(CheatDetected{By: from})
			s.emitState()
			return fmt.Errorf("battleship: full reveal fails commitment from %d", from)
		}
		placement[i] = r.ShipID
		s.reveals[side][i] = int8(r.ShipID)
	}
	if err := shipcommit.FleetLegal(placement); err != nil {
		s.ph = phaseOver
		s.cheatBy = from
		s.mu.Unlock()
		s.ctx.Emit(CheatDetected{By: from})
		s.emitState()
		return fmt.Errorf("battleship: illegal fleet from %d: %w", from, err)
	}
	s.validated[side] = true
	if s.validated[0] && s.validated[1] {
		s.ph = phaseOver
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	side, seated := s.table.Seats.SideOf(from)
	if !seated || (s.ph != phaseShooting && s.ph != phasePlacing) {
		s.mu.Unlock()
		return errors.New("battleship: resign outside game")
	}
	s.winner = int8(side.Opponent())
	s.enterValidationLocked()
	s.mu.Unlock()
	s.afterValidationEntry()
	s.emitState()
	return nil
}

// --- snapshot (PUBLIC state only — never the local secret board) ------------

type snapshot struct {
	P1        uint32    `cbor:"1,keyasint"`
	P2        uint32    `cbor:"2,keyasint"`
	Phase     uint8     `cbor:"3,keyasint"`
	Turn      uint8     `cbor:"4,keyasint"`
	Commits   [2][]byte `cbor:"5,keyasint,omitempty"` // 3200B per side, concatenated
	Committed [2]bool   `cbor:"6,keyasint"`
	Reveals   [2][]int8 `cbor:"7,keyasint"`
	Validated [2]bool   `cbor:"8,keyasint"`
	Pending   int8      `cbor:"9,keyasint"`
	Winner    int8      `cbor:"10,keyasint"`
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == phaseIdle {
		return nil, nil
	}
	snap := snapshot{
		P1: uint32(s.table.Seats.P1), P2: uint32(s.table.Seats.P2),
		Phase: uint8(s.ph), Turn: uint8(s.turn),
		Committed: s.committed, Validated: s.validated,
		Pending: s.pending, Winner: s.winner,
	}
	for side := 0; side < 2; side++ {
		if s.committed[side] {
			flat := make([]byte, 0, 3200)
			for _, c := range s.commits[side] {
				flat = append(flat, c[:]...)
			}
			snap.Commits[side] = flat
		}
		snap.Reveals[side] = append([]int8{}, s.reveals[side][:]...)
	}
	return wire.Marshal(snap)
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("battleship: restore: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only (see chess/backgammon for why).
	if s.ph != phaseIdle {
		s.mu.Unlock()
		return nil
	}
	s.table.Seats = game.Seats{P1: wire.ParticipantID(snap.P1), P2: wire.ParticipantID(snap.P2)}
	s.ph = phase(snap.Phase)
	s.turn = game.Side(snap.Turn)
	s.committed = snap.Committed
	s.validated = snap.Validated
	s.pending = snap.Pending
	s.winner = snap.Winner
	for side := 0; side < 2; side++ {
		if len(snap.Commits[side]) == 3200 {
			for i := 0; i < 100; i++ {
				copy(s.commits[side][i][:], snap.Commits[side][i*32:(i+1)*32])
			}
		}
		if len(snap.Reveals[side]) == 100 {
			copy(s.reveals[side][:], snap.Reveals[side])
		}
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// State returns the current state for UI pulls.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// --- internals ---------------------------------------------------------------

// stateHashLocked covers the public post-apply state (the shared M3 hash
// convention, computed after turn advance / phase change).
func (s *Service) stateHashLocked() []byte {
	b, err := wire.Marshal(struct {
		Reveals [2][]int8 `cbor:"1,keyasint"`
		Turn    uint8     `cbor:"2,keyasint"`
		Phase   uint8     `cbor:"3,keyasint"`
	}{[2][]int8{s.reveals[0][:], s.reveals[1][:]}, uint8(s.turn), uint8(s.ph)})
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	return sum[:8]
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.ctx.Emit(st)
}

func (s *Service) stateLocked() State {
	if s.ph == phaseIdle {
		return State{}
	}
	st := State{
		Playing:   true,
		Phase:     s.ph.String(),
		P1ID:      s.table.Seats.P1,
		P2ID:      s.table.Seats.P2,
		MyFleet:   s.myFleet,
		Committed: s.committed,
		Reveals:   s.reveals,
		CheatBy:   s.cheatBy,
	}
	for side := 0; side < 2; side++ {
		st.Sunk[side] = s.sunkLocked(game.Side(side))
	}
	if s.ph == phaseShooting && s.pending == -1 {
		st.TurnID = s.table.Seats.IDOf(s.turn)
	}
	if s.ph == phaseOver || s.ph == phaseValidating {
		switch {
		case s.cheatBy != 0:
			st.Outcome = "voided — cheating detected"
		case s.winner == 0:
			st.Outcome = "player 1 wins"
		case s.winner == 1:
			st.Outcome = "player 2 wins"
		}
		if s.ph == phaseValidating && s.cheatBy == 0 {
			st.Outcome += " (verifying boards…)"
		}
	}
	return st
}

// sunkLocked derives which ships on side's board are fully revealed.
func (s *Service) sunkLocked(side game.Side) []uint8 {
	counts := [6]uint8{}
	for _, v := range s.reveals[side] {
		if v > 0 {
			counts[v]++
		}
	}
	var sunk []uint8
	for id := uint8(1); id <= 5; id++ {
		if counts[id] == shipcommit.Lengths[id] {
			sunk = append(sunk, id)
		}
	}
	return sunk
}
