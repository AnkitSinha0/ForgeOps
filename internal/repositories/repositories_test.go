package repositories

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/migrations"
	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// These are integration tests against a real Postgres. Constraints, cascades and
// SQLSTATE codes are the behaviour under test, and a mock can't have any of them.
//
//	make up && make test-db
//
// Each test gets its own schema (a private namespace inside the database), runs
// the real migrations into it and drops it afterwards. Tests share no rows, so
// they can run in parallel and in any order.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set (make up && make test-db)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	schema := "test_" + hex.EncodeToString(buf)
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("creating schema: %v", err)
	}

	// search_path in the DSN makes every pooled connection resolve unqualified
	// table names to the test schema.
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing DSN: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connecting to schema: %v", err)
	}
	if err := migrations.Migrate(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("got error %v, want %v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fixture: a tenant with one project and one queued build.
type fixture struct {
	tenant  *models.Tenant
	project *models.Project
	build   *models.Build
}

func newFixture(t *testing.T, db *gorm.DB, slug string) fixture {
	t.Helper()
	ctx := context.Background()
	tn := &models.Tenant{Name: strings.ToUpper(slug), Slug: slug}
	must(t, NewTenantRepository(db).Create(ctx, tn))
	p := &models.Project{TenantID: tn.ID, Name: "web", RepoURL: "https://github.com/" + slug + "/web", Branch: "main"}
	must(t, NewProjectRepository(db).Create(ctx, p))
	b := &models.Build{TenantID: tn.ID, ProjectID: p.ID, CommitSHA: strings.Repeat("a", 40), Branch: "main"}
	must(t, NewBuildRepository(db).Create(ctx, b))
	return fixture{tn, p, b}
}

func digest(n int) string { return fmt.Sprintf("sha256:%064x", n) }

// ── migrations ──────────────────────────────────────────────────────────────

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t) // already migrated once
	must(t, migrations.Migrate(context.Background(), db, zap.NewNop()))
	var n int64
	must(t, db.Raw("SELECT count(*) FROM schema_migrations").Scan(&n).Error)
	if n != 1 {
		t.Fatalf("schema_migrations has %d rows after migrating twice, want 1", n)
	}
}

// ── tenants ─────────────────────────────────────────────────────────────────

func TestTenantCRUD(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewTenantRepository(db)

	tn := &models.Tenant{Name: "Acme", Slug: "acme"}
	must(t, repo.Create(ctx, tn))
	if tn.ID == uuid.Nil {
		t.Fatal("Create left ID empty")
	}
	got, err := repo.FindBySlug(ctx, "acme")
	must(t, err)
	if got.ID != tn.ID {
		t.Fatalf("FindBySlug id = %v, want %v", got.ID, tn.ID)
	}
	wantErr(t, repo.Create(ctx, &models.Tenant{Name: "Other", Slug: "acme"}), ErrConflict)
	wantErr(t, repo.Create(ctx, &models.Tenant{Name: "Bad", Slug: "Not A Slug"}), ErrInvalid)

	must(t, repo.Delete(ctx, tn.ID))
	_, err = repo.FindByID(ctx, tn.ID)
	wantErr(t, err, ErrNotFound)
	wantErr(t, repo.Delete(ctx, tn.ID), ErrNotFound)
}

// ── projects ────────────────────────────────────────────────────────────────

func TestProjectCRUD(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	repo := NewProjectRepository(db)

	api := &models.Project{TenantID: f.tenant.ID, Name: "api", RepoURL: "https://github.com/acme/api", Branch: "main"}
	must(t, repo.Create(ctx, api))
	wantErr(t, repo.Create(ctx, &models.Project{TenantID: f.tenant.ID, Name: "api", RepoURL: "x", Branch: "main"}), ErrConflict)
	wantErr(t, repo.Create(ctx, &models.Project{TenantID: uuid.New(), Name: "ghost", RepoURL: "x", Branch: "main"}), ErrBadReference)

	list, err := repo.List(ctx, f.tenant.ID)
	must(t, err)
	if len(list) != 2 || list[0].Name != "api" || list[1].Name != "web" {
		t.Fatalf("List = %+v, want [api web]", list)
	}

	// Update must write an empty value, not silently skip it.
	api.Branch = ""
	api.RepoURL = "https://github.com/acme/api-v2"
	must(t, repo.Update(ctx, api))
	got, err := repo.FindByName(ctx, f.tenant.ID, "api")
	must(t, err)
	if got.Branch != "" || got.RepoURL != "https://github.com/acme/api-v2" {
		t.Fatalf("after Update got branch %q repo %q", got.Branch, got.RepoURL)
	}

	must(t, repo.Delete(ctx, f.tenant.ID, api.ID))
	_, err = repo.FindByID(ctx, f.tenant.ID, api.ID)
	wantErr(t, err, ErrNotFound)
}

