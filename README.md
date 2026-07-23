# kibitz

> Pull up a chair.

**kibitz** is [croc](https://github.com/schollz/croc)-style pairing for
long-lived, end-to-end-encrypted sessions — chat and turn-based games (chess
first, backgammon next) instead of file transfer.

- **Pair like croc**: the host gets a code phrase like `lion-42-maple`, plus a
  share link and QR code. Friends join by clicking or typing.
- **The relay can't read anything**: the phrase seeds a PAKE key exchange;
  the relay only forwards opaque encrypted frames. Self-host it or use a
  hosted one — either way it's blind.
- **One binary**: the relay embeds the whole web client. `kibitz --listen
  :8080`, open a browser, play.
- **Spectators welcome**: sessions hold two players and any number of
  kibitzers, all in the same encrypted chat.

## Status

**v0.2 — two games playable.** Sessions, encrypted chat, full chess, and
backgammon (with provably fair commit-reveal dice — neither player nor the
relay can steer a roll, and spectators verify every one) all work end to end.
Both games run side by side in one session; switch with the tabs.

## How it works

The relay forwards frames it can never read. The code phrase seeds a PAKE
handshake (as in croc); the host wraps a session group key to each joiner;
all chat and moves are XChaCha20-Poly1305 envelopes. Games are
both-sides-validate: every client runs the same rules engine and checks a
position hash on every move — there's no server to cheat past, because the
server is blind. Details: [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md).

## Hosted instance

A public relay runs at **https://kibitz-play.fly.dev** — open it, start a table,
share the phrase. (Remember: the relay is blind either way; you never have to
trust it.)

## Self-hosting

Grab a release binary (or `go install github.com/richardwooding/kibitz/cmd/kibitz@latest`) and:

```sh
kibitz --listen :8080
```

Or run the container image:

```sh
docker run -p 8080:8080 ghcr.io/richardwooding/kibitz
```

Put TLS in front with your reverse proxy of choice. That's it — the web UI,
relay, and everything else is in the one binary. Useful flags:
`--max-sessions` (default 1000), `--version`.

## Development

```sh
make serve    # build the WASM client and run the relay on :8080
make test     # go test -race ./...
```

## License

MIT
