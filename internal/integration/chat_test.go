package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/chat"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

// table is one fully-attached end: session client + mux + chat.
type table struct {
	client *session.Client
	mux    *service.Mux
	chat   *chat.Service
}

func hostTable(t *testing.T, url string) (*table, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ch := chat.New()
	return &table{client: c, mux: service.NewMux(c, ch), chat: ch}, phrase
}

func joinTable(t *testing.T, url, phrase string) *table {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ch := chat.New()
	return &table{client: c, mux: service.NewMux(c, ch), chat: ch}
}

// muxWait pulls mux events until one matches type E.
func muxWait[E any](t *testing.T, tb *table) E {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-tb.mux.Events():
			if !ok {
				t.Fatalf("mux events closed while waiting for %T", *new(E))
			}
			if e, ok := ev.(E); ok {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %T", *new(E))
		}
	}
}

func TestThreeWayChat(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)
	player := joinTable(t, url, phrase)
	spectator := joinTable(t, url, phrase)

	// Roster reaches everyone with correct roles.
	for _, tb := range []*table{host, player, spectator} {
		roster := muxWait[service.Roster](t, tb)
		for len(roster.Members) < 3 {
			roster = muxWait[service.Roster](t, tb)
		}
		if roster.Members[host.client.Self()] != session.RoleHost {
			t.Fatalf("host role in roster: %v", roster.Members)
		}
		if roster.Members[player.client.Self()] != session.RolePlayer {
			t.Fatalf("player role in roster: %v", roster.Members)
		}
		if roster.Members[spectator.client.Self()] != session.RoleSpectator {
			t.Fatalf("spectator role in roster: %v", roster.Members)
		}
	}

	// Everyone talks; everyone (including spectators) hears everyone.
	speakers := []struct {
		tb   *table
		text string
	}{
		{host, "welcome to the table"},
		{player, "good luck"},
		{spectator, "kibitzing intensifies"},
	}
	for _, sp := range speakers {
		if err := sp.tb.chat.Say(sp.text); err != nil {
			t.Fatal(err)
		}
		for _, tb := range []*table{host, player, spectator} {
			m := muxWait[chat.Message](t, tb)
			if m.Text != sp.text || m.From != sp.tb.client.Self() {
				t.Fatalf("%d heard %+v, want %q from %d", tb.client.Self(), m, sp.text, sp.tb.client.Self())
			}
		}
	}
}

func TestLateJoinerGetsChatHistory(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)

	if err := host.chat.Say("first"); err != nil {
		t.Fatal(err)
	}
	if err := host.chat.Say("second"); err != nil {
		t.Fatal(err)
	}
	muxWait[chat.Message](t, host) // drain own echoes
	muxWait[chat.Message](t, host)

	late := joinTable(t, url, phrase)
	m1 := muxWait[chat.Message](t, late)
	m2 := muxWait[chat.Message](t, late)
	if m1.Text != "first" || m2.Text != "second" {
		t.Fatalf("history: %q, %q", m1.Text, m2.Text)
	}
	if m1.From != host.client.Self() {
		t.Fatalf("history sender %d", m1.From)
	}
}

func TestRosterUpdatesOnLeave(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)
	player := joinTable(t, url, phrase)

	roster := muxWait[service.Roster](t, host)
	for len(roster.Members) < 2 {
		roster = muxWait[service.Roster](t, host)
	}

	_ = player.client.Close()
	roster = muxWait[service.Roster](t, host)
	for len(roster.Members) != 1 {
		roster = muxWait[service.Roster](t, host)
	}
	if _, ok := roster.Members[wire.ParticipantID(2)]; ok {
		t.Fatal("departed player still in roster")
	}
}
