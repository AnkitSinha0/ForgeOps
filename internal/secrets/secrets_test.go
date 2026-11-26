package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func keyText(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, keyBytes))
}

func ring(t *testing.T, spec string) *Keyring {
	t.Helper()
	k, err := ParseKeyring(spec)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	return k
}

func TestRoundTripIncludingEmptyValue(t *testing.T) {
	k := ring(t, "1:"+keyText(1))
	for _, plain := range []string{"", "hunter2", strings.Repeat("x", 70_000)} {
		blob, err := k.Seal([]byte(plain), []byte("row"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.Open(blob, []byte("row"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != plain {
			t.Fatalf("round trip changed the value (%d bytes in, %d out)", len(plain), len(got))
		}
	}
}

func TestCiphertextHidesPlaintextAndNeverRepeats(t *testing.T) {
	k := ring(t, "1:"+keyText(1))
	first, _ := k.Seal([]byte("hunter2"), []byte("row"))
	second, _ := k.Seal([]byte("hunter2"), []byte("row"))
	if bytes.Contains(first, []byte("hunter2")) {
		t.Fatal("ciphertext contains the plaintext")
	}
	if bytes.Equal(first, second) || bytes.Equal(first[1:1+nonceBytes], second[1:1+nonceBytes]) {
		t.Fatal("sealing twice must use a new nonce and give different bytes")
	}
}

func TestOpenRefusesAnythingAltered(t *testing.T) {
	k := ring(t, "1:"+keyText(1))
	blob, _ := k.Seal([]byte("hunter2"), []byte("row-a"))

	cases := map[string]func() ([]byte, error){
		"another row": func() ([]byte, error) { return k.Open(blob, []byte("row-b")) },
		"wrong key":   func() ([]byte, error) { return ring(t, "1:"+keyText(2)).Open(blob, []byte("row-a")) },
		"empty":       func() ([]byte, error) { return k.Open(nil, []byte("row-a")) },
		"truncated":   func() ([]byte, error) { return k.Open(blob[:5], []byte("row-a")) },
		"unknown version": func() ([]byte, error) {
			c := bytes.Clone(blob)
			c[0] = 9
			return k.Open(c, []byte("row-a"))
		},
		"flipped bit": func() ([]byte, error) {
			c := bytes.Clone(blob)
			c[len(c)-1] ^= 1
			return k.Open(c, []byte("row-a"))
		},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := open(); !errors.Is(err, ErrDecrypt) {
				t.Fatalf("got %v, want ErrDecrypt", err)
			}
		})
	}
}

// Rotation: add a higher version. New values use it, and old rows stay readable.
func TestRotationKeepsOldValuesReadable(t *testing.T) {
	old := ring(t, "1:"+keyText(1))
	blob, _ := old.Seal([]byte("before"), []byte("row"))

	rotated := ring(t, "1:"+keyText(1)+", 2:"+keyText(2))
	got, err := rotated.Open(blob, []byte("row"))
	if err != nil || string(got) != "before" {
		t.Fatalf("old value after rotation: %q, %v", got, err)
	}
	fresh, _ := rotated.Seal([]byte("after"), []byte("row"))
	if fresh[0] != 2 {
		t.Fatalf("new values must use the newest key, used version %d", fresh[0])
	}
	if _, err := old.Open(fresh, []byte("row")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("the old key must not open a new value, got %v", err)
	}
}

func TestParseRejectsBadConfigWithoutEchoingKeys(t *testing.T) {
	secret := "c2VjcmV0LWtleS1tYXRlcmlhbA"
	for _, spec := range []string{
		"", "   ", "nocolon", "0:" + keyText(1), "256:" + keyText(1), "x:" + keyText(1),
		"1:not-base64!" + secret, "1:" + base64.StdEncoding.EncodeToString([]byte("too short")),
		"1:" + keyText(1) + ",1:" + keyText(2),
	} {
		_, err := ParseKeyring(spec)
		if err == nil {
			t.Fatalf("ParseKeyring(%q) succeeded, want an error", spec)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the error leaked key material: %v", err)
		}
	}
}
