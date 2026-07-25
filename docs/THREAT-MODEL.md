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

## Host migration

When the host leaves permanently the session no longer ends — a surviving
participant is promoted to host so play/chat/spectating continue. This changes
the authority model but not the key model:

- **The relay stops treating the host specially.** Any departure broadcasts
  `ParticipantLeft`; the hub closes only when the last participant leaves. It
  tracks a mutable current-host id purely to route new joiners' handshake,
  updated by an opaque `ClaimHost` message.
- **Succession is decided in the encrypted channel**, not by the relay: every
  survivor deterministically elects the same successor from the ctl roster; the
  elected one promotes itself and claims host. The relay only records the claim.
- **The departing host is not locked out.** The successor already holds the
  group key (it was keyed) and wraps that *same* key to future joiners. Unlike a
  non-host leaver — who **is** locked out by a rekey (see "Key rotation on
  member-leave" below) — a departing host cannot be: the successor only ran a
  PAKE with the *original* host, so it shares no pairwise channel with the other
  members to distribute a fresh key. Rotating on a host departure would require
  the survivors to re-PAKE with the new host (future work); until then the
  departed host still knows the current key.
- **`ClaimHost` is unauthenticated at the blind relay.** A malicious *member*
  (already inside the trust boundary — it holds the group key) could claim host
  to hijack *new-joiner routing* — a denial-of-service on new joins, never a key
  compromise or a way to read traffic. Existing members are unaffected (they
  keep the key and coordinate succession among themselves).

## Key rotation on member-leave (forward secrecy)

When a member leaves, a participant who kept a copy of the group key could —
colluding with the relay to keep receiving frames — still decrypt whatever is
said afterwards. To close that, the host **rotates the group key on a leave**:

- **What happens.** When a non-host member departs, the host generates a fresh
  32-byte group key and re-wraps it to each *surviving* member under that
  member's pairwise PAKE key (the same wrap used at join, `KindRekey`), then
  switches its own key. Survivors install the new key; the departed member never
  receives it, so it is locked out of all subsequent traffic. This rides the
  encrypted layer — the relay only sees opaque Direct frames, as ever.
- **No mixed-key desync.** Each end keeps a short ring of recently-superseded
  keys and tries them when a frame won't open under the current one, so a frame
  still in flight under the old key when the rotation lands is decrypted
  normally. Senders always seal under the newest key; per-sender ordering means
  the host's own new-key frames never arrive before the rekey that precedes them.
- **Requires the original host.** Re-wrapping needs a pairwise key with every
  survivor, which only the original host holds (it PAKE'd with each). A promoted
  successor lacks pairwise channels with the members it never handshook, so it
  keeps the current key (see "Host migration"). Rotation is therefore a no-op
  after a host migration until a re-PAKE mechanism exists.
- **Scope.** This gives forward secrecy across a **non-host member's departure**
  from an original-host session. It does not provide forward secrecy within a
  continuous membership, nor against a departing host.

## Trust assumptions

- **The host is trusted.** It holds the group key and assigns roles. That's
  fine: the host is a player, not infrastructure.
- **Everyone who knows the phrase is inside the boundary.** Spectators
  decrypt everything, including the players' chat.
- **Key rotation on member-leave (partial).** When a non-host member leaves an
  original-host session, the host rotates the group key so the departed member
  is locked out of later traffic (see "Key rotation on member-leave"). A
  *departing host* is not locked out, and a current member reads everything
  while present — so this is forward secrecy across a non-host departure, not
  full forward secrecy.
- Games are **both-sides-validate**: each client runs the same rules engine
  and checks a position hash on every move. A cheating client can't make an
  illegal move stick; it can only cause a visible desync.

## Non-goals

- Anonymity (the relay sees IPs; use your own transport-level protections)
- Hiding that kibitz is in use, or which session sizes/timings exist
- Full forward secrecy. A member reads all traffic while present, and a
  departing host keeps the key. The group key IS rotated when a non-host member
  leaves an original-host session, locking that member out of later traffic
  (see "Key rotation on member-leave").
