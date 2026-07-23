package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/backgammon"
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/session"
)

type bgTable struct {
	client *session.Client
	mux    *service.Mux
	bg     *backgammon.Service
}

func hostBG(t *testing.T, url string) (*bgTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bg := backgammon.New()
	return &bgTable{client: c, mux: service.NewMux(c, chat.New(), bg), bg: bg}, phrase
}

func joinBG(t *testing.T, url, phrase string) *bgTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bg := backgammon.New()
	return &bgTable{client: c, mux: service.NewMux(c, chat.New(), bg), bg: bg}
}

// drain keeps a table's mux event buffer from filling (state pulls do the
// asserting in these tests).
func drain(tb *bgTable) {
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
}

// bgWait polls a table's pulled state until it matches.
func bgWait(t *testing.T, tb *bgTable, match func(backgammon.State) bool) backgammon.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.bg.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for backgammon state (last known: %+v)", tb.bg.State())
	panic("unreachable")
}

// playTurns advances the game n turns: whoever must act rolls (if needed)
// and plays the first legal turn. Dances are auto-passed by the service.
func playTurns(t *testing.T, players []*bgTable, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		// Find the actor: the player whose own state says it must act.
		var acting *bgTable
		var st backgammon.State
		deadline := time.Now().Add(10 * time.Second)
		for acting == nil {
			if time.Now().After(deadline) {
				t.Fatalf("turn %d: no actor emerged (host sees %+v)", i, players[0].bg.State())
			}
			for _, tb := range players {
				s := tb.bg.State()
				if s.Phase == "over" {
					return
				}
				if s.Playing && s.TurnID == tb.client.Self() && (s.Phase == "rolling" || s.Phase == "moving") {
					acting, st = tb, s
					break
				}
			}
			if acting == nil {
				time.Sleep(20 * time.Millisecond)
			}
		}

		if st.Phase == "rolling" {
			if err := acting.bg.Roll(); err != nil {
				t.Fatalf("roll: %v", err)
			}
			self := acting.client.Self()
			st = bgWait(t, acting, func(s backgammon.State) bool {
				return s.Phase == "over" ||
					(s.Phase == "moving" && s.TurnID == self) ||
					s.TurnID != self // danced: turn auto-passed
			})
			if st.Phase != "moving" || st.TurnID != self {
				continue // danced or game over
			}
		}
		if len(st.Legal) == 0 {
			t.Fatalf("moving phase with no legal turns exposed: %+v", st)
		}
		if err := acting.bg.Move(st.Legal[0]); err != nil {
			t.Fatalf("move %v: %v", st.Legal[0], err)
		}
	}
}

func TestBackgammonOverRelay(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostBG(t, url)
	player := joinBG(t, url, phrase)
	spectator := joinBG(t, url, phrase)
	drain(host)
	drain(player)
	drain(spectator)

	// Opening runs automatically: everyone reaches "moving" with dice set
	// and agreeing on who moves first.
	var first uint32
	for _, tb := range []*bgTable{host, player, spectator} {
		st := bgWait(t, tb, func(s backgammon.State) bool { return s.Playing && s.Phase == "moving" })
		if st.Dice[0] < 1 || st.Dice[0] > 6 || st.Dice[1] < 1 || st.Dice[1] > 6 {
			t.Fatalf("bad opening dice %v", st.Dice)
		}
		if st.Dice[0] == st.Dice[1] {
			t.Fatalf("opening dice equal: %v", st.Dice)
		}
		if first == 0 {
			first = uint32(st.TurnID)
		} else if uint32(st.TurnID) != first {
			t.Fatalf("clients disagree on first mover: %d vs %d", st.TurnID, first)
		}
	}

	// Play 12 turns; all three clients must agree on the position after.
	playTurns(t, []*bgTable{host, player}, 12)

	// Let the last turn propagate, then compare boards.
	deadline := time.Now().Add(5 * time.Second)
	for {
		hb, pb, sb := host.bg.State().Board, player.bg.State().Board, spectator.bg.State().Board
		if hb == pb && pb == sb {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("boards diverged:\nhost: %+v\nplayer: %+v\nspec: %+v", hb, pb, sb)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if host.bg.State().PipsW == 167 && host.bg.State().PipsB == 167 {
		t.Fatal("nothing moved in 12 turns")
	}
}

func TestBackgammonResign(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostBG(t, url)
	player := joinBG(t, url, phrase)
	drain(host)
	drain(player)

	bgWait(t, player, func(s backgammon.State) bool { return s.Playing && s.Phase == "moving" })
	if err := player.bg.Resign(); err != nil {
		t.Fatal(err)
	}
	for _, tb := range []*bgTable{host, player} {
		st := bgWait(t, tb, func(s backgammon.State) bool { return s.Phase == "over" })
		if st.Outcome != "white wins" {
			t.Fatalf("outcome %q", st.Outcome)
		}
	}
}

func TestBackgammonLateJoinerSyncs(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostBG(t, url)
	player := joinBG(t, url, phrase)
	drain(host)
	drain(player)

	bgWait(t, host, func(s backgammon.State) bool { return s.Playing && s.Phase == "moving" })
	bgWait(t, player, func(s backgammon.State) bool { return s.Playing && s.Phase == "moving" })
	playTurns(t, []*bgTable{host, player}, 6)

	late := joinBG(t, url, phrase)
	drain(late)
	bgWait(t, late, func(s backgammon.State) bool { return s.Playing })

	// The late joiner must converge to the players' position.
	deadline := time.Now().Add(5 * time.Second)
	for late.bg.State().Board != host.bg.State().Board {
		if time.Now().After(deadline) {
			t.Fatalf("late joiner never converged:\nlate: %+v\nhost: %+v", late.bg.State().Board, host.bg.State().Board)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
