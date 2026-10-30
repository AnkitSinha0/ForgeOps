// Package repositories is the only package that talks to Postgres. Each file holds
// one interface and its GORM implementation.
//
// Two rules hold for every method:
//   - Tenant-owned data is always read and written with a tenantID in the WHERE
//     clause. The tenantID is a parameter, so a caller can't forget it.
//   - Callers never see GORM or Postgres errors for expected outcomes, only the
//     sentinels below. Anything else (connection lost, bad SQL) passes through
//     wrapped, because it really is unexpected.
package repositories

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	// ErrNotFound: no such row, or it belongs to another tenant. The two are
	// deliberately the same error: "exists but not yours" must not be detectable.
	ErrNotFound = errors.New("record not found")
	// ErrConflict: a unique rule would be broken (duplicate name, hostname, key,
	// a second live deployment for one project).
	ErrConflict = errors.New("record conflicts with an existing one")
	// ErrBadReference: a parent the row points at doesn't exist in this tenant.
	ErrBadReference = errors.New("referenced record does not exist")
	// ErrInvalid: a value breaks a CHECK constraint (bad slug, SHA, digest, ...).
	ErrInvalid = errors.New("record has an invalid value")
	// ErrInvalidTransition: the row exists, but its current status can't move to
	// the requested one (e.g. a failed build can't become succeeded).
	ErrInvalidTransition = errors.New("invalid status transition")
	// ErrInUse: the row can't be deleted while other rows still reference it.
	ErrInUse = errors.New("record is still referenced")
)

// Postgres SQLSTATE codes we translate. https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
)

// translate maps a database error to a sentinel where one applies. fkErr is the
// sentinel for a foreign-key violation, which means different things on insert
// (the parent is missing → ErrBadReference) and on delete (children still point
// here → ErrInUse).
func translate(err error, fkErr error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return ErrConflict
		case pgForeignKeyViolation:
			return fkErr
		case pgCheckViolation:
			return ErrInvalid
		}
	}
	return err
}

// writeErr is translate for inserts and updates.
func writeErr(err error) error { return translate(err, ErrBadReference) }

// affected turns "the UPDATE/DELETE matched no row" into ErrNotFound.
func affected(res *gorm.DB) error {
	if res.Error != nil {
		return writeErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
