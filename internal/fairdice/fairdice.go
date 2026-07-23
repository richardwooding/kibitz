// Package fairdice implements commit-reveal dice between untrusting peers.
//
// Neither player can be trusted to roll alone (there's no server — the relay
// is blind), so a roll is a three-message exchange:
//
//  1. The roller generates random bytes RA and a salt, and broadcasts
//     Commit = SHA-256(deterministic-CBOR(Reveal{RA, Salt})).
//  2. The opponent broadcasts its own random RB (no commitment needed —
//     it moves second and the roller is already bound).
//  3. The roller broadcasts Reveal{RA, Salt}; everyone (spectators too)
//     checks it against the commit and derives Dice(RA, RB).
//
// The roller can't pick dice (bound before seeing RB); the opponent can't
// either (RA is fixed but unknown when choosing RB, so any RB yields
// uniformly distributed dice). A roller whose reveal doesn't match its
// commit is caught by every participant.
package fairdice

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/richardwooding/kibitz/internal/wire"
)

// Commit is the roller's binding hash.
type Commit [32]byte

// Reveal is what the roller discloses after the opponent responds. It is
// CBOR-encoded (deterministic mode, integer keys) before hashing, so the
// commitment preimage is unambiguous.
type Reveal struct {
	RA   [32]byte `cbor:"1,keyasint"`
	Salt [16]byte `cbor:"2,keyasint"`
}

// Response is the opponent's contribution.
type Response [32]byte

// NewRoll starts a roll: fresh randomness plus its commitment.
func NewRoll() (Reveal, Commit, error) {
	var r Reveal
	if _, err := rand.Read(r.RA[:]); err != nil {
		return Reveal{}, Commit{}, fmt.Errorf("fairdice: rand: %w", err)
	}
	if _, err := rand.Read(r.Salt[:]); err != nil {
		return Reveal{}, Commit{}, fmt.Errorf("fairdice: rand: %w", err)
	}
	c, err := r.Commitment()
	if err != nil {
		return Reveal{}, Commit{}, err
	}
	return r, c, nil
}

// NewResponse is the opponent's random contribution.
func NewResponse() (Response, error) {
	var rb Response
	if _, err := rand.Read(rb[:]); err != nil {
		return Response{}, fmt.Errorf("fairdice: rand: %w", err)
	}
	return rb, nil
}

// Commitment computes the binding hash of a Reveal.
func (r Reveal) Commitment() (Commit, error) {
	b, err := wire.Marshal(r)
	if err != nil {
		return Commit{}, fmt.Errorf("fairdice: encode reveal: %w", err)
	}
	return sha256.Sum256(b), nil
}

// Verify reports whether a reveal matches its earlier commitment. False
// means the roller cheated (or the messages were corrupted — either way,
// the roll is void).
func Verify(c Commit, r Reveal) bool {
	got, err := r.Commitment()
	if err != nil {
		return false
	}
	return got == c
}

// Dice derives two dice from the combined randomness, uniformly via
// rejection sampling. Deterministic: every participant derives the same
// pair from the same (reveal, response).
func Dice(r Reveal, rb Response) (d1, d2 int) {
	seed := sha256.Sum256(append(append([]byte{}, r.RA[:]...), rb[:]...))
	dice := sample(seed, 2)
	return dice[0], dice[1]
}

// Opening derives the backgammon opening roll: one die for the roller, one
// for the responder, guaranteed unequal (equal pairs are skipped, mirroring
// the tabletop re-roll rule). The higher die's owner moves first, playing
// both dice.
func Opening(r Reveal, rb Response) (rollerDie, responderDie int) {
	seed := sha256.Sum256(append(append([]byte{'o'}, r.RA[:]...), rb[:]...))
	for n := 2; ; n += 2 {
		s := sample(seed, n)
		if s[n-2] != s[n-1] {
			return s[n-2], s[n-1]
		}
	}
}

// sample draws n uniform values in 1..6 from an expandable hash stream.
// A byte is accepted if < 252 (the largest multiple of 6 ≤ 256), else
// rejected; when a block runs dry the stream extends by rehashing with a
// counter. The probability of needing even one extension block is ~2^-52.
func sample(seed [32]byte, n int) []int {
	out := make([]int, 0, n)
	block := seed
	for counter := byte(0); ; counter++ {
		for _, b := range block {
			if b >= 252 {
				continue
			}
			out = append(out, int(b%6)+1)
			if len(out) == n {
				return out
			}
		}
		block = sha256.Sum256(append(block[:], counter))
	}
}
