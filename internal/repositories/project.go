package repositories

import (
	"context"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ProjectRepository interface {
	// Create inserts p under p.TenantID.
	Create(ctx context.Context, p *models.Project) error
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Project, error)
	FindByName(ctx context.Context, tenantID uuid.UUID, name string) (*models.Project, error)
	// List returns the tenant's projects ordered by name.
	List(ctx context.Context, tenantID uuid.UUID) ([]models.Project, error)
	// Update writes Name, RepoURL and Branch, including empty values.
	Update(ctx context.Context, p *models.Project) error
	// Delete removes the project and, by cascade, its env vars, domains, builds
	// and deployments. Stopping its containers first is the caller's job.
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

type projectRepo struct{ db *gorm.DB }

var _ ProjectRepository = (*projectRepo)(nil)

func NewProjectRepository(db *gorm.DB) ProjectRepository { return &projectRepo{db: db} }

func (r *projectRepo) Create(ctx context.Context, p *models.Project) error {
	return writeErr(r.db.WithContext(ctx).Create(p).Error)
}

func (r *projectRepo) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Project, error) {
	var p models.Project
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&p).Error
	if err != nil {
		return nil, writeErr(err)
	}
	return &p, nil
}

func (r *projectRepo) FindByName(ctx context.Context, tenantID uuid.UUID, name string) (*models.Project, error) {
	var p models.Project
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND name = ?", tenantID, name).Take(&p).Error
	if err != nil {
		return nil, writeErr(err)
	}
	return &p, nil
}

func (r *projectRepo) List(ctx context.Context, tenantID uuid.UUID) ([]models.Project, error) {
	var ps []models.Project
	// Always an explicit ORDER BY: without one, row order is whatever the
	// database's plan happens to produce, and it changes between runs.
	err := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Order("name").Find(&ps).Error
	return ps, writeErr(err)
}

func (r *projectRepo) Update(ctx context.Context, p *models.Project) error {
	// A map, not Updates(p): with a struct, GORM silently skips zero values, so
	// clearing a field would be ignored without an error.
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&models.Project{}).
		Where("tenant_id = ? AND id = ?", p.TenantID, p.ID).
		Updates(map[string]any{"name": p.Name, "repo_url": p.RepoURL, "branch": p.Branch, "updated_at": now})
	if err := affected(res); err != nil {
		return err
	}
	p.UpdatedAt = now
	return nil
}

func (r *projectRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&models.Project{}))
}
