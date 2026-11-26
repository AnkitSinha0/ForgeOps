// Package hostnames decides whether a string is a public DNS hostname, and gives its
// one canonical spelling.
//
// Two places need this: custom domains (a hostname is a globally unique claim, so
// two spellings of one host must not both be claimable) and repo URLs (a host that is
// visibly internal, like 127.0.0.1 or localhost, is refused before anything clones).
package hostnames

import (
	"errors"
	"strings"
)

const (
	maxName  = 253
	maxLabel = 63
)

// ErrInvalid is the only error Canonical returns. It never quotes the input.
var ErrInvalid = errors.New("not a valid public hostname")

// Canonical returns raw in lowercase if it is a public DNS hostname: LDH labels
// (letters, digits, hyphens, no hyphen at either end), 1-63 bytes each, at least two
// labels, 253 bytes in all, a top-level label that isn't all digits, no trailing dot.
//
// What it refuses, and why:
//   - Non-ASCII. International names must arrive as punycode ("xn--..."). Lowercasing
//     Unicode is not one well-defined operation: Go folds U+212A (Kelvin) to "k" and
//     U+0130 to "i", so "K.com" would collide with, or impersonate, "k.com".
//   - A trailing dot. "example.com." is the same host as "example.com" to DNS, but a
//     different string to a unique index, so both could be claimed.
//   - Anything with whitespace or other bytes outside LDH, including "*" wildcards.
//   - IPv4 literals and single-label names ("localhost", "gitlab"): neither is a
//     public name. No real top-level domain is all digits, while every IPv4 literal
//     ends in one, including the odd spellings resolvers accept ("0x7f.1", "127.1").
//   - "localhost" and anything under ".localhost", which resolve to loopback (RFC 6761).
//
// IPv6 literals never get here as hostnames: "[::1]" contains brackets and colons.
func Canonical(raw string) (string, error) {
	if raw == "" || len(raw) > maxName {
		return "", ErrInvalid
	}
	for i := range len(raw) {
		if raw[i] >= 0x80 {
			return "", ErrInvalid
		}
	}
	host := strings.ToLower(raw) // ASCII only by now, so this is unambiguous
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", ErrInvalid
	}
	for _, l := range labels {
		if !validLabel(l) {
			return "", ErrInvalid
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return "", ErrInvalid
	}
	if labels[len(labels)-1] == "localhost" {
		return "", ErrInvalid
	}
	return host, nil
}

func validLabel(l string) bool {
	if l == "" || len(l) > maxLabel || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := range len(l) {
		c := l[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
