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
	"strings"
	"time"

	"github.com/richardwooding/kibitz/internal/relay"
	"github.com/richardwooding/kibitz/web"
)

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
	mux.Handle("/", files)
	// The WASM core is the one heavy asset (~8.6MB); serve the precompressed
	// gzip (~2.5MB) when the client accepts it. instantiateStreaming needs
	// the application/wasm content type either way.
	mux.HandleFunc("/kibitz.wasm", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			if gz, err := web.Dist.ReadFile("dist/kibitz.wasm.gz"); err == nil {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", "application/wasm")
				_, _ = w.Write(gz)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
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
