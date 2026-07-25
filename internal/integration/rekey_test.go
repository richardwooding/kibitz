package integration

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service/connect4"
)

// TestRekeyMidGameDoesNotDisruptPlay: a spectator leaving mid-game makes the
// host rotate the group key and re-wrap it to the surviving players. The two
// players must keep playing seamlessly — every post-rekey move rides the NEW
// key and still validates on both ends — proving forward-secrecy rekeying is
// transparent to live traffic over the real relay + mux + game engine.
func TestRekeyMidGameDoesNotDisruptPlay(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostC4(t, url)
	player := joinC4(t, url, phrase)
	spectator := joinC4(t, url, phrase)
	pollStart(t, host.c4.Start)

	for _, tb := range []*c4Table{host, player, spectator} {
		c4Wait(t, tb, func(s connect4.State) bool { return s.Playing })
	}

	tables := map[uint32]*c4Table{
		uint32(host.client.Self()):   host,
		uint32(player.client.Self()): player,
	}
	play := func(who uint32, col int8) {
		tb := tables[who]
		c4Wait(t, tb, func(s connect4.State) bool { return uint32(s.TurnID) == who })
		if err := tb.c4.Drop(col); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}
	red, yellow := uint32(host.client.Self()), uint32(player.client.Self())

	// One exchange while the spectator is still present (old key).
	play(red, 0)
	play(yellow, 1)

	// The spectator leaves → the host rekeys the two survivors.
	if err := spectator.client.Close(); err != nil {
		t.Fatal(err)
	}
	// The host drops the spectator from its roster once the leave propagates.
	c4Wait(t, host, func(connect4.State) bool { return true })

	// Finish the game AFTER the rekey — these frames use the new key.
	play(red, 0)
	play(yellow, 1)
	play(red, 0)
	play(yellow, 1)
	play(red, 0) // red completes four in column 0

	for _, tb := range []*c4Table{host, player} {
		st := c4Wait(t, tb, func(s connect4.State) bool { return s.Outcome != "" })
		if st.Outcome != "red wins" {
			t.Fatalf("post-rekey game did not finish cleanly: outcome %q", st.Outcome)
		}
	}
}
