// Package integration spins up a real relay and real session clients — the
// exact packages the WASM browser core uses — and exercises full flows over
// live WebSockets. This is the load-bearing test layer: the browser adds only
// a DOM on top of what is verified here.
package integration

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/crypto"
	"github.com/richardwooding/kibitz/internal/relay"
	"github.com/richardwooding/kibitz/internal/session"
	"github.com/richardwooding/kibitz/internal/wire"
)

func startRelay(t *testing.T) string {
	t.Helper()
	s := relay.New(relay.Options{})
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// waitFor pulls events until one matches type E.
func waitFor[E any](t *testing.T, c *session.Client) E {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("events closed while waiting for %T", *new(E))
			}
			if e, ok := ev.(E); ok {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %T", *new(E))
		}
	}
}

func TestEncryptedEcho(t *testing.T) {
	url := startRelay(t)
	ctx := testCtx(t)

	host, phrase, err := session.Host(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	if host.Self() != 1 || host.Role() != session.RoleHost {
		t.Fatalf("host self=%d role=%d", host.Self(), host.Role())
	}

	joiner, err := session.Join(ctx, url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = joiner.Close() }()
	if joiner.Role() != session.RolePlayer {
		t.Fatalf("first joiner role = %d, want player", joiner.Role())
	}

	keyed := waitFor[session.MemberKeyed](t, host)
	if keyed.ID != joiner.Self() || keyed.Role != session.RolePlayer {
		t.Fatalf("host saw keyed %+v", keyed)
	}

	// Joiner → host.
	if err := joiner.Broadcast("echo", []byte("ping")); err != nil {
		t.Fatal(err)
	}
	f := waitFor[session.Frame](t, host)
	if f.From != joiner.Self() || f.Envelope.ServiceID != "echo" || string(f.Envelope.Body) != "ping" {
		t.Fatalf("host got %+v", f)
	}
	if f.Envelope.Seq != 1 {
		t.Fatalf("first frame seq = %d", f.Envelope.Seq)
	}

	// Host → joiner, directed.
	if err := host.SendTo(joiner.Self(), "echo", []byte("pong")); err != nil {
		t.Fatal(err)
	}
	f2 := waitFor[session.Frame](t, joiner)
	if f2.From != host.Self() || string(f2.Envelope.Body) != "pong" {
		t.Fatalf("joiner got %+v", f2)
	}
}

func TestWrongPhraseRejected(t *testing.T) {
	url := startRelay(t)
	ctx := testCtx(t)

	host, phrase, err := session.Host(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()

	// Same session ID requires the same phrase — so derive a wrong phrase
	// that still lands on the host's session by perturbing... impossible by
	// construction: a different phrase hashes to a different session and the
	// join fails with not-found. That IS the wrong-phrase UX for typos that
	// change the hash. The crypto-level wrong-phrase path (same session,
	// wrong secret) needs a hand-rolled joiner, covered in the crypto
	// package. Here, assert the not-found path is clean.
	shortCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = session.Join(shortCtx, url, phrase+"x")
	if err == nil {
		t.Fatal("join with wrong phrase succeeded")
	}
	if errors.Is(err, crypto.ErrUnwrap) {
		return // acceptable: unwrap failure
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecondJoinerIsSpectator(t *testing.T) {
	url := startRelay(t)
	ctx := testCtx(t)

	host, phrase, err := session.Host(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()

	j1, err := session.Join(ctx, url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j1.Close() }()
	j2, err := session.Join(ctx, url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j2.Close() }()

	if j1.Role() != session.RolePlayer {
		t.Fatalf("j1 role %d", j1.Role())
	}
	if j2.Role() != session.RoleSpectator {
		t.Fatalf("j2 role %d", j2.Role())
	}

	// Broadcast reaches both other participants, encrypted.
	if err := host.Broadcast("chat", []byte("hello table")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*session.Client{j1, j2} {
		f := waitFor[session.Frame](t, c)
		if string(f.Envelope.Body) != "hello table" {
			t.Fatalf("got %q", f.Envelope.Body)
		}
	}
}

func TestHostCloseEndsSession(t *testing.T) {
	url := startRelay(t)
	ctx := testCtx(t)

	host, phrase, err := session.Host(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	joiner, err := session.Join(ctx, url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = joiner.Close() }()

	_ = host.Close()
	closed := waitFor[session.Closed](t, joiner)
	if closed.Reason != "host left" {
		t.Fatalf("reason %q", closed.Reason)
	}
}

// TestEavesdropperCannotReadTraffic pins the E2E property with a real
// adversary: a raw wire-level client that joins the session (it can — session
// IDs are not secrets) but doesn't know the phrase. It receives the encrypted
// broadcast and must find no plaintext in it and fail to decrypt it.
func TestEavesdropperCannotReadTraffic(t *testing.T) {
	url := startRelay(t)
	ctx := testCtx(t)

	host, phrase, err := session.Host(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()

	// The eavesdropper speaks raw wire protocol, joining with the session ID
	// (which the relay knows) but no phrase.
	eve := dialRaw(t, ctx, url)
	sid := phraseSessionID(phrase)
	eve.write(t, wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	jr := readAs[wire.JoinResult](t, eve, wire.MsgJoinResult)
	if !jr.OK {
		t.Fatalf("eve couldn't even join: %+v", jr)
	}

	secret := "the queen sacrifice on move 12"
	if err := host.Broadcast("chat", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	b := readAs[wire.Broadcast](t, eve, wire.MsgBroadcast)
	if strings.Contains(string(b.Payload), secret) {
		t.Fatal("plaintext visible in relayed payload")
	}
	kind, praw, err := wire.DecodePayload(b.Payload)
	if err != nil || kind != wire.KindSealed {
		t.Fatalf("payload kind %v err %v", kind, err)
	}
	sf, err := wire.Body[wire.SealedFrame](praw)
	if err != nil {
		t.Fatal(err)
	}
	// Eve tries a made-up key — authentication must fail.
	var guess crypto.Key
	if _, err := crypto.Open(guess, sf, sid, host.Self()); !errors.Is(err, crypto.ErrOpen) {
		t.Fatalf("open with guessed key: %v", err)
	}
}
