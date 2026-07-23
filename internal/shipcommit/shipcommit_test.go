package shipcommit

import (
	"errors"
	"testing"
)

// legalPlacement builds the standard fleet in fixed rows.
func legalPlacement() [100]uint8 {
	var p [100]uint8
	for i := 0; i < 5; i++ {
		p[0+i] = 1 // carrier row 0, cols 0-4
	}
	for i := 0; i < 4; i++ {
		p[10+i] = 2 // battleship row 1
	}
	for i := 0; i < 3; i++ {
		p[20+i] = 3 // cruiser row 2
	}
	for i := 0; i < 3; i++ {
		p[30+i] = 4 // submarine row 3
	}
	for i := 0; i < 2; i++ {
		p[40+i] = 5 // destroyer row 4
	}
	return p
}

func TestCommitRevealRoundTrip(t *testing.T) {
	board, commits, err := NewBoard(legalPlacement())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if !Verify(commits[i], board.Cells[i]) {
			t.Fatalf("cell %d failed verification", i)
		}
	}
}

func TestTamperedRevealsRejected(t *testing.T) {
	board, commits, err := NewBoard(legalPlacement())
	if err != nil {
		t.Fatal(err)
	}
	// Lie about a hit: claim ship cell 0 is water.
	lie := board.Cells[0]
	lie.ShipID = 0
	if Verify(commits[0], lie) {
		t.Fatal("ship-to-water lie verified")
	}
	// Replay another cell's (honest) reveal into slot 0.
	if Verify(commits[0], board.Cells[1]) {
		t.Fatal("cross-cell replay verified")
	}
	// Wrong salt.
	bad := board.Cells[0]
	bad.Salt[0] ^= 1
	if Verify(commits[0], bad) {
		t.Fatal("wrong salt verified")
	}
}

func TestFleetLegal(t *testing.T) {
	if err := FleetLegal(legalPlacement()); err != nil {
		t.Fatalf("legal fleet rejected: %v", err)
	}

	// Wrong count: carrier one cell short.
	p := legalPlacement()
	p[4] = 0
	if err := FleetLegal(p); !errors.Is(err, ErrWrongFleet) {
		t.Fatalf("short carrier: %v", err)
	}

	// Bent carrier: cells 0,1,2,3 then one dropped down a row (13). The
	// rest of the fleet is placed on clear rows so only the bend can fail.
	var bent [100]uint8
	bent[0], bent[1], bent[2], bent[3], bent[13] = 1, 1, 1, 1, 1
	for i := 0; i < 4; i++ {
		bent[30+i] = 2
	}
	for i := 0; i < 3; i++ {
		bent[40+i] = 3
	}
	for i := 0; i < 3; i++ {
		bent[50+i] = 4
	}
	for i := 0; i < 2; i++ {
		bent[60+i] = 5
	}
	if err := FleetLegal(bent); !errors.Is(err, ErrWrongShape) {
		t.Fatalf("bent carrier: %v", err)
	}

	// Diagonal ship.
	var d [100]uint8
	for i := 0; i < 5; i++ {
		d[i*11] = 1 // diagonal
	}
	for i := 0; i < 4; i++ {
		d[50+i] = 2
	}
	for i := 0; i < 3; i++ {
		d[60+i] = 3
	}
	for i := 0; i < 3; i++ {
		d[70+i] = 4
	}
	for i := 0; i < 2; i++ {
		d[80+i] = 5
	}
	if err := FleetLegal(d); !errors.Is(err, ErrWrongShape) {
		t.Fatalf("diagonal carrier: %v", err)
	}

	// Unknown ship id.
	p = legalPlacement()
	p[99] = 7
	if err := FleetLegal(p); !errors.Is(err, ErrBadShipID) {
		t.Fatalf("bad id: %v", err)
	}

	// Touching ships are ALLOWED: fleet in adjacent rows already touches
	// (legalPlacement) and passed above. Also duplicate fleet (two carriers)
	// must fail.
	p = legalPlacement()
	for i := 0; i < 2; i++ {
		p[40+i] = 0
	}
	for i := 0; i < 5; i++ {
		p[50+i] = 1 // second carrier instead of destroyer
	}
	if err := FleetLegal(p); !errors.Is(err, ErrWrongFleet) {
		t.Fatalf("double carrier: %v", err)
	}
}

func TestNewBoardRejectsIllegalPlacement(t *testing.T) {
	var empty [100]uint8
	if _, _, err := NewBoard(empty); err == nil {
		t.Fatal("empty placement accepted")
	}
}

func TestRandomPlacementAlwaysLegal(t *testing.T) {
	for i := 0; i < 200; i++ {
		p, err := RandomPlacement()
		if err != nil {
			t.Fatal(err)
		}
		if err := FleetLegal(p); err != nil {
			t.Fatalf("random placement %d illegal: %v", i, err)
		}
	}
}

func TestSaltedCommitmentsHideContent(t *testing.T) {
	// Same placement twice must give entirely different commitments (salts).
	p := legalPlacement()
	_, c1, err := NewBoard(p)
	if err != nil {
		t.Fatal(err)
	}
	_, c2, err := NewBoard(p)
	if err != nil {
		t.Fatal(err)
	}
	same := 0
	for i := range c1 {
		if c1[i] == c2[i] {
			same++
		}
	}
	if same != 0 {
		t.Fatalf("%d commitments identical across salt sets", same)
	}
}