// The test that matters most: tenant B can't see, change or delete tenant A's data,
// and can't attach its own rows to A's project.
func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	a := newFixture(t, db, "acme")
	b := newFixture(t, db, "beta")
	projects := NewProjectRepository(db)
	envs := NewEnvVarRepository(db)
	builds := NewBuildRepository(db)
	deploys := NewDeploymentRepository(db)
	domains := NewDomainRepository(db)

	// Reads with B's tenant ID and A's row IDs find nothing.
	_, err := projects.FindByID(ctx, b.tenant.ID, a.project.ID)
	wantErr(t, err, ErrNotFound)
	_, err = builds.FindByID(ctx, b.tenant.ID, a.build.ID)
	wantErr(t, err, ErrNotFound)
	list, err := projects.List(ctx, b.tenant.ID)
	must(t, err)
	if len(list) != 1 || list[0].ID != b.project.ID {
		t.Fatalf("B's List = %+v, want only B's project", list)
	}

	// Writes with B's tenant ID don't touch A's rows.
	hijack := *a.project
	hijack.TenantID = b.tenant.ID
	hijack.RepoURL = "https://evil.example/repo"
	wantErr(t, projects.Update(ctx, &hijack), ErrNotFound)
	wantErr(t, projects.Delete(ctx, b.tenant.ID, a.project.ID), ErrNotFound)
	wantErr(t, builds.MarkBuilding(ctx, b.tenant.ID, a.build.ID), ErrNotFound)

	// Child rows claiming B's tenant but A's project are refused by the
	// composite foreign keys, even though nothing in the Go code checks it.
	wantErr(t, envs.Set(ctx, &models.EnvVar{TenantID: b.tenant.ID, ProjectID: a.project.ID, Key: "X", Value: []byte("1")}), ErrBadReference)
	wantErr(t, domains.Create(ctx, &models.Domain{TenantID: b.tenant.ID, ProjectID: a.project.ID, Hostname: "evil.example"}), ErrBadReference)
	wantErr(t, builds.Create(ctx, &models.Build{TenantID: b.tenant.ID, ProjectID: a.project.ID, CommitSHA: strings.Repeat("b", 40), Branch: "main"}), ErrBadReference)
	// A deployment of B's project pointing at A's build is refused too.
	wantErr(t, deploys.Create(ctx, &models.Deployment{TenantID: b.tenant.ID, ProjectID: b.project.ID, BuildID: a.build.ID}), ErrBadReference)

	// And A's data is intact after all of it.
	got, err := projects.FindByID(ctx, a.tenant.ID, a.project.ID)
	must(t, err)
	if got.RepoURL != a.project.RepoURL {
		t.Fatalf("A's project was changed: %q", got.RepoURL)
	}
}

// ── env vars ────────────────────────────────────────────────────────────────

