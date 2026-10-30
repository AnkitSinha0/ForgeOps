// Package models holds the rows ForgeOps stores in Postgres. The schema itself is
// defined by the SQL in internal/migrations, not by these structs: GORM only maps
// columns to fields here, it never creates or alters tables.
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ensureID gives a row a random (v4) UUID unless the caller already set one.
// Generated in Go rather than by a column default, so the struct holds its ID
// straight after Create on any database, with no RETURNING round trip.
func ensureID(id *uuid.UUID) {
	if *id == uuid.Nil {
		*id = uuid.New()
	}
}

// Tenant is an account: everything else belongs to exactly one tenant.
type Tenant struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name      string
	Slug      string // DNS-label safe; becomes part of hostnames
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (t *Tenant) BeforeCreate(*gorm.DB) error { ensureID(&t.ID); return nil }

// Project is one connected Git repository.
type Project struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	TenantID  uuid.UUID `gorm:"type:uuid"`
	Name      string    // unique within the tenant
	RepoURL   string
	Branch    string // the branch whose pushes deploy
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (p *Project) BeforeCreate(*gorm.DB) error { ensureID(&p.ID); return nil }

// EnvVar is one environment variable injected into a project's builds and containers.
type EnvVar struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	TenantID  uuid.UUID `gorm:"type:uuid"`
	ProjectID uuid.UUID `gorm:"type:uuid"`
	Key       string
	// Bytes, not a string: from Phase 2 this holds ciphertext.
	Value     []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (e *EnvVar) BeforeCreate(*gorm.DB) error { ensureID(&e.ID); return nil }

// Domain is a custom hostname routed to a project. Verification arrives in Phase 9.
type Domain struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	TenantID   uuid.UUID `gorm:"type:uuid"`
	ProjectID  uuid.UUID `gorm:"type:uuid"`
	Hostname   string    // lowercase; unique across all tenants
	VerifiedAt *time.Time
	CreatedAt  time.Time
}

func (d *Domain) BeforeCreate(*gorm.DB) error { ensureID(&d.ID); return nil }

// BuildStatus is where a build is in queued → building → succeeded | failed.
type BuildStatus string

const (
	BuildQueued    BuildStatus = "queued"
	BuildBuilding  BuildStatus = "building"
	BuildSucceeded BuildStatus = "succeeded"
	BuildFailed    BuildStatus = "failed"
)

// Build is one attempt to turn a commit into a runnable image.
type Build struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	TenantID   uuid.UUID `gorm:"type:uuid"`
	ProjectID  uuid.UUID `gorm:"type:uuid"`
	CommitSHA  string    // full 40-character hex SHA, never a branch name
	Branch     string
	Status     BuildStatus
	ImageRef   string // set when the build succeeds
	Error      string // set when the build fails
	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (b *Build) BeforeCreate(*gorm.DB) error { ensureID(&b.ID); return nil }

// DeploymentStatus is where a deployment is in its lifecycle.
type DeploymentStatus string

const (
	DeploymentPending    DeploymentStatus = "pending"
	DeploymentDeploying  DeploymentStatus = "deploying"
	DeploymentLive       DeploymentStatus = "live"
	DeploymentSuperseded DeploymentStatus = "superseded" // replaced by a newer deploy; rollback can revive it
	DeploymentFailed     DeploymentStatus = "failed"
	DeploymentStopped    DeploymentStatus = "stopped"
)

// deploymentFrom lists, for each target status, the statuses allowed to move to it.
// Anything else is an illegal transition (e.g. failed → live).
var deploymentFrom = map[DeploymentStatus][]DeploymentStatus{
	DeploymentDeploying:  {DeploymentPending},
	DeploymentLive:       {DeploymentDeploying, DeploymentSuperseded},
	DeploymentSuperseded: {DeploymentLive},
	DeploymentFailed:     {DeploymentPending, DeploymentDeploying},
	DeploymentStopped:    {DeploymentLive, DeploymentSuperseded},
}

// AllowedFrom returns the statuses a deployment may be in to move to s.
func (s DeploymentStatus) AllowedFrom() []DeploymentStatus { return deploymentFrom[s] }

// Deployment is one build's image running as a container. Immutable apart from
// its status: a new commit or a rollback never edits an existing deployment.
type Deployment struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	TenantID  uuid.UUID `gorm:"type:uuid"`
	ProjectID uuid.UUID `gorm:"type:uuid"`
	BuildID   uuid.UUID `gorm:"type:uuid"`
	Status    DeploymentStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (d *Deployment) BeforeCreate(*gorm.DB) error { ensureID(&d.ID); return nil }

// BuildLayer is one content-addressed layer, shared by every build that produced
// the same bytes. It has no tenant: that sharing is the point (Phase 7).
type BuildLayer struct {
	Digest    string `gorm:"primaryKey"` // "sha256:" + 64 lowercase hex
	SizeBytes int64
	CreatedAt time.Time
}

// BuildLayerRef says "this build uses this layer at this position". The number of
// refs to a layer is its reference count.
type BuildLayerRef struct {
	BuildID     uuid.UUID `gorm:"type:uuid;primaryKey"`
	Position    int       `gorm:"primaryKey;autoIncrement:false"`
	LayerDigest string
}
