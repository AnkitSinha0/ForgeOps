package secrets

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// Regression tests from the 2026-11-11 security audit. Red against f85ea24.

// L8: a Keyring built without ParseKeyring has no keys, and Seal dereferenced a nil
// AEAD. It must fail with an error, not take the process down.
func TestZeroKeyringFailsWithoutPanicking(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked: %v", r)
		}
	}()
	var k Keyring
	if _, err := k.Seal([]byte("x"), nil); err == nil {
		t.Fatal("Seal with no keys returned no error")
	}
	if _, err := k.Open(make([]byte, 40), nil); err == nil {
		t.Fatal("Open with no keys returned no error")
	}
}

// L8: an all-zero key (a placeholder left in .env) and one key listed under two
// versions were both accepted.
func TestParseRefusesWeakOrRepeatedKeys(t *testing.T) {
	t.Parallel()
	zero := base64.StdEncoding.EncodeToString(make([]byte, keyBytes))
	for _, spec := range []string{
		"1:" + zero,
		"1:" + keyText(7) + ",2:" + keyText(7),
	} {
		_, err := ParseKeyring(spec)
		if err == nil {
			t.Errorf("ParseKeyring accepted %q", spec)
			continue
		}
		if bytes.Contains([]byte(err.Error()), []byte(keyText(7))) {
			t.Errorf("error echoes key material: %v", err)
		}
	}
	ring(t, "1:"+keyText(7)+",2:"+keyText(8))
}
