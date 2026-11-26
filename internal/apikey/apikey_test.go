package apikey

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate()
	if a.Plaintext == b.Plaintext {
		t.Fatal("two generated keys are identical")
	}
	if !strings.HasPrefix(a.Plaintext, "fo_") || len(a.Plaintext) != 67 {
		t.Fatalf("unexpected key shape, length %d", len(a.Plaintext))
	}
	if !strings.HasPrefix(a.Plaintext, a.Display) || len(a.Display) != 11 {
		t.Fatalf("Display %q is not the key's start", a.Display)
	}
	if bytes.Contains(a.Hash, []byte(a.Plaintext)) || len(a.Hash) != 32 {
		t.Fatalf("hash has the wrong shape, length %d", len(a.Hash))
	}
	got, ok := Hash(a.Plaintext)
	if !ok || !bytes.Equal(got, a.Hash) {
		t.Fatal("Hash of the generated key doesn't match its stored hash")
	}
}

func TestHashRejectsWhatCannotBeAKey(t *testing.T) {
	good := "fo_" + strings.Repeat("a", 64)
	if _, ok := Hash(good); !ok {
		t.Fatal("a well-formed key was rejected")
	}
	for _, bad := range []string{
		"", "fo_", "fo_abc", good + "a", good[:66],
		"xx_" + strings.Repeat("a", 64),
		"fo_" + strings.Repeat("A", 64),
		"fo_" + strings.Repeat("g", 64),
		"fo_" + strings.Repeat("a", 63) + "\x00",
	} {
		if _, ok := Hash(bad); ok {
			t.Fatalf("Hash accepted %q", bad)
		}
	}
}
