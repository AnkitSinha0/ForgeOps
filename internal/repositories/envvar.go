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
	// SetWithin is Set, refused with ErrLimitReached if afterwards the project would
	// hold more than lim.MaxVars variables or more than lim.MaxBytes of stored values.
	// Replacing a variable counts its new size, not old plus new. A project that is
	// not the tenant's returns ErrNotFound.
	SetWithin(ctx context.Context, e *models.EnvVar, lim EnvVarLimits) error
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

// EnvVarLimits bounds one project's variables. MaxBytes counts what is stored, so
// with encryption that is each value plus 29 bytes (version, nonce and tag).
type EnvVarLimits struct {
	MaxVars  int
	MaxBytes int64
}

func (r *envVarRepo) Set(ctx context.Context, e *models.EnvVar) error {
	return upsertEnvVar(r.db.WithContext(ctx), e)
}

func (r *envVarRepo) SetWithin(ctx context.Context, e *models.EnvVar, lim EnvVarLimits) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Locking the project row makes Sets on one project take turns. Without it,
		// two concurrent Sets could each count 199 variables and both insert. Other
		// projects aren't blocked. The lock is tenant-scoped like every other read,
		// so another tenant's project is simply not found.
		var locked []uuid.UUID
		if err := tx.Raw("SELECT id FROM projects WHERE tenant_id = ? AND id = ? FOR UPDATE",
			e.TenantID, e.ProjectID).Scan(&locked).Error; err != nil {
			return writeErr(err)
		}
		if len(locked) == 0 {
			return ErrNotFound
		}
		var usage struct {
			Vars  int
			Bytes int64
		}
		// Every other variable of the project: the one being set is replaced, so only
		// its new size counts.
		if err := tx.Raw(`SELECT count(*) AS vars, coalesce(sum(octet_length(value)), 0) AS bytes
			FROM env_vars WHERE tenant_id = ? AND project_id = ? AND key <> ?`,
			e.TenantID, e.ProjectID, e.Key).Scan(&usage).Error; err != nil {
			return writeErr(err)
		}
		if usage.Vars+1 > lim.MaxVars || usage.Bytes+int64(len(e.Value)) > lim.MaxBytes {
			return ErrLimitReached
		}
		return upsertEnvVar(tx, e)
	})
}

func upsertEnvVar(db *gorm.DB, e *models.EnvVar) error {
	// One clock reading for both, so a new row can't have updated_at before created_at.
	// On a conflict the existing created_at is kept, and RETURNING hands it back.
	now := db.NowFunc()
	e.CreatedAt, e.UpdatedAt = now, now
	// One statement (INSERT ... ON CONFLICT DO UPDATE), not "find, then insert or
	// update": two concurrent Sets of one key can't both insert. RETURNING hands
	// back the id and created_at of the row that was kept.
	//
	// The WHERE is a tenant check the foreign key can't do. On a conflict Postgres
	// updates the existing row, tenant_id doesn't change, and the composite foreign
	// key never runs, so without this any tenant could overwrite another tenant's
	// variable by naming its project ID and key.
	res := db.Clauses(
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
