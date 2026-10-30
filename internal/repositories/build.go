package repositories

import (
	"context"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BuildRepository interface {
	// Create inserts b with status queued, whatever b.Status says.
	Create(ctx context.Context, b *models.Build) error
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Build, error)
	// ListByProject returns the newest builds first, at most limit of them.
	ListByProject(ctx context.Context, tenantID, projectID uuid.UUID, limit int) ([]models.Build, error)

	// The three transitions. Each is one conditional UPDATE: it only matches a
	// row whose current status allows the move, so two workers racing on the
	// same build can't both win. A loser gets ErrInvalidTransition.

	// MarkBuilding: queued → building, sets StartedAt.
	MarkBuilding(ctx context.Context, tenantID, id uuid.UUID) error
	// MarkSucceeded: building → succeeded, sets ImageRef and FinishedAt.
	MarkSucceeded(ctx context.Context, tenantID, id uuid.UUID, imageRef string) error
	// MarkFailed: queued or building → failed, sets Error and FinishedAt.
	MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) error
}

type buildRepo struct{ db *gorm.DB }

var _ BuildRepository = (*buildRepo)(nil)

func NewBuildRepository(db *gorm.DB) BuildRepository { return &buildRepo{db: db} }

func (r *buildRepo) Create(ctx context.Context, b *models.Build) error {
	b.Status = models.BuildQueued
	return writeErr(r.db.WithContext(ctx).Create(b).Error)
}

func (r *buildRepo) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Build, error) {
	var b models.Build
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&b).Error; err != nil {
		return nil, writeErr(err)
	}
	return &b, nil
}

func (r *buildRepo) ListByProject(ctx context.Context, tenantID, projectID uuid.UUID, limit int) ([]models.Build, error) {
	var bs []models.Build
	// id breaks ties between builds created in the same microsecond, so the
	// order is fully determined.
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ?", tenantID, projectID).
		Order("created_at DESC, id").Limit(limit).Find(&bs).Error
	return bs, writeErr(err)
}

func (r *buildRepo) MarkBuilding(ctx context.Context, tenantID, id uuid.UUID) error {
	now := time.Now()
	return r.transition(ctx, tenantID, id, []models.BuildStatus{models.BuildQueued},
		map[string]any{"status": models.BuildBuilding, "started_at": now, "updated_at": now})
}

func (r *buildRepo) MarkSucceeded(ctx context.Context, tenantID, id uuid.UUID, imageRef string) error {
	now := time.Now()
	return r.transition(ctx, tenantID, id, []models.BuildStatus{models.BuildBuilding},
		map[string]any{"status": models.BuildSucceeded, "image_ref": imageRef, "finished_at": now, "updated_at": now})
}

func (r *buildRepo) MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) error {
	now := time.Now()
	return r.transition(ctx, tenantID, id, []models.BuildStatus{models.BuildQueued, models.BuildBuilding},
		map[string]any{"status": models.BuildFailed, "error": reason, "finished_at": now, "updated_at": now})
}

// transition applies set only if the build is currently in one of from.
func (r *buildRepo) transition(ctx context.Context, tenantID, id uuid.UUID, from []models.BuildStatus, set map[string]any) error {
	res := r.db.WithContext(ctx).Model(&models.Build{}).
		Where("tenant_id = ? AND id = ? AND status IN ?", tenantID, id, from).
		Updates(set)
	if res.Error != nil {
		return writeErr(res.Error)
	}
	if res.RowsAffected == 1 {
		return nil
	}
	// No row matched: either the build doesn't exist (in this tenant), or it is
	// in the wrong status. A second query tells the two apart.
	if _, err := r.FindByID(ctx, tenantID, id); err != nil {
		return err
	}
	return ErrInvalidTransition
}
