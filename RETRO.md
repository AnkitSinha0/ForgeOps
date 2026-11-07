# Retros

## Phase 0 — Foundation (2026-09-27)

**Intended:** repo skeleton, three bootable binaries, config / logger / Postgres / Redis, compose stack, Makefile, health endpoints. Deliverable: all three processes start and connect to infra.

**Went well**
- Deliverable met and verified with real probes: 6/6 health endpoints 200, Redis outage → `/readyz` 503 with `/healthz` still 200, auto-recovery without restart.
- Graceful shutdown verified the way production will do it: Linux build in a container, `docker stop` (SIGTERM) → both router listeners drained, exit 0 in 0.7s.

**Went wrong / surprised us**
- The official MinIO image no longer exists. The plan (and HashVault's compose, which uses `minio/minio:latest`) silently assumed it would. Caught only because the image wasn't cached — on a fresh machine HashVault's `docker compose up` will fail too.
- Two bugs caught during verification, both about *timing honesty*:
  - "listening" was logged before the port was bound — a port conflict printed "listening" and then an error. Fixed by binding synchronously with `net.Listen`.
  - `gorm.Open` pings with no deadline, so the 5s ping timeout we wrote wasn't bounding the first connect. Fixed with `DisableAutomaticPing`; verified an unreachable host now fails boot in 5s.

**Would change**
- Test failure paths (port taken, dependency down, bad config, SIGTERM) as part of writing the code, not after. Both bugs were invisible on the happy path.

**Predictions I got wrong:** none to review. Phase 0 had no `JIT/Phase0.predict.md` (built before the brief/predict step existed).

**DECISIONS.md entries that mattered most:** liveness/readiness split (verified behaviour), router least-privilege + separate ops port (sets up the Phase 6 security story).

**Engineering Desk:** no post this phase. Nothing here is a contested trade-off or a failure big enough to be an article; the MinIO image removal is a one-paragraph note, not a story.

## Phase 1 — Domain model & repositories (2026-10-30)

**Intended:** seven models (Tenant, Project, EnvVar, Domain, Build, Deployment, BuildLayer), repositories with `ErrNotFound`, GORM AutoMigrate. Deliverable: schema migrates, CRUD tests pass.

**Went well**
- Deliverable met against real Postgres: 10 integration tests pass, and 3 more runs with `-shuffle=on` all passed. Each test gets its own schema, and no test schema was left behind.
- Tenant isolation is enforced twice: `tenantID` in every repository signature, and composite foreign keys in the database. The isolation test was checked by deliberately removing the tenant filter: it went red (`got error <nil>, want record not found`).
- Invariants moved into the database where it can see them: one live deployment per project (partial unique index), layer refs that can't drift (count rows plus RESTRICT), commit SHA / slug / digest formats (CHECK).
- The controlplane migrates at boot. The second boot applies nothing.

**Went wrong / surprised us**
- The first run failed: attaching an unknown layer digest to a build that already had layers hit the `(build_id, position)` primary key before the foreign key. It was a test bug, but it was also a real lesson: when a row breaks two constraints, which error Postgres reports is not something to rely on.
- The phase plan said AutoMigrate. The schema turned out to need things AutoMigrate can't express, so it became versioned SQL instead (DECISIONS.md).
- `gofmt` flagged two files after a `sed`-inserted import and `" + table"` spacing. Run `gofmt -l` before calling a phase done.

**Would change**
- Write `Phase1.predict.md` before "go". This phase had no author predictions, so nothing was learned from the prediction step.

**Predictions I got wrong:** none to review. `Phase1.predict.md` was empty at "go", and Claude filled it in at the author's request.

**DECISIONS.md entries that mattered most:** composite-FK tenancy (the Phase 6 security story starts in the data layer), and ref-rows-as-refcount (it decides how Phase 7's GC works).

**Engineering Desk:** no post yet. The candidate is "the counter that drifts: why ForgeOps counts layer references with rows, not a column". It's a real trade-off, but nothing failed for it, so it's held until Phase 7 confirms it under the GC.

## Phase 2 — Control plane API, auth, tenancy (2026-11-08)

**Intended:** chi routes, project and env var CRUD with encryption at rest, auth, tenant isolation, keyset pagination. Deliverable: create a project, set env vars, list deployments via curl.

**Went well**
- Deliverable met with the real binary and curl, including the failure cases: no key is 401, another tenant's resource is 404, and Postgres holds only ciphertext and hashes.
- 42 tests + 60 subtests pass, shuffled, and under `-race` in a Linux container.
- Seven deliberate breakages were each caught by tests, so the security tests aren't hollow.

**Went wrong / surprised us**
- **A real cross-tenant hole had shipped in Phase 1.** `EnvVarRepository.Set` let any tenant overwrite another's variable, because a composite foreign key doesn't run on an upsert's update path. Ten passing tests, a mutation check and this file's praise of the isolation model all missed it. `TestTenantIsolation` tried inserts, never an upsert onto an existing key. It was found by reading the code while planning this phase, and nothing could reach it before this API existed.
- Timestamps disagreed in two places: nanoseconds in responses vs microseconds stored (caught by a test), and `updated_at` before `created_at` (only caught by reading real curl output).
- The Sonnet review found no high or medium issues, but it did find three low-severity bugs, one contradicting my own comment: `r.URL.Query()` silently dropped a malformed cursor.

**Would change**
- Write the isolation test per write path (insert, update, upsert, delete), not per resource.
- Write `Phase2.predict.md`. There were no author predictions, for the second phase in a row.

**Predictions I got wrong:** none to review. There's no `Phase2.predict.md`, because the author chose to start without one.

**DECISIONS.md entries that mattered most:** the upsert tenant check, which corrects an earlier decision's claim; and write-only env vars with row-bound ciphertext, which set up Phase 6's security story.

**Engineering Desk:** one draft in progress, on the foreign key that didn't fire. It's a real failure with a fix, not a clean success.
