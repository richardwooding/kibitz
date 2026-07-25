// Package shipcommit implements the Battleship hiding scheme: each player
// commits to all 100 cells of their board before the first shot, then
// reveals exactly the cells that get shot. Nobody — not the opponent, not
// the relay (which sees only ciphertext anyway), not a spectator — learns an
// unshot cell, yet every reveal is verifiable against the commitment, so
// lying about a hit is impossible.
//
// Preimage = deterministic CBOR of CellReveal (the fairdice pattern).
// Including Cell in the preimage binds each reveal to its slot (no replaying
// a miss-reveal into a different square); committing ShipID (not a bare
// occupied bit) makes "sunk" a public derivation — revealed hits of ship k
// equal its length — so no sunk-declaration message exists to lie in.
// Accepted tradeoff: the shooter learns WHICH ship a hit struck immediately.
package shipcommit

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/richardwooding/kibitz/internal/wire"
)

// Lengths[k] is the length of fleet ship k (1-indexed; 0 unused):
// carrier 5, battleship 4, cruiser 3, submarine 3, destroyer 2.
var Lengths = [6]uint8{0, 5, 4, 3, 3, 2}

// TotalShipCells is the number of hits that sink a whole fleet.
const TotalShipCells = 17

// CellReveal is the opening of one cell's commitment.
type CellReveal struct {
	Cell   uint8    `cbor:"1,keyasint"` // 0..99 = y*10 + x
	ShipID uint8    `cbor:"2,keyasint"` // 0 water, 1..5 fleet ordinal
	Salt   [16]byte `cbor:"3,keyasint"`
}

// Commitment hashes the deterministic encoding of the reveal.
func (r CellReveal) Commitment() ([32]byte, error) {
	b, err := wire.Marshal(r)
	if err != nil {
		return [32]byte{}, fmt.Errorf("shipcommit: encode: %w", err)
	}
	return sha256.Sum256(b), nil
}

// Verify reports whether a reveal opens the commitment.
func Verify(c [32]byte, r CellReveal) bool {
	got, err := r.Commitment()
	if err != nil {
		return false
	}
	return got == c
}

// Board is a player's SECRET: every cell's reveal, salts included. It must
// never leave the local process (snapshots carry only the public
// commitments).
type Board struct {
	Cells [100]CellReveal
}

// NewBoard salts a placement and produces the public commitment vector.
func NewBoard(placement [100]uint8) (Board, [100][32]byte, error) {
	if err := FleetLegal(placement); err != nil {
		return Board{}, [100][32]byte{}, err
	}
	var b Board
	var commits [100][32]byte
	for i := 0; i < 100; i++ {
		b.Cells[i] = CellReveal{Cell: uint8(i), ShipID: placement[i]}
		if _, err := rand.Read(b.Cells[i].Salt[:]); err != nil {
			return Board{}, [100][32]byte{}, fmt.Errorf("shipcommit: rand: %w", err)
		}
		c, err := b.Cells[i].Commitment()
		if err != nil {
			return Board{}, [100][32]byte{}, err
		}
		commits[i] = c
	}
	return b, commits, nil
}

var (
	ErrBadShipID  = errors.New("shipcommit: cell holds an unknown ship id")
	ErrWrongShape = errors.New("shipcommit: ship is not a straight contiguous line")
	ErrWrongFleet = errors.New("shipcommit: fleet must be exactly ships 1..5 with standard lengths")
)

// FleetLegal validates a placement: exactly the standard fleet (5/4/3/3/2),
// each ship a straight contiguous line, no overlap (implied — a cell holds
// one ship id). Touching ships are allowed (Hasbro standard).
func FleetLegal(placement [100]uint8) error {
	cells := map[uint8][]uint8{}
	for i, id := range placement {
		if id > 5 {
			return ErrBadShipID
		}
		if id != 0 {
			cells[id] = append(cells[id], uint8(i))
		}
	}
	for id := uint8(1); id <= 5; id++ {
		line := cells[id]
		if len(line) != int(Lengths[id]) {
			return ErrWrongFleet
		}
		if !straightContiguous(line) {
			return ErrWrongShape
		}
	}
	return nil
}

// straightContiguous checks a sorted-by-construction cell list forms one
// horizontal or vertical run. (Placement iteration order makes the list
// ascending already.)
func straightContiguous(cells []uint8) bool {
	if len(cells) < 2 {
		return false // no fleet ship has length 1
	}
	x0, y0 := cells[0]%10, cells[0]/10
	horizontal := true
	vertical := true
	for i, c := range cells {
		x, y := c%10, c/10
		if y != y0 || x != x0+uint8(i) {
			horizontal = false
		}
		if x != x0 || y != y0+uint8(i) {
			vertical = false
		}
	}
	return horizontal || vertical
}

// RandomPlacement generates a legal random fleet (for the UI's Randomize
// button and for tests).
func RandomPlacement() ([100]uint8, error) {
	var placement [100]uint8
	for id := uint8(1); id <= 5; id++ {
		if err := placeShip(&placement, id); err != nil {
			return placement, err
		}
	}
	return placement, nil
}

// placeShip retries random candidates until ship id lands legally, matching
// the original draw sequence: one rand.Read of 3 bytes per attempt, bounds
// check then overlap check, giving up after 1000 attempts.
func placeShip(placement *[100]uint8, id uint8) error {
	length := int(Lengths[id])
	for attempt := 0; ; attempt++ {
		if attempt > 1000 {
			return errors.New("shipcommit: placement failed to converge")
		}
		horizontal, x, y, err := randomShipCandidate()
		if err != nil {
			return err
		}
		if !shipFits(horizontal, x, y, length) {
			continue
		}
		if !cellsFree(placement, horizontal, x, y, length) {
			continue
		}
		writeShip(placement, id, horizontal, x, y, length)
		return nil
	}
}

// randomShipCandidate draws one orientation and origin from 3 random bytes.
func randomShipCandidate() (horizontal bool, x, y int, err error) {
	var rb [3]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return false, 0, 0, fmt.Errorf("shipcommit: rand: %w", err)
	}
	return rb[0]&1 == 0, int(rb[1]) % 10, int(rb[2]) % 10, nil
}

// shipCell is the board index of the i-th cell of a ship at (x, y).
func shipCell(horizontal bool, x, y, i int) int {
	if horizontal {
		return y*10 + x + i
	}
	return (y+i)*10 + x
}

// shipFits reports whether a ship of the given length stays on the board.
func shipFits(horizontal bool, x, y, length int) bool {
	if horizontal {
		return x+length <= 10
	}
	return y+length <= 10
}

// cellsFree reports whether every cell the ship would occupy is empty.
func cellsFree(placement *[100]uint8, horizontal bool, x, y, length int) bool {
	for i := 0; i < length; i++ {
		if placement[shipCell(horizontal, x, y, i)] != 0 {
			return false
		}
	}
	return true
}

// writeShip stamps ship id onto every cell it occupies.
func writeShip(placement *[100]uint8, id uint8, horizontal bool, x, y, length int) {
	for i := 0; i < length; i++ {
		placement[shipCell(horizontal, x, y, i)] = id
	}
}