func TestEnvVarSetIsUpsert(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	repo := NewEnvVarRepository(db)

	first := &models.EnvVar{TenantID: f.tenant.ID, ProjectID: f.project.ID, Key: "API_KEY", Value: []byte("one")}
	must(t, repo.Set(ctx, first))
	second := &models.EnvVar{TenantID: f.tenant.ID, ProjectID: f.project.ID, Key: "API_KEY", Value: []byte("two")}
	must(t, repo.Set(ctx, second))
	if second.ID != first.ID {
		t.Fatalf("upsert returned id %v, want the original %v", second.ID, first.ID)
	}

	got, err := repo.Get(ctx, f.tenant.ID, f.project.ID, "API_KEY")
	must(t, err)
	if string(got.Value) != "two" {
		t.Fatalf("value = %q, want %q", got.Value, "two")
	}
	// Arbitrary bytes survive the round trip (Phase 2 stores ciphertext here).
	raw := &models.EnvVar{TenantID: f.tenant.ID, ProjectID: f.project.ID, Key: "BLOB", Value: []byte{0, 255, 10, 0}}
	must(t, repo.Set(ctx, raw))
	got, err = repo.Get(ctx, f.tenant.ID, f.project.ID, "BLOB")
	must(t, err)
	if string(got.Value) != string([]byte{0, 255, 10, 0}) {
		t.Fatalf("binary value came back as %v", got.Value)
	}

	wantErr(t, repo.Set(ctx, &models.EnvVar{TenantID: f.tenant.ID, ProjectID: f.project.ID, Key: "1BAD", Value: []byte("x")}), ErrInvalid)
	list, err := repo.List(ctx, f.tenant.ID, f.project.ID)
	must(t, err)
	if len(list) != 2 || list[0].Key != "API_KEY" || list[1].Key != "BLOB" {
		t.Fatalf("List = %+v", list)
	}
	must(t, repo.Delete(ctx, f.tenant.ID, f.project.ID, "API_KEY"))
	wantErr(t, repo.Delete(ctx, f.tenant.ID, f.project.ID, "API_KEY"), ErrNotFound)
}

// ── domains ─────────────────────────────────────────────────────────────────

// The composite foreign key does not protect an upsert. On a conflict Postgres takes
// the DO UPDATE path against the existing row, tenant_id doesn't change, and the
// foreign key is never rechecked. Found while planning Phase 2; this test was red
// before the fix.
func TestEnvVarSetCannotOverwriteAnotherTenantsVariable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	a, b := newFixture(t, db, "acme"), newFixture(t, db, "bravo")
	repo, ctx := NewEnvVarRepository(db), context.Background()

	must(t, repo.Set(ctx, &models.EnvVar{TenantID: a.tenant.ID, ProjectID: a.project.ID, Key: "DB_PASSWORD", Value: []byte("a-secret")}))

	// Tenant B names A's project and A's key, under B's own tenant ID.
	attack := &models.EnvVar{TenantID: b.tenant.ID, ProjectID: a.project.ID, Key: "DB_PASSWORD", Value: []byte("overwritten")}
	wantErr(t, repo.Set(ctx, attack), ErrNotFound)

	got, err := repo.Get(ctx, a.tenant.ID, a.project.ID, "DB_PASSWORD")
	must(t, err)
	if string(got.Value) != "a-secret" {
		t.Fatalf("tenant B overwrote tenant A's variable: value is now %q", got.Value)
	}
}

func TestDomainHostnameUniqueAcrossTenants(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	a := newFixture(t, db, "acme")
	b := newFixture(t, db, "beta")
	repo := NewDomainRepository(db)

	d := &models.Domain{TenantID: a.tenant.ID, ProjectID: a.project.ID, Hostname: "App.Acme.com"}
	must(t, repo.Create(ctx, d))
	if d.Hostname != "app.acme.com" {
		t.Fatalf("hostname stored as %q, want lowercase", d.Hostname)
	}
	// Same host, different case, different tenant: still the same host.
	wantErr(t, repo.Create(ctx, &models.Domain{TenantID: b.tenant.ID, ProjectID: b.project.ID, Hostname: "APP.acme.com"}), ErrConflict)

	at := time.Now().UTC().Truncate(time.Microsecond)
	must(t, repo.MarkVerified(ctx, a.tenant.ID, d.ID, at))
	got, err := repo.FindByID(ctx, a.tenant.ID, d.ID)
	must(t, err)
	if got.VerifiedAt == nil || !got.VerifiedAt.Equal(at) {
		t.Fatalf("VerifiedAt = %v, want %v", got.VerifiedAt, at)
	}
	ds, err := repo.ListByProject(ctx, a.tenant.ID, a.project.ID)
	must(t, err)
	if len(ds) != 1 {
		t.Fatalf("ListByProject returned %d domains, want 1", len(ds))
	}
	must(t, repo.Delete(ctx, a.tenant.ID, d.ID))
}

// ── builds ──────────────────────────────────────────────────────────────────

