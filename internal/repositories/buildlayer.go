package repositories

import (
	"context"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BuildLayerRepository manages the global, content-addressed layer table. Layers
// have no tenant, so reads are global; attaching layers to a build is checked
// against the build's tenant.
type BuildLayerRepository interface {
	// Ensure records the layer if its digest is new. created reports whether
	// this call inserted it (false = the bytes were already stored: a dedup hit).
	Ensure(ctx context.Context, l *models.BuildLayer) (created bool, err error)
	Find(ctx context.Context, digest string) (*models.BuildLayer, error)
	// AttachToBuild records that the build uses these layers, in this order.
	// A layer that was never Ensured returns ErrBadReference.
	AttachToBuild(ctx context.Context, tenantID, buildID uuid.UUID, digests []string) error
	// RefCount is the number of build positions using the layer.
	RefCount(ctx context.Context, digest string) (int64, error)
	// Unreferenced lists layers no build uses: candidates for garbage collection.
	Unreferenced(ctx context.Context, limit int) ([]models.BuildLayer, error)
	// Delete removes an unreferenced layer. A layer still in use returns ErrInUse,
	// enforced by the database, so a build attaching it at the same moment is safe.
	Delete(ctx context.Context, digest string) error
}

type buildLayerRepo struct{ db *gorm.DB }

var _ BuildLayerRepository = (*buildLayerRepo)(nil)

func NewBuildLayerRepository(db *gorm.DB) BuildLayerRepository { return &buildLayerRepo{db: db} }

func (r *buildLayerRepo) Ensure(ctx context.Context, l *models.BuildLayer) (bool, error) {
	// ON CONFLICT DO NOTHING: two tenants pushing the same layer at once both
	// succeed, and exactly one of them inserts it.
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(l)
	if res.Error != nil {
		return false, writeErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *buildLayerRepo) Find(ctx context.Context, digest string) (*models.BuildLayer, error) {
	var l models.BuildLayer
	if err := r.db.WithContext(ctx).Where("digest = ?", digest).Take(&l).Error; err != nil {
		return nil, writeErr(err)
	}
	return &l, nil
}

func (r *buildLayerRepo) AttachToBuild(ctx context.Context, tenantID, buildID uuid.UUID, digests []string) error {
	if len(digests) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// build_layer_refs has no tenant_id, so the tenant check happens here:
		// a build of another tenant looks exactly like a missing build.
		var n int64
		if err := tx.Model(&models.Build{}).Where("tenant_id = ? AND id = ?", tenantID, buildID).Count(&n).Error; err != nil {
			return writeErr(err)
		}
		if n == 0 {
			return ErrNotFound
		}
		refs := make([]models.BuildLayerRef, len(digests))
		for i, d := range digests {
			refs[i] = models.BuildLayerRef{BuildID: buildID, Position: i, LayerDigest: d}
		}
		return writeErr(tx.Create(&refs).Error)
	})
}

func (r *buildLayerRepo) RefCount(ctx context.Context, digest string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.BuildLayerRef{}).Where("layer_digest = ?", digest).Count(&n).Error
	return n, writeErr(err)
}

func (r *buildLayerRepo) Unreferenced(ctx context.Context, limit int) ([]models.BuildLayer, error) {
	var ls []models.BuildLayer
	err := r.db.WithContext(ctx).
		Where("NOT EXISTS (SELECT 1 FROM build_layer_refs r WHERE r.layer_digest = build_layers.digest)").
		Order("created_at, digest").Limit(limit).Find(&ls).Error
	return ls, writeErr(err)
}

func (r *buildLayerRepo) Delete(ctx context.Context, digest string) error {
	res := r.db.WithContext(ctx).Where("digest = ?", digest).Delete(&models.BuildLayer{})
	if res.Error != nil {
		return translate(res.Error, ErrInUse) // ON DELETE RESTRICT fired: refs still exist
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
