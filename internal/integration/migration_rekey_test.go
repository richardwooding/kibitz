package integration

import (
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/chat"
)

// eventuallyDelivers keeps sending `text` from sender until receiver decrypts it
// (or the deadline). It tolerates the brief window after a migration where the
// bystander hasn't yet installed the rotated key: retries land once it has.
func eventuallyDelivers(t *testing.T, sender, receiver *table, text string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	if err := sender.chat.Say(text); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev, ok := <-receiver.mux.Events():
			if !ok {
				t.Fatal("receiver mux closed before delivery")
			}
			if m, isMsg := ev.(chat.Message); isMsg && m.Text == text {
				return
			}
		case <-tick.C:
			_ = sender.chat.Say(text)
		case <-deadline:
			t.Fatalf("%d never heard %q from %d after migration", receiver.client.Self(), text, sender.client.Self())
		}
	}
}

// TestHostMigrationRekeysSurvivors drives a REAL migration through the mux
// (host leaves → player promoted) and proves the promoted host rotates the group
// key and the bystander survivor re-PAKEs to fetch it: chat from the NEW host to
// the bystander only decrypts once the bystander has installed the rotated key
// (the new host seals under it; the bystander has no other way to hold it). This
// covers the mux wiring (RotateForMigration / RekeyWithNewHost in maybeMigrate)
// that the session-layer crypto test drives by hand.
func TestHostMigrationRekeysSurvivors(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostTable(t, url)
	a := joinTable(t, url, phrase) // player, id 2 → elected successor
	b := joinTable(t, url, phrase) // spectator, id 3 → bystander survivor

	for _, tb := range []*table{a, b} {
		r := muxWait[service.Roster](t, tb)
		for len(r.Members) < 3 {
			r = muxWait[service.Roster](t, tb)
		}
	}

	// The host departs → the mux promotes A and re-keys the survivors. Waiting
	// for Promoted on A also means A finished re-Attaching (happens-before), so
	// A.chat.Say below doesn't race A's mux goroutine.
	if err := host.client.Close(); err != nil {
		t.Fatal(err)
	}
	muxWait[service.Promoted](t, a)

	// Traffic flows both ways post-migration. b→a exercises a send from the
	// bystander concurrently with its own mux re-Attach (the path that used to
	// data-race before service Context became atomic). a→b is the crypto proof:
	// the new host's rotated-key chat only decrypts once the bystander re-PAKEd
	// and installed the rotated key (retries cover the round-trip window).
	eventuallyDelivers(t, b, a, "bystander after migration")
	eventuallyDelivers(t, a, b, "new host after migration")
}