func TestBuildTransitions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	repo := NewBuildRepository(db)
	tid, id := f.tenant.ID, f.build.ID

	if f.build.Status != models.BuildQueued {
		t.Fatalf("new build status = %q, want queued", f.build.Status)
	}
	wantErr(t, repo.MarkSucceeded(ctx, tid, id, "img"), ErrInvalidTransition) // can't skip building
	must(t, repo.MarkBuilding(ctx, tid, id))
	wantErr(t, repo.MarkBuilding(ctx, tid, id), ErrInvalidTransition) // a second worker loses
	must(t, repo.MarkSucceeded(ctx, tid, id, "registry.local/acme/web:aaaa"))
	wantErr(t, repo.MarkFailed(ctx, tid, id, "late failure"), ErrInvalidTransition) // terminal

	got, err := repo.FindByID(ctx, tid, id)
	must(t, err)
	if got.Status != models.BuildSucceeded || got.ImageRef != "registry.local/acme/web:aaaa" ||
		got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("after success: %+v", got)
	}
	wantErr(t, repo.MarkBuilding(ctx, tid, uuid.New()), ErrNotFound)
	wantErr(t, repo.Create(ctx, &models.Build{TenantID: tid, ProjectID: f.project.ID, CommitSHA: "main", Branch: "main"}), ErrInvalid)

	// The newest build comes first.
	newer := &models.Build{TenantID: tid, ProjectID: f.project.ID, CommitSHA: strings.Repeat("c", 40), Branch: "main"}
	must(t, repo.Create(ctx, newer))
	must(t, repo.MarkFailed(ctx, tid, newer.ID, "npm ci exited 1"))
	list, err := repo.ListByProject(ctx, tid, f.project.ID, 10)
	must(t, err)
	if len(list) != 2 || list[0].ID != newer.ID || list[0].Error != "npm ci exited 1" {
		t.Fatalf("ListByProject = %+v", list)
	}
}

// ── deployments ─────────────────────────────────────────────────────────────

func TestDeploymentOneLivePerProjectAndRollback(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	repo := NewDeploymentRepository(db)
	tid := f.tenant.ID

	v1 := &models.Deployment{TenantID: tid, ProjectID: f.project.ID, BuildID: f.build.ID}
	v2 := &models.Deployment{TenantID: tid, ProjectID: f.project.ID, BuildID: f.build.ID} // a redeploy of the same build
	must(t, repo.Create(ctx, v1))
	must(t, repo.Create(ctx, v2))

	wantErr(t, repo.SetStatus(ctx, tid, v1.ID, models.DeploymentLive), ErrInvalidTransition) // pending can't go live
	must(t, repo.SetStatus(ctx, tid, v1.ID, models.DeploymentDeploying))
	must(t, repo.SetStatus(ctx, tid, v1.ID, models.DeploymentLive))

	must(t, repo.SetStatus(ctx, tid, v2.ID, models.DeploymentDeploying))
	// Two live deployments for one project: the partial unique index refuses it.
	wantErr(t, repo.SetStatus(ctx, tid, v2.ID, models.DeploymentLive), ErrConflict)

	// The real order: demote the old one, then promote the new one.
	must(t, repo.SetStatus(ctx, tid, v1.ID, models.DeploymentSuperseded))
	must(t, repo.SetStatus(ctx, tid, v2.ID, models.DeploymentLive))
	// Rollback: v2 steps down, v1 comes back without a rebuild.
	must(t, repo.SetStatus(ctx, tid, v2.ID, models.DeploymentSuperseded))
	must(t, repo.SetStatus(ctx, tid, v1.ID, models.DeploymentLive))

	got, err := repo.FindByID(ctx, tid, v1.ID)
	must(t, err)
	if got.Status != models.DeploymentLive {
		t.Fatalf("v1 status = %q, want live", got.Status)
	}
	wantErr(t, repo.SetStatus(ctx, tid, v1.ID, models.DeploymentPending), ErrInvalidTransition)
	list, err := repo.ListByProject(ctx, tid, f.project.ID, 10)
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("ListByProject returned %d, want 2", len(list))
	}
}

// ── build layers ────────────────────────────────────────────────────────────

