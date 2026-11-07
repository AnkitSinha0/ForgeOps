package repositories

import (
	"context"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EnvVarRepository interface {
	// Set creates the variable, or replaces the value of an existing one with the
	// same key. e.ID and e.CreatedAt come back as stored. If the project belongs to
	// another tenant, it returns ErrBadReference when the key is new and ErrNotFound
	// when the key already exists, and changes nothing either way.
	Set(ctx context.Context, e *models.EnvVar) error
	Get(ctx context.Context, tenantID, projectID uuid.UUID, key string) (*models.EnvVar, error)
	// List returns all of the project's variables with their values, ordered by key.
	List(ctx context.Context, tenantID, projectID uuid.UUID) ([]models.EnvVar, error)
	// ListKeys returns one page ordered by key, starting after afterKey ("" starts
	// from the beginning). Value is left empty: ciphertext stays in the database
	// unless something actually needs to decrypt it.
	ListKeys(ctx context.Context, tenantID, projectID uuid.UUID, afterKey string, limit int) ([]models.EnvVar, error)
	Delete(ctx context.Context, tenantID, projectID uuid.UUID, key string) error
}

type envVarRepo struct{ db *gorm.DB }

var _ EnvVarRepository = (*envVarRepo)(nil)

func NewEnvVarRepository(db *gorm.DB) EnvVarRepository { return &envVarRepo{db: db} }

func (r *envVarRepo) Set(ctx context.Context, e *models.EnvVar) error {
	// One clock reading for both, so a new row can't have updated_at before created_at.
	// On a conflict the existing created_at is kept, and RETURNING hands it back.
	now := r.db.NowFunc()
	e.CreatedAt, e.UpdatedAt = now, now
	// One statement (INSERT ... ON CONFLICT DO UPDATE), not "find, then insert or
	// update": two concurrent Sets of one key can't both insert. RETURNING hands
	// back the id and created_at of the row that was kept.
	//
	// The WHERE is a tenant check the foreign key can't do. On a conflict Postgres
	// updates the existing row, tenant_id doesn't change, and the composite foreign
	// key never runs, so without this any tenant could overwrite another tenant's
	// variable by naming its project ID and key.
	res := r.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
			Where: clause.Where{Exprs: []clause.Expression{
				clause.Expr{SQL: "env_vars.tenant_id = EXCLUDED.tenant_id"},
			}},
		},
		clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}},
	).Create(e)
	return affected(res)
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

func (r *envVarRepo) ListKeys(ctx context.Context, tenantID, projectID uuid.UUID, afterKey string, limit int) ([]models.EnvVar, error) {
	var es []models.EnvVar
	q := r.db.WithContext(ctx).Omit("value").Where("tenant_id = ? AND project_id = ?", tenantID, projectID)
	if afterKey != "" {
		q = q.Where("key > ?", afterKey)
	}
	err := q.Order("key").Limit(limit).Find(&es).Error
	return es, writeErr(err)
}

func (r *envVarRepo) Delete(ctx context.Context, tenantID, projectID uuid.UUID, key string) error {
	return affected(r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id = ? AND key = ?", tenantID, projectID, key).
		Delete(&models.EnvVar{}))
}
