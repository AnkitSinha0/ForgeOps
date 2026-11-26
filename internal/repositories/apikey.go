package repositories

import (
	"context"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// APIKeyRepository stores credentials. Only FindActiveByHash isn't tenant-scoped,
// because it is how a request finds out which tenant it belongs to.
type APIKeyRepository interface {
	// Create inserts k under k.TenantID.
	Create(ctx context.Context, k *models.APIKey) error
	// FindActiveByHash returns the unrevoked key with this hash, or ErrNotFound.
	FindActiveByHash(ctx context.Context, hash []byte) (*models.APIKey, error)
	// List returns the tenant's keys newest first, revoked ones included. KeyHash
	// is left empty: nothing that lists keys has a reason to hold the hashes.
	List(ctx context.Context, tenantID uuid.UUID, after *TimeCursor, limit int) ([]models.APIKey, error)
	// Revoke marks the key revoked. An already revoked key returns ErrNotFound.
	Revoke(ctx context.Context, tenantID, id uuid.UUID) error
}

type apiKeyRepo struct{ db *gorm.DB }

var _ APIKeyRepository = (*apiKeyRepo)(nil)

func NewAPIKeyRepository(db *gorm.DB) APIKeyRepository { return &apiKeyRepo{db: db} }

func (r *apiKeyRepo) Create(ctx context.Context, k *models.APIKey) error {
	return writeErr(r.db.WithContext(ctx).Create(k).Error)
}

func (r *apiKeyRepo) FindActiveByHash(ctx context.Context, hash []byte) (*models.APIKey, error) {
	var k models.APIKey
	err := r.db.WithContext(ctx).Where("key_hash = ? AND revoked_at IS NULL", hash).Take(&k).Error
	if err != nil {
		return nil, writeErr(err)
	}
	return &k, nil
}

func (r *apiKeyRepo) List(ctx context.Context, tenantID uuid.UUID, after *TimeCursor, limit int) ([]models.APIKey, error) {
	var ks []models.APIKey
	q := r.db.WithContext(ctx).Omit("key_hash").Where("tenant_id = ?", tenantID)
	err := newestFirst(q, after, limit).Find(&ks).Error
	return ks, writeErr(err)
}

func (r *apiKeyRepo) Revoke(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.db.WithContext(ctx).Model(&models.APIKey{}).
		Where("tenant_id = ? AND id = ? AND revoked_at IS NULL", tenantID, id).
		Update("revoked_at", time.Now()))
}
