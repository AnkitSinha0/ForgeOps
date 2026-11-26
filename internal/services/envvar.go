package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/AnkitSinha0/forgeops/internal/secrets"
	"github.com/google/uuid"
)

const maxEnvValueBytes = 64 << 10 // 64 KiB

// envLimits bound one project's variables, sized against where they end up: the
// environment of a process started with execve, whose arguments and environment
// together are capped (2 MiB on a default Linux stack, less with the args added).
// 512 KiB leaves the platform and the app's own arguments plenty of room.
var envLimits = repositories.EnvVarLimits{MaxVars: 200, MaxBytes: 512 << 10}

// envKeyPattern matches the CHECK on env_vars.key in 0001_init.sql, plus a length limit.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)

// Names a tenant may not set. They are compared in uppercase: Linux treats PATH and
// path as two variables, but Windows containers and many tools don't.
//   - The loader's own variables (LD_*, DYLD_*): they decide what code runs before main.
//   - What the platform sets for every app (PORT from Phase 5, FORGEOPS_*): a tenant
//     value would either be overwritten silently or, if the executor ever applied
//     tenant values last, override the platform.
//   - PATH, HOME, HOSTNAME: set by the image and the runtime; overriding them breaks
//     the container in ways that look like platform bugs.
//
// This guards the tenant's own container from surprises and keeps the platform's
// names the platform's. It is not the isolation boundary: Phase 6's sandbox is.
var (
	reservedEnvNames    = []string{"PATH", "HOME", "HOSTNAME", "PORT"}
	reservedEnvPrefixes = []string{"LD_", "DYLD_", "FORGEOPS_"}
)

func reservedEnvName(key string) bool {
	upper := strings.ToUpper(key)
	if slices.Contains(reservedEnvNames, upper) {
		return true
	}
	for _, p := range reservedEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// EnvVarService stores environment variables encrypted. Values go in through Set and
// are never returned by the API. The only way out is Decrypted, for the executor.
type EnvVarService struct {
	projects repositories.ProjectRepository
	envVars  repositories.EnvVarRepository
	keys     *secrets.Keyring
}

func NewEnvVarService(projects repositories.ProjectRepository, envVars repositories.EnvVarRepository, keys *secrets.Keyring) *EnvVarService {
	return &EnvVarService{projects: projects, envVars: envVars, keys: keys}
}

// boundTo ties a ciphertext to its row: tenant + project + key. Copying one row's
// value into another row, with database write access, makes it fail to decrypt rather
// than injecting one project's secret into another. The two IDs have a fixed length,
// so these bytes can't be read two ways.
func boundTo(tenantID, projectID uuid.UUID, key string) []byte {
	return slices.Concat(tenantID[:], projectID[:], []byte(key))
}

// Set creates or replaces a variable. It returns ErrNotFound if the project isn't the
// tenant's, whether or not the key already exists in it.
func (s *EnvVarService) Set(ctx context.Context, tenantID, projectID uuid.UUID, key, value string) (*models.EnvVar, error) {
	if !envKeyPattern.MatchString(key) {
		return nil, invalid("key", "must be letters, digits and underscores, up to 256 characters, not starting with a digit")
	}
	if reservedEnvName(key) {
		return nil, invalid("key", "is reserved for the platform (PATH, HOME, HOSTNAME, PORT, LD_*, DYLD_*, FORGEOPS_*)")
	}
	if len(value) > maxEnvValueBytes {
		return nil, invalid("value", fmt.Sprintf("must be at most %d bytes", maxEnvValueBytes))
	}
	// execve takes C strings, so a NUL would silently cut the value short. Newlines are
	// fine (certificates, keys): the executor passes values as a list, never through a
	// shell or an env-file, where a newline would start a new variable.
	if strings.ContainsRune(value, 0) {
		return nil, invalid("value", "must not contain a NUL byte")
	}
	sealed, err := s.keys.Seal([]byte(value), boundTo(tenantID, projectID, key))
	if err != nil {
		return nil, fmt.Errorf("encrypting value: %w", err)
	}
	e := &models.EnvVar{TenantID: tenantID, ProjectID: projectID, Key: key, Value: sealed}
	if err := s.envVars.SetWithin(ctx, e, envLimits); err != nil {
		switch {
		case errors.Is(err, repositories.ErrBadReference):
			return nil, ErrNotFound
		case errors.Is(err, repositories.ErrLimitReached):
			return nil, invalid("key", fmt.Sprintf("a project holds at most %d variables and %d KiB of values in total",
				envLimits.MaxVars, envLimits.MaxBytes>>10))
		}
		return nil, err
	}
	e.Value = nil
	return e, nil
}

// List returns keys and timestamps, never values.
func (s *EnvVarService) List(ctx context.Context, tenantID, projectID uuid.UUID, limit int, cursor string) (Page[models.EnvVar], error) {
	if err := checkLimit(limit); err != nil {
		return Page[models.EnvVar]{}, err
	}
	after, err := decodeNameCursor(cursor, envKeyPattern)
	if err != nil {
		return Page[models.EnvVar]{}, err
	}
	if _, err := s.projects.FindByID(ctx, tenantID, projectID); err != nil {
		return Page[models.EnvVar]{}, err
	}
	rows, err := s.envVars.ListKeys(ctx, tenantID, projectID, after, limit+1)
	if err != nil {
		return Page[models.EnvVar]{}, err
	}
	return pageOf(rows, limit, func(e models.EnvVar) string { return encodeCursor(e.Key) }), nil
}

// Delete removes a variable. A key that can't exist is simply not found.
func (s *EnvVarService) Delete(ctx context.Context, tenantID, projectID uuid.UUID, key string) error {
	if !envKeyPattern.MatchString(key) {
		return ErrNotFound
	}
	return s.envVars.Delete(ctx, tenantID, projectID, key)
}

// Decrypted returns a project's variables in plaintext for injecting into a build or
// container (Phases 4-5). No HTTP route calls this. It fails on the first value that
// won't decrypt: running with a variable silently missing is worse than not running.
func (s *EnvVarService) Decrypted(ctx context.Context, tenantID, projectID uuid.UUID) (map[string]string, error) {
	rows, err := s.envVars.List(ctx, tenantID, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, e := range rows {
		plain, err := s.keys.Open(e.Value, boundTo(tenantID, projectID, e.Key))
		if err != nil {
			return nil, fmt.Errorf("env var %s: %w", e.Key, err)
		}
		out[e.Key] = string(plain)
	}
	return out, nil
}
