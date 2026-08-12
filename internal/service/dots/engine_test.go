package dots

import (
	"errors"
	"testing"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/parley/wire"
)

// ---- engine ---------------------------------------------------------------

func TestBoxEdgeScheme(t *testing.T) {
	if got := boxEdges(0); got != [4]int8{0, 5, 30, 31} {
		t.Fatalf("box 0 edges %v", got)
	}
	// box 24 = (br4,bc4): top 24, bottom 29, left 58, right 59.
	if got := boxEdges(24); got != [4]int8{24, 29, 58, 59} {
		t.Fatalf("box 24 edges %v", got)
	}
}

func TestApplyClaimsOneBox(t *testing.T) {
	var b Board
	for _, e := range []int8{0, 30, 31} { // three of box 0's edges
		if n, err := b.Apply(e, 1); err != nil || n != 0 {
			t.Fatalf("apply %d: n=%d err=%v", e, n, err)
		}
	}
	// Drawing the fourth edge completes box 0 for side 1.
	if n, err := b.Apply(5, 1); err != nil || n != 1 {
		t.Fatalf("closing edge: n=%d err=%v", n, err)
	}
	if b.Owner[0] != 1 {
		t.Fatalf("box 0 owner %d, want 1", b.Owner[0])
	}
}

func TestApplyClaimsTwoBoxes(t *testing.T) {
	var b Board
	// Edge 31 borders box 0 (right) and box 1 (left). Prime both so drawing 31
	// closes them together.
	for _, e := range []int8{0, 5, 30, 1, 6, 32} {
		if _, err := b.Apply(e, 1); err != nil {
			t.Fatalf("prime %d: %v", e, err)
		}
	}
	if got := b.Completes(31); len(got) != 2 {
		t.Fatalf("Completes(31) = %v, want two boxes", got)
	}
	if n, err := b.Apply(31, 2); err != nil || n != 2 {
		t.Fatalf("double claim: n=%d err=%v", n, err)
	}
	if b.Owner[0] != 2 || b.Owner[1] != 2 {
		t.Fatalf("owners %d/%d, want 2/2", b.Owner[0], b.Owner[1])
	}
}

func TestApplyRejectsBadEdges(t *testing.T) {
	var b Board
	if _, err := b.Apply(0, 1); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	if _, err := b.Apply(0, 2); !errors.Is(err, ErrDrawn) {
		t.Fatalf("redraw: %v", err)
	}
	if _, err := b.Apply(NumEdges, 1); !errors.Is(err, ErrOffBoard) {
		t.Fatalf("off board (high): %v", err)
	}
	if _, err := b.Apply(-1, 1); !errors.Is(err, ErrOffBoard) {
		t.Fatalf("off board (neg): %v", err)
	}
}

func TestLegalAndFull(t *testing.T) {
	var b Board
	if len(b.Legal()) != NumEdges {
		t.Fatalf("fresh legal count %d", len(b.Legal()))
	}
	for e := int8(0); int(e) < NumEdges; e++ {
		if _, err := b.Apply(e, 1); err != nil {
			t.Fatalf("apply %d: %v", e, err)
		}
	}
	if !b.Full() || len(b.Legal()) != 0 {
		t.Fatalf("board should be full: full=%v legal=%d", b.Full(), len(b.Legal()))
	}
	if p1, p2 := b.Score(); p1 != NumBoxes || p2 != 0 {
		t.Fatalf("score %d/%d", p1, p2)
	}
}

// ---- service (turn logic over an in-memory pipe) --------------------------

type fakeSender struct{ sent [][]byte }

