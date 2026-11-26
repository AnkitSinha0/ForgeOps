package repositories

import (
	"context"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TenantRepository is the one repository not scoped by tenant: it manages the
// tenants themselves. Phase 2's auth decides who may call it.
type TenantRepository interface {
	Create(ctx context.Context, t *models.Tenant) error
	// CreateWithKey inserts the tenant and its first API key in one transaction, so
	// a failure can't leave a tenant nobody can log in to. The body only touches
	// the database, so running it again after a retry is safe (Phase 11b).
	CreateWithKey(ctx context.Context, t *models.Tenant, k *models.APIKey) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Tenant, error)
	FindBySlug(ctx context.Context, slug string) (*models.Tenant, error)
	// Delete removes the tenant and, by cascade, everything it owns.
	Delete(ctx context.Context, id uuid.UUID) error
}

type tenantRepo struct{ db *gorm.DB }

var _ TenantRepository = (*tenantRepo)(nil)

func NewTenantRepository(db *gorm.DB) TenantRepository { return &tenantRepo{db: db} }

func (r *tenantRepo) Create(ctx context.Context, t *models.Tenant) error {
	return writeErr(r.db.WithContext(ctx).Create(t).Error)
}

func (r *tenantRepo) CreateWithKey(ctx context.Context, t *models.Tenant, k *models.APIKey) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(t).Error; err != nil {
			return writeErr(err)
		}
		k.TenantID = t.ID
		return writeErr(tx.Create(k).Error)
	})
}

func (r *tenantRepo) FindByID(ctx context.Context, id uuid.UUID) (*models.Tenant, error) {
	var t models.Tenant
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&t).Error; err != nil {
		return nil, writeErr(err)
	}
	return &t, nil
}

func (r *tenantRepo) FindBySlug(ctx context.Context, slug string) (*models.Tenant, error) {
	var t models.Tenant
	if err := r.db.WithContext(ctx).Where("slug = ?", slug).Take(&t).Error; err != nil {
		return nil, writeErr(err)
	}
	return &t, nil
}

func (r *tenantRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return affected(r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.Tenant{}))
}
