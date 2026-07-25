// Package pushfwd is a keyless Web Push forwarder. Browsers can't POST to a
// push service directly (CORS), so the client signs an empty VAPID push
// in-browser and hands the finished request here; this forwards it verbatim.
//
// It holds no keys and does no crypto — it cannot forge a push, only forward or
// drop one (an availability power the relay already has; see docs/THREAT-MODEL).
// It sees the recipient's push endpoint (metadata), an opaque VAPID JWT, and an
// empty body — never game content. Forwarding is restricted to known push-service
// hosts so it can't be abused as an open proxy (SSRF).
package pushfwd

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBody bounds the request: an endpoint URL plus a VAPID JWT is well under 8KB.
const maxBody = 8 << 10

// Forwarder is an http.Handler for POST /push. Client and Allow are injectable
// for tests; New wires the production defaults.
type Forwarder struct {
	Client *http.Client
	Allow  func(host string) bool
}

func New() *Forwarder {
	return &Forwarder{
		Client: &http.Client{Timeout: 10 * time.Second},
		Allow:  allowedPushHost,
	}
}

type pushReq struct {
	Endpoint      string `json:"endpoint"`
	Authorization string `json:"authorization"`
	TTL           int    `json:"ttl"`
}

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req pushReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(req.Endpoint)
	if err != nil || u.Scheme != "https" || !f.Allow(u.Hostname()) {
		http.Error(w, "endpoint not allowed", http.StatusForbidden)
		return
	}
	ttl := req.TTL
	if ttl <= 0 || ttl > 86400 {
		ttl = 60
	}

	// Forward an empty-body POST with the client's VAPID auth. No payload means
	// no Encryption headers; the recipient's service worker shows a generic
	// notification on any push.
	out, err := http.NewRequestWithContext(r.Context(), http.MethodPost, req.Endpoint, http.NoBody)
	if err != nil {
		http.Error(w, "bad endpoint", http.StatusBadRequest)
		return
	}
	out.Header.Set("Authorization", req.Authorization)
	out.Header.Set("TTL", strconv.Itoa(ttl))
	out.ContentLength = 0

	resp, err := f.Client.Do(out)
	if err != nil {
		http.Error(w, "push service unreachable", http.StatusBadGateway)
		return
	}
	_ = resp.Body.Close()
	// Mirror the push service's status so the client can drop a gone (404/410)
	// subscription. No body is relayed.
	w.WriteHeader(resp.StatusCode)
}

// allowedPushHost restricts forwarding to the major push services, so /push can
// never be turned into an open proxy to arbitrary (e.g. internal) hosts.
func allowedPushHost(host string) bool {
	host = strings.ToLower(host)
	switch {
	case host == "fcm.googleapis.com":
		return true
	case strings.HasSuffix(host, ".push.services.mozilla.com"):
		return true
	case strings.HasSuffix(host, ".notify.windows.com"):
		return true
	case strings.HasSuffix(host, ".push.apple.com"):
		return true
	default:
		return false
	}
}
