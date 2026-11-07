// Package services holds the rules between HTTP and the database: input validation,
// encryption, key handling and pagination. It knows nothing about HTTP. Handlers turn
// the errors below into status codes.
package services

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/google/uuid"
)

var (
	// ErrNotFound: no such row, or it belongs to another tenant. Callers can't tell
	// which, on purpose.
	ErrNotFound = repositories.ErrNotFound
	// ErrConflict: a unique rule would be broken, such as a duplicate project name.
	ErrConflict = repositories.ErrConflict
	// ErrInvalid: the database refused a value that validation here let through.
	ErrInvalid = repositories.ErrInvalid
	// ErrUnauthenticated: the credential is malformed, unknown or revoked. One error
	// for all three, so a caller learns nothing about which keys exist.
	ErrUnauthenticated = errors.New("invalid credential")
)

// ValidationError is a bad input the caller can fix. Message is written for the
// caller, so it must never contain a submitted value: that value could be a secret.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, message string) error { return &ValidationError{Field: field, Message: message} }

// slugPattern matches the CHECK on project names and tenant slugs in 0001_init.sql.
// The database stays the last line of defence; this is what gives a useful message.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Page is one page of a list. NextCursor is empty on the last page.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

const maxLimit = 100

func checkLimit(limit int) error {
	if limit < 1 || limit > maxLimit {
		return invalid("limit", fmt.Sprintf("must be between 1 and %d", maxLimit))
	}
	return nil
}

// A cursor is opaque to clients so its format can change. It isn't signed because it
// grants nothing: it only says where to start inside a query already scoped to the
// caller's tenant, so a forged one can only move around the caller's own rows.
// Decoding is still strict, because its contents reach SQL as parameters and a value
// Postgres refuses (a NUL byte, an impossible date) would become a 500.

func encodeCursor(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func badCursor() error { return invalid("cursor", "is not a valid cursor") }

func decodeCursor(c string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", badCursor()
	}
	return string(raw), nil
}

// decodeNameCursor reads a cursor holding a value that must match pattern. An empty
// cursor means the first page.
func decodeNameCursor(c string, pattern *regexp.Regexp) (string, error) {
	if c == "" {
		return "", nil
	}
	s, err := decodeCursor(c)
	if err != nil {
		return "", err
	}
	if !pattern.MatchString(s) {
		return "", badCursor()
	}
	return s, nil
}

// maxCursorMicros is the end of year 9999, far beyond any real row and well inside
// what Postgres can compare.
const maxCursorMicros = 253402300799999999

func encodeTimeCursor(createdAt time.Time, id uuid.UUID) string {
	return encodeCursor(strconv.FormatInt(createdAt.UnixMicro(), 10) + "." + id.String())
}

func decodeTimeCursor(c string) (*repositories.TimeCursor, error) {
	if c == "" {
		return nil, nil
	}
	s, err := decodeCursor(c)
	if err != nil {
		return nil, err
	}
	microsText, idText, ok := strings.Cut(s, ".")
	if !ok {
		return nil, badCursor()
	}
	micros, err := strconv.ParseInt(microsText, 10, 64)
	if err != nil || micros < 0 || micros > maxCursorMicros {
		return nil, badCursor()
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		return nil, badCursor()
	}
	return &repositories.TimeCursor{CreatedAt: time.UnixMicro(micros), ID: id}, nil
}
