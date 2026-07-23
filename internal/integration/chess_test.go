package integration

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/service/chess"
	"github.com/richardwooding/kibitz/internal/session"
)

// chessTable is a fully-loaded end: session + mux + chat + chess.
type chessTable struct {
	client *session.Client
	mux    *service.Mux
	chess  *chess.Service
}

func hostChess(t *testing.T, url string) (*chessTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	cs := chess.New()
	return &chessTable{client: c, mux: service.NewMux(c, chat.New(), cs), chess: cs}, phrase
}

func joinChess(t *testing.T, url, phrase string) *chessTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	cs := chess.New()
	return &chessTable{client: c, mux: service.NewMux(c, chat.New(), cs), chess: cs}
}

func chessWait[E any](t *testing.T, tb *chessTable, match func(E) bool) E {
	t.Helper()
	for ev := range tb.mux.Events() {
		if e, ok := ev.(E); ok && match(e) {
			return e
		}
	}
	t.Fatalf("mux events closed while waiting for %T", *new(E))
	panic("unreachable")
}

// TestFullGameOverRelay plays scholar's mate over a real relay with a
// spectator watching, asserting the position state on every client after
// every move.
func TestFullGameOverRelay(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostChess(t, url)
	player := joinChess(t, url, phrase)
	spectator := joinChess(t, url, phrase)

	// Everyone sees the game start (host = white, player = black).
	for _, tb := range []*chessTable{host, player, spectator} {
		st := chessWait(t, tb, func(s chess.State) bool { return s.Playing })
		if st.WhiteID != host.client.Self() || st.BlackID != player.client.Self() {
			t.Fatalf("players %+v", st)
		}
	}

	moves := []struct {
		tb  *chessTable
		uci string
	}{
		{host, "e2e4"}, {player, "e7e5"},
		{host, "d1h5"}, {player, "b8c6"},
		{host, "f1c4"}, {player, "g8f6"},
		{host, "h5f7"},
	}
	for _, m := range moves {
		if err := m.tb.chess.TryMove(m.uci); err != nil {
			t.Fatalf("move %s: %v", m.uci, err)
		}
		// Every client converges on the same position after each move.
		want := ""
		for _, tb := range []*chessTable{host, player, spectator} {
			st := chessWait(t, tb, func(s chess.State) bool { return s.LastUCI == m.uci })
			if want == "" {
				want = st.FEN
			} else if st.FEN != want {
				t.Fatalf("position divergence after %s: %q vs %q", m.uci, st.FEN, want)
			}
		}
	}

	for _, tb := range []*chessTable{host, player, spectator} {
		st := tb.chess.State()
		if st.Outcome != "1-0" || st.Method != "Checkmate" {
			t.Fatalf("client %d final state %+v", tb.client.Self(), st)
		}
	}
}

// TestLateSpectatorSyncsMidGame joins a spectator after several moves and
// checks the snapshot lands them on the live position.
func TestLateSpectatorSyncsMidGame(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostChess(t, url)
	player := joinChess(t, url, phrase)

	chessWait(t, host, func(s chess.State) bool { return s.Playing })
	chessWait(t, player, func(s chess.State) bool { return s.Playing })

	for _, m := range []struct {
		tb  *chessTable
		uci string
	}{{host, "e2e4"}, {player, "c7c5"}, {host, "g1f3"}} {
		if err := m.tb.chess.TryMove(m.uci); err != nil {
			t.Fatal(err)
		}
		chessWait(t, host, func(s chess.State) bool { return s.LastUCI == m.uci })
		chessWait(t, player, func(s chess.State) bool { return s.LastUCI == m.uci })
	}
	want := host.chess.State().FEN

	late := joinChess(t, url, phrase)
	st := chessWait(t, late, func(s chess.State) bool { return s.Playing })
	if st.FEN != want {
		t.Fatalf("late spectator FEN %q, want %q", st.FEN, want)
	}
	if st.TurnID != player.client.Self() {
		t.Fatalf("late spectator thinks turn=%d", st.TurnID)
	}
}
