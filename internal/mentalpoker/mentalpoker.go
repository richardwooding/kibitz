// Package mentalpoker implements the cryptographic core of a dealerless card
// shuffle ("mental poker"): a commutative cipher that lets two players jointly
// shuffle and deal a deck so that neither player — nor the blind relay — learns
// the deck order or the other's hand, yet every card can be revealed and
// verified at showdown.
//
// Scheme (Pohlig–Hellman / SRA over a safe prime). p is a 2048-bit safe prime
// (RFC 3526 group 14); q=(p-1)/2 is prime and the quadratic residues mod p form
// the unique subgroup of order q. Each card i is encoded as a distinct QR
// token_i. A player's secret is an exponent k in [2,q-1]; encryption is
// c = m^k mod p and decryption c^(k⁻¹ mod q) mod p. Because the subgroup has
// prime order q every k is invertible mod q, and exponentiation commutes, so
//
//	Eₐ(E_b(m)) = m^(kₐ·k_b) = E_b(Eₐ(m)).
//
// To deal a doubly-encrypted card m^(kₐk_b) to A, B strips its layer
// (Decrypt_b → m^kₐ) and A strips its own (Decrypt_a → m); B never sees m
// because it would need kₐ. Cheating is caught at showdown: both players reveal
// their exponents and anyone (including spectators) recomputes the shuffle and
// checks the fully-decrypted deck is exactly the 52 tokens — see VerifyDeck.
package mentalpoker

import (
	"crypto/rand"
	"math/big"
)

// DeckSize is a standard 52-card deck.
const DeckSize = 52

// rfc3526Group14 is the 2048-bit MODP safe prime from RFC 3526.
const rfc3526Group14 = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74" +
	"020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F1437" +
	"4FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF05" +
	"98DA48361C55D39A69163FA8FD24CF5F83655D23DCA3AD961C62F356208552BB" +
	"9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF695581718" +
	"3995497CEA956AE515D2261898FA051015728E5A8AACAA68FFFFFFFFFFFFFFFF"

var (
	p          *big.Int
	q          *big.Int // (p-1)/2, prime — the QR subgroup order
	tokens     [DeckSize]*big.Int
	tokenIndex map[string]int
	two        = big.NewInt(2)
)

func init() {
	p, _ = new(big.Int).SetString(rfc3526Group14, 16)
	q = new(big.Int).Rsh(new(big.Int).Sub(p, big.NewInt(1)), 1)
	tokenIndex = make(map[string]int, DeckSize)
	for i := 0; i < DeckSize; i++ {
		// (i+2)² mod p — a quadratic residue, distinct for these small bases.
		t := new(big.Int).Exp(big.NewInt(int64(i+2)), two, p)
		tokens[i] = t
		tokenIndex[t.String()] = i
	}
}

// Key is a player's secret exponent and its inverse mod q.
type Key struct {
	k    *big.Int
	kinv *big.Int
}

// NewKey draws a fresh random secret key.
func NewKey() (Key, error) {
	for {
		k, err := rand.Int(rand.Reader, q)
		if err != nil {
			return Key{}, err
		}
		if k.Cmp(two) < 0 {
			continue // avoid trivial 0/1
		}
		if kinv := new(big.Int).ModInverse(k, q); kinv != nil {
			return Key{k: k, kinv: kinv}, nil
		}
	}
}

// KeyFromExponent reconstructs a key from a revealed exponent (for showdown
// verification). Returns ok=false if the exponent isn't a valid unit mod q.
func KeyFromExponent(k *big.Int) (Key, bool) {
	if k == nil || k.Sign() <= 0 || k.Cmp(q) >= 0 {
		return Key{}, false
	}
	kinv := new(big.Int).ModInverse(k, q)
	if kinv == nil {
		return Key{}, false
	}
	return Key{k: new(big.Int).Set(k), kinv: kinv}, true
}

// Exponent returns the raw secret exponent, to be revealed only at showdown.
func (key Key) Exponent() *big.Int { return new(big.Int).Set(key.k) }

// Encrypt raises m to the secret exponent; Decrypt strips one layer.
func (key Key) Encrypt(m *big.Int) *big.Int { return new(big.Int).Exp(m, key.k, p) }
func (key Key) Decrypt(c *big.Int) *big.Int { return new(big.Int).Exp(c, key.kinv, p) }

// Token returns the QR encoding of card i (0..51).
func Token(i int) *big.Int { return new(big.Int).Set(tokens[i]) }

// Decode maps a fully-decrypted plaintext back to a card index, or -1.
func Decode(m *big.Int) int {
	if i, ok := tokenIndex[m.String()]; ok {
		return i
	}
	return -1
}

// Deck marshals to/from the wire as big-endian byte slices.
func Marshal(deck []*big.Int) [][]byte {
	out := make([][]byte, len(deck))
	for i, m := range deck {
		out[i] = m.Bytes()
	}
	return out
}

func Unmarshal(raw [][]byte) []*big.Int {
	out := make([]*big.Int, len(raw))
	for i, b := range raw {
		out[i] = new(big.Int).SetBytes(b)
	}
	return out
}

// EncryptAll returns a new deck with every card raised to key (order preserved).
func EncryptAll(key Key, deck []*big.Int) []*big.Int {
	out := make([]*big.Int, len(deck))
	for i, m := range deck {
		out[i] = key.Encrypt(m)
	}
	return out
}

// Shuffle permutes deck in place with a crypto/rand Fisher–Yates.
func Shuffle(deck []*big.Int) error {
	for i := len(deck) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		jj := int(j.Int64())
		deck[i], deck[jj] = deck[jj], deck[i]
	}
	return nil
}

// FreshDeck returns the 52 plaintext card tokens in card order.
func FreshDeck() []*big.Int {
	d := make([]*big.Int, DeckSize)
	for i := range d {
		d[i] = Token(i)
	}
	return d
}

// VerifyDeck checks that a doubly-encrypted deck, decrypted with both revealed
// keys, is exactly the 52 distinct card tokens — proving both players used a
// single consistent key and the deck contains no duplicated or forged cards.
func VerifyDeck(deck []*big.Int, a, b Key) bool {
	if len(deck) != DeckSize {
		return false
	}
	seen := make([]bool, DeckSize)
	for _, c := range deck {
		i := Decode(a.Decrypt(b.Decrypt(c)))
		if i < 0 || seen[i] {
			return false
		}
		seen[i] = true
	}
	return true
}
