package connect4

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/wire"
)

// nullSender satisfies service.Sender; a joiner-side service under test has
// nothing meaningful to transmit.
type nullSender struct{}

func (nullSender) Broadcast(string, []byte) error                  { return nil }
func (nullSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

func attachJoiner(t *testing.T, self, host wire.ParticipantID) *Service {
	t.Helper()
	s := New()
	s.Attach(service.Context{Send: nullSender{}, Emit: func(any) {}, Self: self, HostID: host})
	return s
}

// A session leave has no cross-sender ordering against the host's newGame
// broadcast, so it can arrive first — while this end still has no seats to
// check. The seat pair that then lands still names the departed player; the
// game must end by forfeit immediately, not sit live forever.
func TestNewGameAfterLeaveForfeits(t *testing.T) {
	s := attachJoiner(t, 3, 1) // spectator end
	s.MemberLeft(2)            // player 2's leave overtook the newGame below
	body, err := wire.Marshal(msg{Kind: kindNewGame, P1: 1, P2: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HandleFrame(1, body); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.Outcome != "red wins" || st.TurnID != 0 {
		t.Fatalf("outcome=%q turn=%d, want red wins/0 (P2 departed)", st.Outcome, st.TurnID)
	}
}
