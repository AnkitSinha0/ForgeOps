package repositories

import (
	"context"
	"strings"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type DomainRepository interface {
	// Create lowercases d.Hostname before inserting. A hostname taken by any
	// tenant returns ErrConflict.
	Create(ctx context.Context, d *models.Domain) error
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Domain, error)
	ListByProject(ctx context.Context, tenantID, projectID uuid.UUID) ([]models.Domain, error)
	MarkVerified(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

type domainRepo struct{ db *gorm.DB }

var _ DomainRepository = (*domainRepo)(nil)

func NewDomainRepository(db *gorm.DB) DomainRepository { return &domainRepo{db: db} }

func (r *domainRepo) Create(ctx context.Context, d *models.Domain) error {
	// DNS names are case-insensitive: "App.Acme.com" and "app.acme.com" are one
	// host, so they must collide on the unique index.
	d.Hostname = strings.ToLower(d.Hostname)
	return writeErr(r.db.WithContext(ctx).Create(d).Error)
}

func (r *domainRepo) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*models.Domain, error) {
	var d models.Domain
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&d).Error; err != nil {
		return nil, writeErr(err)
	}
	return &d, nil
}

func (r *domainRepo) ListByProject(ctx context.Context, tenantID, projectID uuid.UUID) ([]models.Domain, error) {
	var ds []models.Domain
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ?", tenantID, projectID).Order("hostname").Find(&ds).Error
	return ds, writeErr(err)
}

func (r *domainRepo) MarkVerified(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error {
	return affected(r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("tenant_id = ? AND id = ?", tenantID, id).Update("verified_at", at))
}

func (r *domainRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&models.Domain{}))
}
