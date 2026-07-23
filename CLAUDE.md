# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

kibitz is croc-style pairing for long-lived, end-to-end-encrypted game/chat
sessions. A host gets a code phrase (`lion-42-maple`) plus a share link and QR;
others join with it. A relay server (hosted or self-hosted, one binary)
forwards frames it can never read — the phrase seeds a PAKE handshake and all
service traffic is encrypted client-side. Layered services run over one
session: chat plus six games — chess (corentings/chess), backgammon,
connect4, checkers, reversi, battleship (shipcommit per-cell commitments).
Games start on demand from a picker (service Start() / startReq;
internal/service/game holds shared seat/lifecycle logic); rematch swaps seats.

**Game rules engines and fair dice are extracted into standalone modules**
(richardwooding/{backgammon,checkers,reversi,fairdice}); the kibitz service
packages consume them and re-export the engine API via type/const/var
aliases so `internal/service/<game>/service.go`, the WASM bridge, and the
integration tests reference the local package name. Engine changes happen
upstream in those repos, not here. connect4's engine stays in-tree (too
small to extract); chess uses the external corentings/chess/v2. A kibitzer is someone who watches a chess game and chats over
it — hence the name.

## Commands

```sh
make test                        # go test -race ./...
make wasm                        # build browser core into web/dist (+ assets + wasm_exec.js)
make serve                       # make wasm && go run ./cmd/kibitz → http://localhost:8080
make lint                        # go vet + golangci-lint (CI runs golangci-lint latest — errcheck and unused are strict)
go test ./internal/wire/ -run TestRoundTrip -v   # one test
GOOS=js GOARCH=wasm go build -o /dev/null ./cmd/kibitz-wasm   # WASM compile check (CI runs this)
```

PRs also run the codemetrics complexity ratchet — any function a PR *changes*
must stay under cognitive complexity 15.

## Architecture and invariants

**The relay is blind.** It sees session IDs (hashes of phrases — never the
phrase itself), participant counts, and opaque encrypted frames. All services
(chat, games) are client-side; there is no server-side game logic anywhere.
Never add relay features that require reading frame payloads.

**Zero `syscall/js` outside `cmd/kibitz-wasm`.** `internal/{wire,crypto,
session,service/...}` compile natively AND to WASM. The headless integration
tests in `internal/integration` (relay + native clients) exercise the exact
code the browser runs — that only stays true while this invariant holds.
Platform splits use build-tagged files (`dial_js.go` / `dial_native.go`).

- **Wire protocol** (`internal/wire`): every WS binary message is
  `[version 0x01][MsgType][CBOR body]`. The relay understands only the
  MsgType layer (create/join/direct/broadcast/membership/ping/error).
  CBOR structs use `cbor:"N,keyasint"` integer keys — never bare string keys.
- **Crypto** (`internal/crypto`): schollz/pake/v3 (curve "siec", croc's
  default) joiner↔host per pair; HKDF-SHA256 → pairwise key; host wraps a
  random 32-byte group key to each joiner; XChaCha20-Poly1305 for all AEAD
  with AD = SessionID ∥ version ∥ senderID (kills cross-session/sender
  replay). SessionID = SHA-256("kibitz/v1/session-id" ∥ phrase)[:16].
  Wrong phrase → group-key unwrap fails cleanly. No key rotation on leave
  (documented future work). Threat model: docs/THREAT-MODEL.md.
- **Services** (`internal/service`): implement `Service` (ID/Version/
  Attach/HandleFrame/Snapshot/Restore/Event). The reserved `ctl` service
  carries roles (host/player/spectator), service announcements, and snapshot
  transfer for late joiners. Roles live INSIDE the encrypted channel.
- **Game sync is both-sides-validate**: every client runs the same
  deterministic rules engine; movers broadcast `{Move, Seq, StateHash}`,
  receivers verify or surface a desync error. There is no authoritative
  server by design (the relay can't be one — it's blind).
- **Reconnect = rejoin** (re-enter phrase, re-PAKE, host re-snapshots).
  No resume tokens in MVP.
- **web/dist is generated** (gitignored except .gitkeep) — `make wasm`
  populates it and `web/embed.go` embeds it into the relay binary. The
  goreleaser before-hook runs `make wasm` so releases always embed a fresh
  client. JS↔WASM bridge is exactly two functions: `kibitz_send(json)`
  (UI→core) and `kibitzOnEvent(json)` (core→UI). JSON at the bridge only;
  CBOR on the wire. The JS layer renders — it never implements protocol.

## Releasing and deploy

Tag push (`vX.Y.Z`) triggers goreleaser: linux/darwin/windows binaries
(amd64+arm64) AND a container image at ghcr.io/richardwooding/kibitz
(ko, chainguard-static base), all with the web client embedded. CI-only
dependency bumps don't warrant a release; real dependency changes do.

The hosted instance is a Fly.io app (`kibitz`, region jnb) defined by
fly.toml, running the `:latest` ghcr image. Roll it after a release with
`fly deploy` from the repo root. The relay is stateful in-memory: it must
stay exactly ONE always-on machine — never enable auto-stop or scale count
past 1 (both kill/split live sessions). Deploys drop in-flight sessions;
that's by design (reconnect = rejoin).
