package relay

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/richardwooding/kibitz/internal/wire"
)

// testClient is a raw wire-protocol client for exercising the relay.
type testClient struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func newServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	s := New(opts)
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, srv *httptest.Server) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return &testClient{t: t, conn: conn, ctx: ctx}
}

func (c *testClient) write(t wire.MsgType, body any) {
	c.t.Helper()
	frame, err := wire.Encode(t, body)
	if err != nil {
		c.t.Fatalf("encode: %v", err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, frame); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *testClient) read() (wire.MsgType, []byte) {
	c.t.Helper()
	_, data, err := c.conn.Read(c.ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	typ, raw, err := wire.Decode(data)
	if err != nil {
		c.t.Fatalf("decode: %v", err)
	}
	return typ, raw
}

// expect reads frames until one of type want arrives, failing on anything
// unexpected other than membership notifications, which tests often ignore.
func expect[T any](c *testClient, want wire.MsgType) T {
	c.t.Helper()
	for range 10 {
		typ, raw := c.read()
		if typ == want {
			v, err := wire.Body[T](raw)
			if err != nil {
				c.t.Fatalf("body: %v", err)
			}
			return v
		}
		if typ == wire.MsgParticipantJoined || typ == wire.MsgParticipantLeft {
			continue
		}
		c.t.Fatalf("got message type %v, want %v", typ, want)
	}
	c.t.Fatalf("no %v frame in 10 messages", want)
	panic("unreachable")
}

var sid = wire.SessionID{0xaa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

func createSession(t *testing.T, srv *httptest.Server) *testClient {
	t.Helper()
	host := dial(t, srv)
	host.write(wire.MsgCreateSession, wire.CreateSession{SessionID: sid, MaxParticipants: 4})
	created := expect[wire.SessionCreated](host, wire.MsgSessionCreated)
	if created.ParticipantID != 1 {
		t.Fatalf("host ID = %d, want 1", created.ParticipantID)
	}
	return host
}

func TestCreateAndJoin(t *testing.T) {
	srv := newServer(t, Options{})
	host := createSession(t, srv)

	joiner := dial(t, srv)
	joiner.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	jr := expect[wire.JoinResult](joiner, wire.MsgJoinResult)
	if !jr.OK || jr.ParticipantID != 2 || jr.HostID != 1 {
		t.Fatalf("join result %+v", jr)
	}
	if len(jr.Peers) != 1 || jr.Peers[0] != 1 {
		t.Fatalf("peers %v, want [1]", jr.Peers)
	}

	pj := expect[wire.ParticipantJoined](host, wire.MsgParticipantJoined)
	if pj.ParticipantID != 2 {
		t.Fatalf("host notified of %d, want 2", pj.ParticipantID)
	}
}

func TestDirectStampsFrom(t *testing.T) {
	srv := newServer(t, Options{})
	host := createSession(t, srv)
	joiner := dial(t, srv)
	joiner.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	expect[wire.JoinResult](joiner, wire.MsgJoinResult)

	// Joiner lies about From — the relay must overwrite it.
	joiner.write(wire.MsgDirect, wire.Direct{To: 1, From: 99, Payload: []byte("hello")})
	d := expect[wire.Direct](host, wire.MsgDirect)
	if d.From != 2 {
		t.Fatalf("From = %d, want relay-stamped 2", d.From)
	}
	if string(d.Payload) != "hello" {
		t.Fatalf("payload %q", d.Payload)
	}
}

func TestBroadcastExcludesSender(t *testing.T) {
	srv := newServer(t, Options{})
	host := createSession(t, srv)
	j2 := dial(t, srv)
	j2.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	expect[wire.JoinResult](j2, wire.MsgJoinResult)
	j3 := dial(t, srv)
	j3.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	expect[wire.JoinResult](j3, wire.MsgJoinResult)

	j2.write(wire.MsgBroadcast, wire.Broadcast{Payload: []byte("all")})
	for _, c := range []*testClient{host, j3} {
		b := expect[wire.Broadcast](c, wire.MsgBroadcast)
		if b.From != 2 || string(b.Payload) != "all" {
			t.Fatalf("broadcast %+v", b)
		}
	}
	// Sender must NOT receive its own broadcast: send a ping and ensure the
	// next frame is the pong, not an echoed broadcast.
	j2.write(wire.MsgPing, wire.Ping{Nonce: 7})
	if p := expect[wire.Pong](j2, wire.MsgPong); p.Nonce != 7 {
		t.Fatalf("pong nonce %d", p.Nonce)
	}
}

func TestJoinMissingSession(t *testing.T) {
	srv := newServer(t, Options{})
	c := dial(t, srv)
	c.write(wire.MsgJoinSession, wire.JoinSession{SessionID: wire.SessionID{0xff}})
	e := expect[wire.Error](c, wire.MsgError)
	if e.Code != wire.ErrCodeSessionNotFound {
		t.Fatalf("error code %d", e.Code)
	}
}

func TestSessionFull(t *testing.T) {
	srv := newServer(t, Options{MaxParticipants: 2})
	createSession(t, srv)
	j2 := dial(t, srv)
	j2.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	expect[wire.JoinResult](j2, wire.MsgJoinResult)

	j3 := dial(t, srv)
	j3.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	e := expect[wire.Error](j3, wire.MsgError)
	if e.Code != wire.ErrCodeSessionFull {
		t.Fatalf("error code %d", e.Code)
	}
}

func TestDuplicateCreate(t *testing.T) {
	srv := newServer(t, Options{})
	createSession(t, srv)
	c := dial(t, srv)
	c.write(wire.MsgCreateSession, wire.CreateSession{SessionID: sid, MaxParticipants: 4})
	e := expect[wire.Error](c, wire.MsgError)
	if e.Code != wire.ErrCodeSessionExists {
		t.Fatalf("error code %d", e.Code)
	}
}

func TestHostLeaveClosesSession(t *testing.T) {
	srv := newServer(t, Options{})
	host := createSession(t, srv)
	joiner := dial(t, srv)
	joiner.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	expect[wire.JoinResult](joiner, wire.MsgJoinResult)

	_ = host.conn.Close(websocket.StatusNormalClosure, "bye")
	sc := expect[wire.SessionClosed](joiner, wire.MsgSessionClosed)
	if sc.Reason != "host left" {
		t.Fatalf("reason %q", sc.Reason)
	}

	// The session must be gone: a new join fails.
	c := dial(t, srv)
	c.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	e := expect[wire.Error](c, wire.MsgError)
	if e.Code != wire.ErrCodeSessionNotFound {
		t.Fatalf("error code %d", e.Code)
	}
}

func TestPeerLeaveNotifies(t *testing.T) {
	srv := newServer(t, Options{})
	host := createSession(t, srv)
	joiner := dial(t, srv)
	joiner.write(wire.MsgJoinSession, wire.JoinSession{SessionID: sid})
	expect[wire.JoinResult](joiner, wire.MsgJoinResult)
	expect[wire.ParticipantJoined](host, wire.MsgParticipantJoined)

	_ = joiner.conn.Close(websocket.StatusNormalClosure, "bye")
	pl := expect[wire.ParticipantLeft](host, wire.MsgParticipantLeft)
	if pl.ParticipantID != 2 {
		t.Fatalf("left ID %d", pl.ParticipantID)
	}
}

func TestDirectToUnknownPeer(t *testing.T) {
	srv := newServer(t, Options{})
	host := createSession(t, srv)
	host.write(wire.MsgDirect, wire.Direct{To: 42, Payload: []byte("x")})
	e := expect[wire.Error](host, wire.MsgError)
	if e.Code != wire.ErrCodeUnknownPeer {
		t.Fatalf("error code %d", e.Code)
	}
}

func TestMaxAgeSweep(t *testing.T) {
	srv := newServer(t, Options{MaxAge: 50 * time.Millisecond, SweepEvery: 20 * time.Millisecond})
	host := createSession(t, srv)
	sc := expect[wire.SessionClosed](host, wire.MsgSessionClosed)
	if sc.Reason != "session expired" {
		t.Fatalf("reason %q", sc.Reason)
	}
}

func TestConnRateLimit(t *testing.T) {
	srv := newServer(t, Options{ConnRate: rate.Every(time.Hour), ConnBurst: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	for i := 0; i < 2; i++ {
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("dial %d within burst: %v", i, err)
		}
		_ = conn.CloseNow()
	}
	if _, _, err := websocket.Dial(ctx, url, nil); err == nil {
		t.Fatal("third dial exceeded burst but was accepted")
	}
}

func TestSessionCapacity(t *testing.T) {
	srv := newServer(t, Options{MaxSessions: 1})
	createSession(t, srv)
	c := dial(t, srv)
	c.write(wire.MsgCreateSession, wire.CreateSession{SessionID: wire.SessionID{0xbb}, MaxParticipants: 2})
	e := expect[wire.Error](c, wire.MsgError)
	if e.Code != wire.ErrCodeRateLimited {
		t.Fatalf("error code %d", e.Code)
	}
}
