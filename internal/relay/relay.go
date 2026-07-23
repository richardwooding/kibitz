// Package relay implements the kibitz relay server: a blind frame forwarder.
// It manages sessions and participant membership, stamps sender IDs, and
// forwards opaque payloads — it never parses anything inside a Direct or
// Broadcast payload and never learns the code phrase (only its hash).
package relay

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/richardwooding/kibitz/internal/wire"
)

type Options struct {
	MaxSessions     int           // relay-wide live session cap (default 1000)
	MaxParticipants int           // hard per-session cap; client requests are clamped to it (default 16)
	MaxAge          time.Duration // absolute session lifetime (default 24h)
	SweepEvery      time.Duration // sweeper interval (default 1m)
	IdleTimeout     time.Duration // per-connection read deadline; clients ping to stay alive (default 90s)
	ConnRate        rate.Limit    // per-IP connection attempts (default 5/min)
	ConnBurst       int           // per-IP burst (default 5)
	Logger          *slog.Logger
}

func (o *Options) defaults() {
	if o.MaxSessions <= 0 {
		o.MaxSessions = 1000
	}
	if o.MaxParticipants <= 0 {
		o.MaxParticipants = 16
	}
	if o.MaxAge <= 0 {
		o.MaxAge = 24 * time.Hour
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = time.Minute
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 90 * time.Second
	}
	if o.ConnRate <= 0 {
		o.ConnRate = rate.Every(12 * time.Second) // 5/min
	}
	if o.ConnBurst <= 0 {
		o.ConnBurst = 5
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Server is an http.Handler that upgrades to WebSocket and speaks the wire
// protocol. Mount it at /ws.
type Server struct {
	opts    Options
	reg     *registry
	limiter *ipLimiter
	stop    chan struct{}
}

func New(opts Options) *Server {
	opts.defaults()
	s := &Server{
		opts:    opts,
		reg:     newRegistry(opts.MaxSessions, opts.MaxAge),
		limiter: newIPLimiter(opts.ConnRate, opts.ConnBurst),
		stop:    make(chan struct{}),
	}
	go s.reg.sweepLoop(opts.SweepEvery, s.stop)
	return s
}

// Close stops the background sweeper. Live connections wind down with their
// own contexts.
func (s *Server) Close() { close(s.stop) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(r.RemoteAddr) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The web client may be served from a different origin than a
		// self-hosted relay; session security comes from PAKE, not Origin.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(wire.MaxFrame + 16)
	s.handle(r.Context(), conn)
}

// handle drives one connection: hello (create or join), the relay loop, then
// an explicit teardown that flushes queued frames (e.g. SessionClosed) before
// the socket closes.
func (s *Server) handle(ctx context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer conn.CloseNow()

	// kicked is the hub's way to shed this client. It must NOT cancel ctx
	// directly: coder/websocket tears down the whole connection the moment a
	// Read context dies, which would race ahead of queued frames like
	// SessionClosed. The writer drains first, then cancels.
	kicked := make(chan struct{})
	var kickOnce sync.Once
	kick := func() { kickOnce.Do(func() { close(kicked) }) }

	h, id, out, ok := s.hello(ctx, conn, kick)
	if !ok {
		cancel()
		return
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		writer(ctx, cancel, kicked, conn, out)
	}()

	s.readLoop(ctx, conn, h, id, out)

	// Teardown, in order: tell the hub we're gone (it may be gone already),
	// stop the writer — it drains anything the hub queued on the way out —
	// and only then close the socket.
	h.send(leaveCmd{id: id})
	kick()
	<-writerDone
	cancel()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func (s *Server) readLoop(ctx context.Context, conn *websocket.Conn, h *hub, id wire.ParticipantID, out chan []byte) {
	for {
		// Idle disconnect: clients heartbeat with MsgPing well inside this
		// window; a silent connection is a dead one.
		rctx, rcancel := context.WithTimeout(ctx, s.opts.IdleTimeout)
		typ, raw, err := readFrame(rctx, conn)
		rcancel()
		if err != nil {
			return
		}
		switch typ {
		case wire.MsgPing:
			p, err := wire.Body[wire.Ping](raw)
			if err == nil {
				queueFrame(out, wire.MsgPong, wire.Pong{Nonce: p.Nonce})
			}
		case wire.MsgDirect, wire.MsgBroadcast:
			h.send(frameCmd{from: id, typ: typ, raw: raw})
		default:
			queueFrame(out, wire.MsgError, wire.Error{Code: wire.ErrCodeBadFrame, Msg: "unexpected message type"})
		}
	}
}

// helloTimeout bounds how long a fresh connection may dawdle before its
// create/join frame arrives.
const helloTimeout = 10 * time.Second

// hello performs the first exchange on a fresh connection and hooks it into
// a hub. On success it returns the client's out channel with the initial
// reply already queued; the caller starts the writer that drains it.
func (s *Server) hello(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) (*hub, wire.ParticipantID, chan []byte, bool) {
	hctx, hcancel := context.WithTimeout(ctx, helloTimeout)
	typ, raw, err := readFrame(hctx, conn)
	hcancel()
	if err != nil {
		if errors.Is(err, wire.ErrUnsupportedVersion) {
			writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: wire.ErrCodeUnsupportedVersion, Msg: "unsupported protocol version"})
		}
		return nil, 0, nil, false
	}

	var h *hub
	switch typ {
	case wire.MsgCreateSession:
		cs, err := wire.Body[wire.CreateSession](raw)
		if err != nil {
			writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: wire.ErrCodeBadFrame, Msg: "bad create frame"})
			return nil, 0, nil, false
		}
		maxP := int(cs.MaxParticipants)
		if maxP <= 0 || maxP > s.opts.MaxParticipants {
			maxP = s.opts.MaxParticipants
		}
		var code uint16
		var msg string
		h, code, msg = s.reg.create(cs.SessionID, maxP)
		if h == nil {
			writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: code, Msg: msg})
			return nil, 0, nil, false
		}
	case wire.MsgJoinSession:
		js, err := wire.Body[wire.JoinSession](raw)
		if err != nil {
			writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: wire.ErrCodeBadFrame, Msg: "bad join frame"})
			return nil, 0, nil, false
		}
		var ok bool
		h, ok = s.reg.get(js.SessionID)
		if !ok {
			writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: wire.ErrCodeSessionNotFound, Msg: "session not found"})
			return nil, 0, nil, false
		}
	default:
		writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: wire.ErrCodeBadFrame, Msg: "expected create or join"})
		return nil, 0, nil, false
	}

	out := make(chan []byte, sendBuffer)
	reply := make(chan joinReply, 1)
	if !h.send(joinCmd{out: out, kick: cancel, reply: reply}) {
		writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: wire.ErrCodeSessionNotFound, Msg: "session closed"})
		return nil, 0, nil, false
	}
	jr := <-reply
	if !jr.ok {
		writeFrame(ctx, conn, wire.MsgError, wire.Error{Code: jr.errC, Msg: jr.errS})
		return nil, 0, nil, false
	}

	if typ == wire.MsgCreateSession {
		queueFrame(out, wire.MsgSessionCreated, wire.SessionCreated{ParticipantID: jr.id})
	} else {
		queueFrame(out, wire.MsgJoinResult, wire.JoinResult{OK: true, ParticipantID: jr.id, Peers: jr.peers, HostID: hostID})
	}
	return h, jr.id, out, true
}

