// Command kibitz is the relay server. It serves the embedded web client at /
// and (from M1) the WebSocket relay at /ws. The relay only ever forwards
// opaque encrypted frames between session participants — it can never read
// service traffic (see docs/THREAT-MODEL.md).
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/richardwooding/kibitz/internal/pushfwd"
	"github.com/richardwooding/kibitz/internal/relay"
	"github.com/richardwooding/kibitz/web"
)

// precompressed serves an embedded asset's brotli (.br) or gzip (.gz) sibling
// when the client accepts it, else falls back to the raw FileServer. This keeps
// the relay a dumb static server while shipping the smallest bytes; the wasm
// core and the whole JS/CSS/HTML shell are precompressed at build time.
func precompressed(dist fs.FS, raw http.Handler) http.HandlerFunc {
	encs := []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			raw.ServeHTTP(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		ae := r.Header.Get("Accept-Encoding")
		for _, e := range encs {
			if !strings.Contains(ae, e.token) {
				continue
			}
			b, err := fs.ReadFile(dist, p+e.ext)
			if err != nil {
				continue
			}
			h := w.Header()
			h.Set("Content-Encoding", e.token)
			h.Add("Vary", "Accept-Encoding")
			h.Set("Content-Type", contentType(p))
			_, _ = w.Write(b)
			return
		}
		raw.ServeHTTP(w, r)
	}
}

// contentType maps a served path to its media type (the precompressed sibling
// hides the real extension from net/http's sniffer, so we set it explicitly).
func contentType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".wasm":
		return "application/wasm"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// version is stamped by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

// displayVersion is what the UI shows: "dev" as-is, otherwise "vX.Y.Z".
func displayVersion() string {
	if version == "dev" || version == "" {
		return "dev"
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	maxSessions := flag.Int("max-sessions", 1000, "maximum concurrent sessions")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("kibitz", version)
		os.Exit(0)
	}
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatalf("embedded web client: %v", err)
	}

	mux := http.NewServeMux()
	files := http.FileServerFS(dist)
	// Serve the smallest precompressed encoding each client accepts (brotli, then
	// gzip), falling back to the raw file. `make wasm` precompresses every
	// servable asset — the ~9MB wasm core AND the JS/CSS/HTML shell — so a first
	// mobile load ships ~2MB (brotli) instead of ~2.6MB.
	mux.Handle("/", precompressed(dist, files))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
	// The web client fetches this to show its version badge. Return the
	// display form: "dev" for local builds, else "vX.Y.Z".
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, displayVersion())
	})
	relaySrv := relay.New(relay.Options{MaxSessions: *maxSessions})
	defer relaySrv.Close()
	mux.Handle("/ws", relaySrv)
	// Keyless Web Push forwarder: clients sign an empty VAPID push in-browser
	// (CORS forbids posting to push services directly) and this forwards it. It
	// holds no keys and sees no game content — see internal/pushfwd.
	mux.Handle("/push", pushfwd.New())

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout/ReadTimeout: /ws connections are long-lived; the
		// relay enforces its own per-frame idle deadline.
	}
	log.Printf("kibitz %s listening on %s", version, *listen)
	log.Fatal(srv.ListenAndServe())
}
