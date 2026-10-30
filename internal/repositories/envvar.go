package repositories

import (
	"context"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EnvVarRepository interface {
	// Set creates the variable, or replaces the value of an existing one with the
	// same key. e.ID and e.CreatedAt come back as stored.
	Set(ctx context.Context, e *models.EnvVar) error
	Get(ctx context.Context, tenantID, projectID uuid.UUID, key string) (*models.EnvVar, error)
	// List returns the project's variables ordered by key.
	List(ctx context.Context, tenantID, projectID uuid.UUID) ([]models.EnvVar, error)
	Delete(ctx context.Context, tenantID, projectID uuid.UUID, key string) error
}

type envVarRepo struct{ db *gorm.DB }

var _ EnvVarRepository = (*envVarRepo)(nil)

func NewEnvVarRepository(db *gorm.DB) EnvVarRepository { return &envVarRepo{db: db} }

func (r *envVarRepo) Set(ctx context.Context, e *models.EnvVar) error {
	e.UpdatedAt = time.Now()
	// One statement (INSERT ... ON CONFLICT DO UPDATE), not "find, then insert or
	// update": two concurrent Sets of one key can't both insert. RETURNING hands
	// back the id and created_at of the row that was kept.
	err := r.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		},
		clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}},
	).Create(e).Error
	return writeErr(err)
}

func (r *envVarRepo) Get(ctx context.Context, tenantID, projectID uuid.UUID, key string) (*models.EnvVar, error) {
	var e models.EnvVar
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ? AND key = ?", tenantID, projectID, key).Take(&e).Error
	if err != nil {
		return nil, writeErr(err)
	}
	return &e, nil
}

func (r *envVarRepo) List(ctx context.Context, tenantID, projectID uuid.UUID) ([]models.EnvVar, error) {
	var es []models.EnvVar
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ?", tenantID, projectID).Order("key").Find(&es).Error
	return es, writeErr(err)
}

func (r *envVarRepo) Delete(ctx context.Context, tenantID, projectID uuid.UUID, key string) error {
	return affected(r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ? AND key = ?", tenantID, projectID, key).
		Delete(&models.EnvVar{}))
}
