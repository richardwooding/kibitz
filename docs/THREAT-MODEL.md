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