func (f *fakeSender) Broadcast(_ string, body []byte) error {
	f.sent = append(f.sent, body)
	return nil
}
func (f *fakeSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

type pair struct {
	host, player     *Service
	hostOut, plOut   *fakeSender
	hostEv, playerEv []any
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{host: New(), player: New(), hostOut: &fakeSender{}, plOut: &fakeSender{}}
	p.host.Attach(service.Context{
		Send: p.hostOut, Emit: func(e any) { p.hostEv = append(p.hostEv, e) },
		Self: 1, HostID: 1, Host: true,
	})
	p.player.Attach(service.Context{
		Send: p.plOut, Emit: func(e any) { p.playerEv = append(p.playerEv, e) },
		Self: 2, HostID: 1, Host: false,
	})
	p.host.MemberKeyed(2, proto.RolePlayer)
	if err := p.host.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p.pump(t)
	return p
}

// pump delivers pending broadcasts to the opposite side until quiet; a hash
// mismatch surfaces as a HandleFrame error here.
func (p *pair) pump(t *testing.T) {
	t.Helper()
	for len(p.hostOut.sent) > 0 || len(p.plOut.sent) > 0 {
		hs, ps := p.hostOut.sent, p.plOut.sent
		p.hostOut.sent, p.plOut.sent = nil, nil
		for _, b := range hs {
			if err := p.player.HandleFrame(1, b); err != nil {
				t.Fatalf("player handling host frame: %v", err)
			}
		}
		for _, b := range ps {
			if err := p.host.HandleFrame(2, b); err != nil {
				t.Fatalf("host handling player frame: %v", err)
			}
		}
	}
}

func (p *pair) drawAs(t *testing.T, s *Service, edge int8) {
	t.Helper()
	if err := s.DrawEdge(edge); err != nil {
		t.Fatalf("draw %d: %v", edge, err)
	}
	p.pump(t)
}

func TestNonCompletingEdgePassesTurn(t *testing.T) {
	p := newPair(t)
	if p.host.State().TurnID != 1 {
		t.Fatalf("opening turn %d, want host", p.host.State().TurnID)
	}
	p.drawAs(t, p.host, 0) // no box completed
	for _, s := range []*Service{p.host, p.player} {
		if s.State().TurnID != 2 {
			t.Fatalf("turn should pass to player, got %d", s.State().TurnID)
		}
	}
}

func TestCompletingBoxKeepsTurnAndClaims(t *testing.T) {
	p := newPair(t)
	// Prime three edges of box 0 with alternating non-completing moves.
	p.drawAs(t, p.host, 0)    // host → player
	p.drawAs(t, p.player, 30) // player → host
	p.drawAs(t, p.host, 31)   // host → player (box 0 now has 3 edges)
	if p.host.State().TurnID != 2 {
		t.Fatalf("pre-claim turn %d, want player", p.host.State().TurnID)
	}
	// Player closes box 0: claims it AND keeps the turn.
	p.drawAs(t, p.player, 5)
	for _, s := range []*Service{p.host, p.player} {
		st := s.State()
		if st.Boxes[0] != 2 {
			t.Fatalf("box 0 owner %d, want player(2)", st.Boxes[0])
		}
		if st.ScoreP2 != 1 {
			t.Fatalf("P2 score %d, want 1", st.ScoreP2)
		}
		if st.TurnID != 2 {
			t.Fatalf("turn after claim %d, want player keeps it", st.TurnID)
		}
	}
	// A following non-completing move finally passes the turn.
	p.drawAs(t, p.player, 1)
	if p.host.State().TurnID != 1 {
		t.Fatalf("turn should return to host, got %d", p.host.State().TurnID)
	}
}

func TestFullGameEndsWithWinnerByBoxCount(t *testing.T) {
	p := newPair(t)
	for e := int8(0); int(e) < NumEdges; e++ {
		st := p.host.State()
		if st.Outcome != "" {
			t.Fatalf("game ended early after %d edges", e)
		}
		if st.TurnID == 1 {
			p.drawAs(t, p.host, e)
		} else {
			p.drawAs(t, p.player, e)
		}
	}
	st := p.host.State()
	if st.Outcome == "" {
		t.Fatalf("game not over after all edges drawn")
	}
	if st.ScoreP1+st.ScoreP2 != NumBoxes {
		t.Fatalf("boxes claimed %d, want %d", st.ScoreP1+st.ScoreP2, NumBoxes)
	}
	// 25 boxes cannot tie, so there is a decisive winner phrased by count.
	if st.Outcome[:3] != "red" && st.Outcome[:4] != "blue" {
		t.Fatalf("outcome %q not a win", st.Outcome)
	}
	if st.TurnID != 0 {
		t.Fatalf("turn %d after game over, want 0", st.TurnID)
	}
	// Both ends must agree (hashes matched throughout, and outcomes match).
	if p.player.State().Outcome != st.Outcome {
		t.Fatalf("ends disagree: %q vs %q", p.player.State().Outcome, st.Outcome)
	}
}

func TestDrawEdgeRejectsAlreadyDrawn(t *testing.T) {
	p := newPair(t)
	p.drawAs(t, p.host, 0)
	// Player is on turn but edge 0 is taken.
	if err := p.player.DrawEdge(0); !errors.Is(err, ErrDrawn) {
		t.Fatalf("redraw over service: %v", err)
	}
}

func TestFinishWinnerAndDrawByBoxCount(t *testing.T) {
	red := New()
	for i := 0; i < 13; i++ {
		red.board.Owner[i] = 1
	}
	for i := 13; i < NumBoxes; i++ {
		red.board.Owner[i] = 2
	}
	red.finishLocked()
	if red.winner != 1 || red.outcomeText() != "red wins 13–12" {
		t.Fatalf("winner %d text %q", red.winner, red.outcomeText())
	}
	// Equal box counts → draw (winner code 3), exercising the tie branch.
	tie := New()
	for i := 0; i < 10; i++ {
		tie.board.Owner[i] = 1
	}
	for i := 10; i < 20; i++ {
		tie.board.Owner[i] = 2
	}
	tie.finishLocked()
	if tie.winner != 3 {
		t.Fatalf("tie winner %d, want 3 (draw)", tie.winner)
	}
}
