package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/wire"
)

// waitNames pulls Roster events until every wanted id→name pair is present.
func waitNames(t *testing.T, tb *table, want map[wire.ParticipantID]string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-tb.mux.Events():
			if !ok {
				t.Fatal("events closed while waiting for names")
			}
			r, isRoster := ev.(service.Roster)
			if !isRoster {
				continue
			}
			done := true
			for id, n := range want {
				if r.Names[id] != n {
					done = false
					break
				}
			}
			if done {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for names %v", want)
		}
	}
}

func TestScreenNamesDistribute(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)
	player := joinTable(t, url, phrase)

	host.mux.SetName("Ada")
	player.mux.SetName("Bo")

	hid := host.client.Self()   // 1
	pid := player.client.Self() // 2
	want := map[wire.ParticipantID]string{hid: "Ada", pid: "Bo"}

	// Both ends (host-authoritative roster is broadcast) converge on both names.
	waitNames(t, host, want)
	waitNames(t, player, want)
}

func TestScreenNamesLateJoinerLearnsAll(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)
	player := joinTable(t, url, phrase)
	host.mux.SetName("Ada")
	player.mux.SetName("Bo")

	hid, pid := host.client.Self(), player.client.Self()
	waitNames(t, host, map[wire.ParticipantID]string{hid: "Ada", pid: "Bo"})

	// A spectator joins after names are set and must learn the existing two
	// (via the host's post-join re-announce) plus its own.
	late := joinTable(t, url, phrase)
	late.mux.SetName("Cy")
	waitNames(t, late, map[wire.ParticipantID]string{hid: "Ada", pid: "Bo", late.client.Self(): "Cy"})
}

func TestScreenNameSanitizedAndCapped(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)
	player := joinTable(t, url, phrase)

	host.mux.SetName("Ada")
	// Control chars stripped, length capped to 24 runes.
	player.mux.SetName("  Bo\tby\n" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")

	pid := player.client.Self()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-host.mux.Events():
			if !ok {
				t.Fatal("events closed")
			}
			if r, isR := ev.(service.Roster); isR {
				got := r.Names[pid]
				if got == "" {
					continue
				}
				if len([]rune(got)) > 24 {
					t.Fatalf("name not capped: %q (%d runes)", got, len([]rune(got)))
				}
				for _, ru := range got {
					if ru == '\t' || ru == '\n' {
						t.Fatalf("control char survived: %q", got)
					}
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}