// send delivers a command to the hub unless it has already shut down.
// Reports whether the command was accepted.
func (h *hub) send(cmd any) bool {
	select {
	case h.inbox <- cmd:
		return true
	case <-h.done:
		return false
	}
}

// writer serializes all post-hello writes for one connection. On kick it
// flushes already-queued frames — e.g. the SessionClosed notice — before
// canceling the connection context; on ctx death (peer disconnect) it just
// exits.
func writer(ctx context.Context, cancel context.CancelFunc, kicked <-chan struct{}, conn *websocket.Conn, out <-chan []byte) {
	write := func(b []byte) bool {
		// Per-write deadline independent of ctx so a frame queued just
		// before a kick still goes out.
		wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer wcancel()
		return conn.Write(wctx, websocket.MessageBinary, b) == nil
	}
	for {
		select {
		case b := <-out:
			if !write(b) {
				cancel()
				return
			}
		case <-kicked:
			for {
				select {
				case b := <-out:
					if !write(b) {
						cancel()
						return
					}
				default:
					cancel()
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func readFrame(ctx context.Context, conn *websocket.Conn) (wire.MsgType, []byte, error) {
	mt, data, err := conn.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	if mt != websocket.MessageBinary {
		return 0, nil, errors.New("relay: non-binary message")
	}
	return wire.Decode(data)
}

// writeFrame is for pre-hello replies only — once a writer goroutine owns
// the connection, use queueFrame.
func writeFrame(ctx context.Context, conn *websocket.Conn, t wire.MsgType, body any) {
	frame, err := wire.Encode(t, body)
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = conn.Write(wctx, websocket.MessageBinary, frame)
}

func queueFrame(out chan<- []byte, t wire.MsgType, body any) bool {
	frame, err := wire.Encode(t, body)
	if err != nil {
		return false
	}
	select {
	case out <- frame:
		return true
	default:
		return false
	}
}