func TestBuildLayerDedupAndRefCounting(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	a := newFixture(t, db, "acme")
	b := newFixture(t, db, "beta")
	repo := NewBuildLayerRepository(db)
	base, deps := digest(1), digest(2)

	created, err := repo.Ensure(ctx, &models.BuildLayer{Digest: base, SizeBytes: 100})
	must(t, err)
	if !created {
		t.Fatal("first Ensure reported a dedup hit")
	}
	created, err = repo.Ensure(ctx, &models.BuildLayer{Digest: base, SizeBytes: 100})
	must(t, err)
	if created {
		t.Fatal("second Ensure of the same digest inserted again")
	}
	_, err = repo.Ensure(ctx, &models.BuildLayer{Digest: deps, SizeBytes: 5000})
	must(t, err)
	_, err = repo.Ensure(ctx, &models.BuildLayer{Digest: "sha256:not-hex", SizeBytes: 1})
	wantErr(t, err, ErrInvalid)

	// Two tenants share the base layer.
	must(t, repo.AttachToBuild(ctx, a.tenant.ID, a.build.ID, []string{base, deps}))
	must(t, repo.AttachToBuild(ctx, b.tenant.ID, b.build.ID, []string{base}))
	wantErr(t, repo.AttachToBuild(ctx, b.tenant.ID, a.build.ID, []string{base}), ErrNotFound) // not B's build
	// A build's layer list is written once: attaching again collides on (build_id, position).
	wantErr(t, repo.AttachToBuild(ctx, a.tenant.ID, a.build.ID, []string{base}), ErrConflict)
	// A digest that was never Ensured is refused by the foreign key (fresh build, so
	// the position can't collide first).
	fresh := &models.Build{TenantID: a.tenant.ID, ProjectID: a.project.ID, CommitSHA: strings.Repeat("d", 40), Branch: "main"}
	must(t, NewBuildRepository(db).Create(ctx, fresh))
	wantErr(t, repo.AttachToBuild(ctx, a.tenant.ID, fresh.ID, []string{digest(9)}), ErrBadReference)

	n, err := repo.RefCount(ctx, base)
	must(t, err)
	if n != 2 {
		t.Fatalf("base refcount = %d, want 2", n)
	}
	wantErr(t, repo.Delete(ctx, base), ErrInUse) // the database refuses

	// Deleting tenant B's project cascades to its build and refs; the count follows.
	must(t, NewProjectRepository(db).Delete(ctx, b.tenant.ID, b.project.ID))
	n, err = repo.RefCount(ctx, base)
	must(t, err)
	if n != 1 {
		t.Fatalf("base refcount after cascade = %d, want 1", n)
	}

	// Deleting A's project leaves both layers unreferenced, so GC may take them.
	must(t, NewProjectRepository(db).Delete(ctx, a.tenant.ID, a.project.ID))
	free, err := repo.Unreferenced(ctx, 10)
	must(t, err)
	if len(free) != 2 {
		t.Fatalf("Unreferenced = %d layers, want 2", len(free))
	}
	must(t, repo.Delete(ctx, base))
	_, err = repo.Find(ctx, base)
	wantErr(t, err, ErrNotFound)
}

// ── cascades ────────────────────────────────────────────────────────────────

func TestProjectDeleteCascades(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	must(t, NewEnvVarRepository(db).Set(ctx, &models.EnvVar{TenantID: f.tenant.ID, ProjectID: f.project.ID, Key: "SECRET", Value: []byte("s")}))
	must(t, NewDomainRepository(db).Create(ctx, &models.Domain{TenantID: f.tenant.ID, ProjectID: f.project.ID, Hostname: "acme.example"}))
	must(t, NewDeploymentRepository(db).Create(ctx, &models.Deployment{TenantID: f.tenant.ID, ProjectID: f.project.ID, BuildID: f.build.ID}))

	must(t, NewTenantRepository(db).Delete(ctx, f.tenant.ID))
	for _, table := range []string{"projects", "env_vars", "domains", "builds", "deployments"} {
		var n int64
		must(t, db.Raw("SELECT count(*) FROM "+table).Scan(&n).Error)
		if n != 0 {
			t.Errorf("%s still has %d rows after deleting the tenant", table, n)
		}
	}
}
