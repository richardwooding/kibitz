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

Early days — the scaffold is up, the protocol is being built. See milestones
in the repo issues.

## Self-hosting

```sh
kibitz --listen :8080
```

Put TLS in front with your reverse proxy of choice. That's it — the web UI,
relay, and everything else is in the one binary.

## Development

```sh
make serve    # build the WASM client and run the relay on :8080
make test     # go test -race ./...
```

## License

MIT
