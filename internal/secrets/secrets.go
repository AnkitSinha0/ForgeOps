// Package secrets encrypts values before they reach the database, with AES-256-GCM.
//
// A stored value looks like this:
//
//	version (1 byte) | nonce (12 bytes) | ciphertext + authentication tag
//
// The version says which key sealed it, so a new key can be added while old rows
// can still be read. See Keyring.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	keyBytes   = 32 // AES-256
	nonceBytes = 12 // the size GCM is designed for
)

// ErrDecrypt means a value could not be opened: wrong key, tampered bytes, or the
// value was sealed for a different row. The reasons are deliberately one error,
// because telling them apart would help an attacker probing the format.
var ErrDecrypt = errors.New("could not decrypt value")

// Keyring holds every key that can still open stored values. The highest version
// seals new values.
type Keyring struct {
	current byte
	aeads   map[byte]cipher.AEAD
}

// ParseKeyring reads "version:base64key" pairs separated by commas, for example
// "2:AAAA...,1:BBBB...". Errors never include key material.
func ParseKeyring(spec string) (*Keyring, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("SECRETS_KEYS is required; generate a key with: openssl rand -base64 32, then set SECRETS_KEYS=1:<key>")
	}
	k := &Keyring{aeads: make(map[byte]cipher.AEAD)}
	for pair := range strings.SplitSeq(spec, ",") {
		versionText, encoded, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok {
			return nil, errors.New("SECRETS_KEYS: each entry must look like version:base64key")
		}
		version, err := strconv.ParseUint(versionText, 10, 8)
		if err != nil || version == 0 {
			return nil, errors.New("SECRETS_KEYS: version must be a number from 1 to 255")
		}
		v := byte(version)
		if _, dup := k.aeads[v]; dup {
			return nil, fmt.Errorf("SECRETS_KEYS: version %d appears twice", v)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("SECRETS_KEYS: version %d is not valid base64", v)
		}
		if len(raw) != keyBytes {
			return nil, fmt.Errorf("SECRETS_KEYS: version %d must decode to %d bytes, got %d", v, keyBytes, len(raw))
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, fmt.Errorf("SECRETS_KEYS: version %d: %w", v, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("SECRETS_KEYS: version %d: %w", v, err)
		}
		k.aeads[v] = aead
		k.current = max(k.current, v)
	}
	return k, nil
}

// Seal encrypts plaintext with the newest key. boundTo is authenticated but not
// stored: Open must be given the same bytes, so a value copied to a different row
// fails to decrypt rather than handing over another row's secret.
//
// The nonce is random for every call. GCM breaks badly if a key ever reuses a
// nonce, and 12 random bytes are safe for about 2^32 values per key.
func (k *Keyring) Seal(plaintext, boundTo []byte) ([]byte, error) {
	out := make([]byte, 1+nonceBytes, 1+nonceBytes+len(plaintext)+k.aeads[k.current].Overhead())
	out[0] = k.current
	if _, err := rand.Read(out[1 : 1+nonceBytes]); err != nil {
		return nil, fmt.Errorf("reading random nonce: %w", err)
	}
	return k.aeads[k.current].Seal(out, out[1:1+nonceBytes], plaintext, authData(k.current, boundTo)), nil
}

// Open decrypts a value from Seal, using whichever key its version names.
func (k *Keyring) Open(blob, boundTo []byte) ([]byte, error) {
	if len(blob) < 1+nonceBytes {
		return nil, ErrDecrypt
	}
	aead, ok := k.aeads[blob[0]]
	if !ok {
		return nil, ErrDecrypt
	}
	plaintext, err := aead.Open(nil, blob[1:1+nonceBytes], blob[1+nonceBytes:], authData(blob[0], boundTo))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// authData covers the version byte as well, so it can't be edited to pick another key.
func authData(version byte, boundTo []byte) []byte {
	return slices.Concat([]byte{version}, boundTo)
}
