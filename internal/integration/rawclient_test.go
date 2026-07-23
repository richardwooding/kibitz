package integration

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/richardwooding/kibitz/internal/phrase"
	"github.com/richardwooding/kibitz/internal/wire"
)

// rawClient speaks bare wire protocol with no session engine on top — used
// to play adversary or exercise relay edge cases from integration tests.
type rawClient struct {
	conn *websocket.Conn
	ctx  context.Context
}

func dialRaw(t *testing.T, ctx context.Context, url string) *rawClient {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return &rawClient{conn: conn, ctx: ctx}
}

func (r *rawClient) write(t *testing.T, typ wire.MsgType, body any) {
	t.Helper()
	frame, err := wire.Encode(typ, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Write(r.ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
}

// readAs reads frames until one of the wanted type arrives.
func readAs[T any](t *testing.T, r *rawClient, want wire.MsgType) T {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := r.conn.Read(r.ctx)
		if err != nil {
			t.Fatalf("raw read: %v", err)
		}
		typ, raw, err := wire.Decode(data)
		if err != nil {
			t.Fatalf("raw decode: %v", err)
		}
		if typ != want {
			continue
		}
		v, err := wire.Body[T](raw)
		if err != nil {
			t.Fatalf("raw body: %v", err)
		}
		return v
	}
	t.Fatalf("no %v frame before deadline", want)
	panic("unreachable")
}

func phraseSessionID(p string) wire.SessionID {
	return phrase.SessionID(p)
}
