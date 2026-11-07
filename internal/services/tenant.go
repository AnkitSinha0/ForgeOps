package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AnkitSinha0/forgeops/internal/apikey"
	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/google/uuid"
)

// TenantService manages tenants. Creating one is not on the public API: only the
// `controlplane admin` CLI calls Create, so it requires access to the server.
type TenantService struct{ tenants repositories.TenantRepository }

func NewTenantService(tenants repositories.TenantRepository) *TenantService {
	return &TenantService{tenants: tenants}
}

// Create makes a tenant and its first API key, and returns the key's plaintext. This
// is the only moment that plaintext exists on the server.
func (s *TenantService) Create(ctx context.Context, name, slug string) (*models.Tenant, string, error) {
	if err := checkLabel("name", name); err != nil {
		return nil, "", err
	}
	if !slugPattern.MatchString(slug) {
		return nil, "", invalid("slug", "must be 1-40 lowercase letters, digits or inner hyphens")
	}
	gen, err := apikey.Generate()
	if err != nil {
		return nil, "", fmt.Errorf("generating key: %w", err)
	}
	t := &models.Tenant{Name: name, Slug: slug}
	k := &models.APIKey{Name: "initial", Prefix: gen.Display, KeyHash: gen.Hash}
	if err := s.tenants.CreateWithKey(ctx, t, k); err != nil {
		return nil, "", err
	}
	return t, gen.Plaintext, nil
}

func (s *TenantService) Get(ctx context.Context, id uuid.UUID) (*models.Tenant, error) {
	return s.tenants.FindByID(ctx, id)
}

func (s *TenantService) FindBySlug(ctx context.Context, slug string) (*models.Tenant, error) {
	return s.tenants.FindBySlug(ctx, slug)
}

// APIKeyService issues and checks credentials.
type APIKeyService struct{ keys repositories.APIKeyRepository }

func NewAPIKeyService(keys repositories.APIKeyRepository) *APIKeyService {
	return &APIKeyService{keys: keys}
}

// Authenticate returns the tenant a presented key acts as, or ErrUnauthenticated.
// It looks the key up on every request, so a revoke applies to the very next one.
// Any other error means the check itself failed (the database is down, say), and must
// not be reported to the caller as a bad key.
func (s *APIKeyService) Authenticate(ctx context.Context, presented string) (uuid.UUID, error) {
	hash, ok := apikey.Hash(presented)
	if !ok {
		return uuid.Nil, ErrUnauthenticated
	}
	k, err := s.keys.FindActiveByHash(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		return uuid.Nil, ErrUnauthenticated
	}
	if err != nil {
		return uuid.Nil, err
	}
	return k.TenantID, nil
}

// Create issues a new key for the tenant and returns its plaintext once.
func (s *APIKeyService) Create(ctx context.Context, tenantID uuid.UUID, name string) (*models.APIKey, string, error) {
	if err := checkLabel("name", name); err != nil {
		return nil, "", err
	}
	gen, err := apikey.Generate()
	if err != nil {
		return nil, "", fmt.Errorf("generating key: %w", err)
	}
	k := &models.APIKey{TenantID: tenantID, Name: name, Prefix: gen.Display, KeyHash: gen.Hash}
	if err := s.keys.Create(ctx, k); err != nil {
		return nil, "", err
	}
	return k, gen.Plaintext, nil
}

func (s *APIKeyService) List(ctx context.Context, tenantID uuid.UUID, limit int, cursor string) (Page[models.APIKey], error) {
	if err := checkLimit(limit); err != nil {
		return Page[models.APIKey]{}, err
	}
	after, err := decodeTimeCursor(cursor)
	if err != nil {
		return Page[models.APIKey]{}, err
	}
	rows, err := s.keys.List(ctx, tenantID, after, limit+1)
	if err != nil {
		return Page[models.APIKey]{}, err
	}
	return pageOf(rows, limit, func(k models.APIKey) string { return encodeTimeCursor(k.CreatedAt, k.ID) }), nil
}

func (s *APIKeyService) Revoke(ctx context.Context, tenantID, id uuid.UUID) error {
	return s.keys.Revoke(ctx, tenantID, id)
}

// pageOf cuts a result fetched with limit+1 rows. The extra row is how we know
// another page exists, without running a COUNT.
func pageOf[T any](rows []T, limit int, cursor func(T) string) Page[T] {
	if len(rows) <= limit {
		return Page[T]{Items: rows}
	}
	rows = rows[:limit]
	return Page[T]{Items: rows, NextCursor: cursor(rows[limit-1])}
}

// checkLabel validates free text shown to people, such as tenant and key names.
// Control characters are refused because Postgres can't store a NUL byte in text, and
// the failure would otherwise surface as a 500.
func checkLabel(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalid(field, "is required")
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 100 {
		return invalid(field, "must be valid text of at most 100 characters")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return invalid(field, "must not contain control characters")
	}
	return nil
}
