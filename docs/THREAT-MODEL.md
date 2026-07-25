# kibitz threat model

kibitz's security promise is croc's, extended to long-lived sessions: **the
relay is blind**. Anyone can run a relay; nobody should have to trust one.

## How the crypto works

1. The host's browser generates the code phrase (`lion-42-maple`, ~2²⁷
   combinations) and a random 32-byte **group key**. The phrase never leaves
   the clients — the relay sees only `SessionID =
   SHA-256("kibitz/v1/session-id" ∥ phrase)[:16]`.
2. Each joiner runs a **PAKE** (schollz/pake/v3, SPAKE2-like, curve `siec` —
   the same construction croc uses) with the host through relay-forwarded
   opaque frames. Both ends derive a pairwise key via HKDF-SHA256 bound to the
   session ID and the participant pair.
3. The host wraps the group key (plus the joiner's role) to each joiner with
   XChaCha20-Poly1305 under the pairwise key, with associated data binding the
   session and joiner identity.
4. All service traffic (chat, moves) is an encrypted envelope:
   XChaCha20-Poly1305 under the group key, random 24-byte nonces, AD =
   `SessionID ∥ protocol version ∥ senderID`. The relay stamps sender IDs on
   forwarded frames, so a frame sealed by participant 2 cannot be replayed
   as participant 3, in another session, or under another protocol version.

A wrong phrase produces a garbage pairwise key, the group-key unwrap fails
authentication, and the joiner gets a clean "wrong phrase" error. **Every
phrase guess costs an online round-trip** against a rate-limited relay
(5 connection attempts/min/IP by default) — that is what makes a 27-bit
phrase adequate.

## What the relay learns (metadata — by design)

- Session IDs (phrase hashes), session count, creation time and lifetime
- Participant counts, join/leave times, relay-assigned participant IDs
- Client IP addresses
- Frame sizes, timing, and direction (who talks to whom: direct vs broadcast)
- Per-participant **reclaim tokens** it mints for reconnect (opaque random
  bytes it only equality-compares — see "Reconnect" below)

Traffic analysis of move timing trivially reveals "this is probably a chess
game". That is out of scope.

## What the relay cannot do

- Read or forge any service traffic (chat, moves, snapshots, roles)
- Learn the code phrase, or join the session as a participant (it never sees
  the phrase, and PAKE defeats an active MITM without it)
- Replay frames across sessions or senders (AEAD associated-data binding)

## What the relay CAN do (availability attacks)

Drop, delay, reorder, or partition traffic, and close sessions. Per-sender
sequence numbers detect gaps (surfaced as desync errors); nothing hides an
unavailable relay. If you don't trust a relay to stay up, run your own.

## Reconnect (session resume)

A dropped connection (flaky network, backgrounded tab, brief host blip) is
recovered without losing the game. This adds relay behaviour but not relay
knowledge:

- At create/join the relay mints a 32-byte random **reclaim token** and returns
  it to that participant. On an unexpected drop the relay holds the slot for a
  short grace window (~30s) instead of evicting it; a reconnecting client
  presents the token to reclaim its **same participant id** (constant-time
  compared). The token is opaque to the relay — it grants only slot
  re-attachment, never decryption (the group key never touches the relay).
- **The relay stays blind.** Tokens carry no phrase, name, or role; payloads
  remain E2E-encrypted throughout a reconnect (the group key and per-sender
  AEAD binding are unchanged — the participant id is preserved, so no re-key).
- **Token exposure.** A token travels client↔relay in the clear at the wire
  layer, so it relies on transport TLS (wss) like the SessionID does. A stolen
  token lets an attacker hijack that participant's relay slot (a targeted
  availability attack — they receive ciphertext they cannot decrypt); it never
  grants plaintext. Over plaintext ws (self-hosted, no TLS) it is exposed to a
  network eavesdropper, same threat class as the SessionID.
- **Bounded.** The grace hold is short and per-slot; a full relay restart drops
  all tokens and sessions (in-memory only — reconnect then falls back to a
  fresh rejoin). No new persistent state.

## Turn notifications (Web Push)

Opt-in "your turn" notifications let friends play across the day. Browsers can't
POST to a push service directly (CORS), so the server has to forward — but it
never gains keys or content:

- **VAPID keys are client-held, ephemeral, and E2E-distributed.** The host mints
  a per-session VAPID keypair in-browser; it travels to members inside the
  encrypted `ctl` channel (like the group key), never to the relay. Each member
  shares only its push *endpoint* over `ctl`.
- **Notifications carry no payload.** A push is an empty wake; the service worker
  shows a generic "your turn". No move, board, name, or phrase is ever sent —
  there is nothing in a push to read.
- **The `/push` route is a keyless, content-blind forwarder.** The mover signs an
  empty VAPID push in-browser and hands the finished request to `/push`, which
  forwards it verbatim to the push service. The relay holds no VAPID key, so it
  **cannot forge** a push — only forward or drop it (an availability power it
  already has). It sees the recipient's push endpoint (metadata) and an opaque
  JWT; forwarding is **restricted to known push-service hosts** so `/push` can't
  be used as an open proxy (SSRF).
- **What the relay learns (metadata):** push endpoints (stable per-device
  identifiers from FCM/Mozilla/Apple/WNS) and notification timing. Not content.
- **Endpoint exposure** rides the same TLS assumption as the SessionID/tokens;
  over plaintext ws a network eavesdropper sees endpoints (metadata), never
  plaintext.

## Trust assumptions

- **The host is trusted.** It holds the group key and assigns roles. That's
  fine: the host is a player, not infrastructure.
- **Everyone who knows the phrase is inside the boundary.** Spectators
  decrypt everything, including the players' chat.
- **No key rotation on leave (MVP).** A departed participant who colludes
  with the relay to keep receiving frames can still decrypt them. Acceptable
  for casual games; host-initiated re-keying is future work (the protocol's
  group-key wrap already supports it).
- Games are **both-sides-validate**: each client runs the same rules engine
  and checks a position hash on every move. A cheating client can't make an
  illegal move stick; it can only cause a visible desync.

## Non-goals

- Anonymity (the relay sees IPs; use your own transport-level protections)
- Hiding that kibitz is in use, or which session sizes/timings exist
- Perfect forward secrecy within a session
