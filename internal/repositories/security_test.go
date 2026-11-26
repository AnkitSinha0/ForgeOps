package repositories

import (
	"context"
	"strings"
	"testing"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/google/uuid"
)

// Regression tests from the 2026-11-11 security audit (JIT/SecurityAudit-2026-11-11.md
// in the main checkout). Each one was red against f85ea24 and is green after the fix.

// M5: hostnames were stored with only strings.ToLower. A trailing-dot or whitespace
// variant of another tenant's host got past the global unique index, Unicode
// lowercasing differed between Go and Postgres, and junk like "*.com" was accepted.
func TestDomainHostnameIsValidatedAndCanonical(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	a := newFixture(t, db, "acme")
	b := newFixture(t, db, "bravo")
	repo := NewDomainRepository(db)

	must(t, repo.Create(ctx, &models.Domain{TenantID: b.tenant.ID, ProjectID: b.project.ID, Hostname: "shop.example.com"}))

	bad := []string{
		"", " ", "a b", "-", "a/b", "*.com", "exa\nmple.com", "a\x00b.com",
		strings.Repeat("a", 300) + ".com", strings.Repeat("a", 64) + ".com",
		// Non-ASCII must arrive as punycode. The next line holds U+00FC, U+0130 and U+212A
		// (Kelvin): Go's strings.ToLower folds the last two to ASCII "i" and "k".
		"ünï.com", "İ.com", "K.com", // non-ASCII: see below
		"shop.example.com.", "shop.example.com ", " shop.example.com", // variants of B's host
		"a..b.com", "-a.com", "a-.com", ".a.com",
		"localhost", "1.2.3.4", "999.com.123", // single label, IP literal, numeric TLD
	}
	for _, h := range bad {
		err := repo.Create(ctx, &models.Domain{TenantID: a.tenant.ID, ProjectID: a.project.ID, Hostname: h})
		if err == nil {
			t.Errorf("hostname %q was accepted", h)
		}
	}

	good := map[string]string{
		"App.Acme.com":          "app.acme.com",
		"xn--bcher-kva.example": "xn--bcher-kva.example",
		"a-b.c-d.io":            "a-b.c-d.io",
		"x1.y2.z3.dev":          "x1.y2.z3.dev",
	}
	for in, want := range good {
		d := &models.Domain{TenantID: a.tenant.ID, ProjectID: a.project.ID, Hostname: in}
		if err := repo.Create(ctx, d); err != nil {
			t.Errorf("hostname %q refused: %v", in, err)
			continue
		}
		if d.Hostname != want {
			t.Errorf("hostname %q stored as %q, want %q", in, d.Hostname, want)
		}
	}

	// The database refuses a bad hostname even if Go code skips the check.
	err := db.Exec(`INSERT INTO domains (id, tenant_id, project_id, hostname, created_at)
		VALUES (?, ?, ?, 'shop.example.com.', now())`, uuid.New(), a.tenant.ID, a.project.ID).Error
	if err == nil {
		t.Error("the domains table accepted a hostname with a trailing dot")
	}
}

// L1: a build could be marked succeeded with no image, and a failure reason could be
// any size and contain a NUL byte (which Postgres text can't store: a raw 22021 error).
func TestBuildResultsAreBounded(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	repo := NewBuildRepository(db)
	tid := f.tenant.ID

	must(t, repo.MarkBuilding(ctx, tid, f.build.ID))
	wantErr(t, repo.MarkSucceeded(ctx, tid, f.build.ID, ""), ErrInvalid)
	wantErr(t, repo.MarkSucceeded(ctx, tid, f.build.ID, "   "), ErrInvalid)

	// An executor reporting a failure must never be refused, so the reason is cleaned
	// up rather than rejected.
	long := strings.Repeat("x", 10_000) + "\x00tail"
	must(t, repo.MarkFailed(ctx, tid, f.build.ID, long))
	got, err := repo.FindByID(ctx, tid, f.build.ID)
	must(t, err)
	if len(got.Error) > 4096 || strings.ContainsRune(got.Error, 0) {
		t.Fatalf("stored reason is %d bytes, NUL present: %v", len(got.Error), strings.ContainsRune(got.Error, 0))
	}

	// The database backs the image rule up.
	b2 := &models.Build{TenantID: tid, ProjectID: f.project.ID, CommitSHA: strings.Repeat("b", 40), Branch: "main"}
	must(t, repo.Create(ctx, b2))
	err = db.Exec("UPDATE builds SET status = 'succeeded', image_ref = '' WHERE id = ?", b2.ID).Error
	if err == nil {
		t.Error("the builds table accepted succeeded with an empty image_ref")
	}
}

// L1: a deployment could be created from a build that never produced an image.
func TestDeploymentNeedsASucceededBuild(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	f := newFixture(t, db, "acme")
	builds, deps := NewBuildRepository(db), NewDeploymentRepository(db)
	tid := f.tenant.ID

	dep := func() *models.Deployment {
		return &models.Deployment{TenantID: tid, ProjectID: f.project.ID, BuildID: f.build.ID}
	}
	wantErr(t, deps.Create(ctx, dep()), ErrInvalid) // queued
	must(t, builds.MarkBuilding(ctx, tid, f.build.ID))
	wantErr(t, deps.Create(ctx, dep()), ErrInvalid) // building

	failed := &models.Build{TenantID: tid, ProjectID: f.project.ID, CommitSHA: strings.Repeat("c", 40), Branch: "main"}
	must(t, builds.Create(ctx, failed))
	must(t, builds.MarkFailed(ctx, tid, failed.ID, "boom"))
	wantErr(t, deps.Create(ctx, &models.Deployment{TenantID: tid, ProjectID: f.project.ID, BuildID: failed.ID}), ErrInvalid)

	must(t, builds.MarkSucceeded(ctx, tid, f.build.ID, "registry.local/acme/web:aaaa"))
	must(t, deps.Create(ctx, dep()))

	// Another tenant's build is still a bad reference, not a status problem.
	other := newFixture(t, db, "bravo")
	wantErr(t, deps.Create(ctx, &models.Deployment{TenantID: tid, ProjectID: f.project.ID, BuildID: other.build.ID}), ErrBadReference)
}
