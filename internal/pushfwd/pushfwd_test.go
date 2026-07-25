package pushfwd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowedPushHost(t *testing.T) {
	ok := []string{
		"fcm.googleapis.com",
		"updates.push.services.mozilla.com",
		"web.push.apple.com",
		"AB1.notify.windows.com",
	}
	for _, h := range ok {
		if !allowedPushHost(h) {
			t.Errorf("want allowed: %s", h)
		}
	}
	bad := []string{
		"localhost", "127.0.0.1", "169.254.169.254", "evil.com",
		"fcm.googleapis.com.evil.com", "push.apple.com.evil.com", "",
	}
	for _, h := range bad {
		if allowedPushHost(h) {
			t.Errorf("want rejected: %s", h)
		}
	}
}

func post(t *testing.T, f *Forwarder, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/push", bytes.NewReader(b))
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	return w
}

// TestForwardsToAllowedEndpoint: a permitted https endpoint gets an empty-body
// POST carrying the client's Authorization + TTL, and the push service's status
// is mirrored back to the caller.
func TestForwardsToAllowedEndpoint(t *testing.T) {
	var gotAuth, gotTTL, gotMethod string
	var gotBodyLen int64
	push := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotTTL = r.Header.Get("TTL")
		gotBodyLen = r.ContentLength
		w.WriteHeader(http.StatusCreated)
	}))
	defer push.Close()

	f := &Forwarder{Client: push.Client(), Allow: func(string) bool { return true }}
	w := post(t, f, pushReq{Endpoint: push.URL, Authorization: "vapid t=jwt, k=pub", TTL: 90})

	if w.Code != http.StatusCreated {
		t.Fatalf("mirrored status %d, want 201", w.Code)
	}
	if gotMethod != http.MethodPost || gotAuth != "vapid t=jwt, k=pub" || gotTTL != "90" || gotBodyLen != 0 {
		t.Fatalf("forwarded req: method=%s auth=%q ttl=%q bodyLen=%d", gotMethod, gotAuth, gotTTL, gotBodyLen)
	}
}

// TestMirrorsGoneStatus: a 410 (expired subscription) is mirrored so the client
// can drop the endpoint.
func TestMirrorsGoneStatus(t *testing.T) {
	push := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer push.Close()
	f := &Forwarder{Client: push.Client(), Allow: func(string) bool { return true }}
	w := post(t, f, pushReq{Endpoint: push.URL, Authorization: "x", TTL: 60})
	if w.Code != http.StatusGone {
		t.Fatalf("status %d, want 410", w.Code)
	}
}

func TestRejectsNonPost(t *testing.T) {
	f := New()
	r := httptest.NewRequest(http.MethodGet, "/push", nil)
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", w.Code)
	}
}

func TestRejectsDisallowedEndpoint(t *testing.T) {
	f := New()
	w := post(t, f, pushReq{Endpoint: "https://169.254.169.254/latest/meta-data", Authorization: "x"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("SSRF target status %d, want 403", w.Code)
	}
}

func TestRejectsNonHTTPS(t *testing.T) {
	f := &Forwarder{Client: http.DefaultClient, Allow: func(string) bool { return true }}
	w := post(t, f, pushReq{Endpoint: "http://fcm.googleapis.com/x", Authorization: "x"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("http endpoint status %d, want 403", w.Code)
	}
}
