// Package apikey generates and checks the format of ForgeOps API keys.
//
// A key is "fo_" followed by 64 hex characters (32 random bytes). The fixed prefix
// lets secret scanners and people recognise one, and lets the API throw out random
// strings without a database query.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

const (
	marker     = "fo_"
	randomLen  = 32
	displayLen = len(marker) + 8
)

// Generated is a new key. Plaintext is shown to the caller once and never stored.
type Generated struct {
	Plaintext string
	Hash      []byte
	Display   string // the first characters, safe to store and show
}

// Generate creates a new random key.
func Generate() (Generated, error) {
	buf := make([]byte, randomLen)
	if _, err := rand.Read(buf); err != nil {
		return Generated{}, err
	}
	plaintext := marker + hex.EncodeToString(buf)
	return Generated{Plaintext: plaintext, Hash: hash(plaintext), Display: plaintext[:displayLen]}, nil
}

// Hash returns the stored form of a presented key, and false if the string can't be
// a key at all.
//
// Plain SHA-256 is right here, unlike for passwords. bcrypt and argon2 exist to slow
// down guessing of low-entropy human choices, while 256 random bits can't be guessed
// at any speed. A fast hash also keeps the per-request cost down, and because it is
// deterministic the database can look the key up directly by its hash. An attacker
// can't steer a guess byte by byte through a SHA-256 output, so the comparison the
// index makes doesn't leak anything useful through timing.
func Hash(presented string) ([]byte, bool) {
	if !valid(presented) {
		return nil, false
	}
	return hash(presented), true
}

func hash(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func valid(s string) bool {
	if len(s) != len(marker)+randomLen*2 || s[:len(marker)] != marker {
		return false
	}
	for _, c := range []byte(s[len(marker):]) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
