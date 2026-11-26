package repositories

import (
	"context"
	"strings"
	"unicode/utf8"

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
	now := r.db.NowFunc()
	return r.transition(ctx, tenantID, id, []models.BuildStatus{models.BuildQueued},
		map[string]any{"status": models.BuildBuilding, "started_at": now, "updated_at": now})
}

// MarkSucceeded refuses an empty image: "succeeded" is what makes a build deployable,
// and a deployment of nothing would fail far from the cause. Migration 0003 enforces
// the same rule in the database.
func (r *buildRepo) MarkSucceeded(ctx context.Context, tenantID, id uuid.UUID, imageRef string) error {
	if strings.TrimSpace(imageRef) == "" {
		return ErrInvalid
	}
	now := r.db.NowFunc()
	return r.transition(ctx, tenantID, id, []models.BuildStatus{models.BuildBuilding},
		map[string]any{"status": models.BuildSucceeded, "image_ref": imageRef, "finished_at": now, "updated_at": now})
}

// maxBuildErrorBytes bounds a stored failure reason. Reasons come from build output,
// which a tenant controls.
const maxBuildErrorBytes = 4096

// MarkFailed never refuses a reason: an executor reporting a failure must always be
// able to record it, or the build would sit in "building" forever. So the reason is
// cleaned instead: NUL bytes (which Postgres text can't hold) are dropped and the
// rest is cut to maxBuildErrorBytes on a UTF-8 boundary.
func (r *buildRepo) MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) error {
	now := r.db.NowFunc()
	return r.transition(ctx, tenantID, id, []models.BuildStatus{models.BuildQueued, models.BuildBuilding},
		map[string]any{"status": models.BuildFailed, "error": boundedReason(reason), "finished_at": now, "updated_at": now})
}

func boundedReason(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if len(s) <= maxBuildErrorBytes {
		return s
	}
	cut := maxBuildErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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
