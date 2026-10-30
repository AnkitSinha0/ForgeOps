package repositories

import (
	"context"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type DeploymentRepository interface {
	// Create inserts d with status pending. d.BuildID must be a build of
	// d.ProjectID in d.TenantID, or the result is ErrBadReference.
	Create(ctx context.Context, d *models.Deployment) error
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Deployment, error)
	// ListByProject returns the newest deployments first, at most limit of them.
	ListByProject(ctx context.Context, tenantID, projectID uuid.UUID, limit int) ([]models.Deployment, error)
	// SetStatus moves the deployment to status if its current status allows it
	// (models.DeploymentStatus.AllowedFrom), else ErrInvalidTransition. Making a
	// second deployment of one project live returns ErrConflict.
	SetStatus(ctx context.Context, tenantID, id uuid.UUID, status models.DeploymentStatus) error
}

type deploymentRepo struct{ db *gorm.DB }

var _ DeploymentRepository = (*deploymentRepo)(nil)

func NewDeploymentRepository(db *gorm.DB) DeploymentRepository { return &deploymentRepo{db: db} }

func (r *deploymentRepo) Create(ctx context.Context, d *models.Deployment) error {
	d.Status = models.DeploymentPending
	return writeErr(r.db.WithContext(ctx).Create(d).Error)
}

func (r *deploymentRepo) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Deployment, error) {
	var d models.Deployment
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&d).Error; err != nil {
		return nil, writeErr(err)
	}
	return &d, nil
}

func (r *deploymentRepo) ListByProject(ctx context.Context, tenantID, projectID uuid.UUID, limit int) ([]models.Deployment, error) {
	var ds []models.Deployment
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ?", tenantID, projectID).
		Order("created_at DESC, id").Limit(limit).Find(&ds).Error
	return ds, writeErr(err)
}

func (r *deploymentRepo) SetStatus(ctx context.Context, tenantID, id uuid.UUID, status models.DeploymentStatus) error {
	from := status.AllowedFrom()
	if len(from) == 0 {
		return ErrInvalidTransition // nothing may move to pending, or to an unknown status
	}
	res := r.db.WithContext(ctx).Model(&models.Deployment{}).
		Where("tenant_id = ? AND id = ? AND status IN ?", tenantID, id, from).
		Updates(map[string]any{"status": status, "updated_at": time.Now()})
	if res.Error != nil {
		return writeErr(res.Error) // the one-live-per-project index → ErrConflict
	}
	if res.RowsAffected == 1 {
		return nil
	}
	if _, err := r.FindByID(ctx, tenantID, id); err != nil {
		return err
	}
	return ErrInvalidTransition
}
