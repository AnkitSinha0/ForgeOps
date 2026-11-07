# Decisions

## [2026-09-27] The router connects to Redis only — no database credentials
Why: the router is the only internet-facing process. If it is ever compromised, it should hold nothing that reaches tenant data in Postgres. Everything it needs (host → backend) will live in the Redis registry from Phase 5.
Alternative considered: give all three services the same Postgres + Redis wiring for symmetry — rejected because symmetry is not a security property; least privilege on the edge process is.
Would flip if: the router needs data that cannot be precomputed into the Redis registry (e.g. per-request authorization against tenant records) — and even then it gets a read-only role on one narrow view, not the shared `DATABASE_DSN`. (reconstructed 2026-09-30)

## [2026-09-27] Router health probes live on a separate ops port (:8082), not on the proxy port (:8000)
Why: every path on the proxy port belongs to a tenant app. A router-owned `/healthz` there would shadow any tenant that ships its own `/healthz`. The ops port also never needs to be exposed publicly. The two listeners run in one errgroup: if either fails, both stop, because a router that serves traffic but cannot report health is worse than one that restarts.
Alternative considered: a reserved path prefix like `/__forgeops/healthz` on the proxy port — rejected because it still steals a slice of every tenant's URL space and puts the probe on the public internet.
Would flip if: the deployment target exposes only one port per instance and probes that port (some single-port PaaS / load balancers) — then a reserved prefix is the only option left. (reconstructed 2026-09-30)

## [2026-09-27] Liveness (`/healthz`) never checks dependencies; readiness (`/readyz`) does
Why: an orchestrator restarts a container whose liveness fails. If liveness pinged Postgres, one database blip would restart every healthy service at once, then all of them would reconnect together. Readiness failing only removes the instance from traffic until the dependency recovers — verified: stopping Redis flips `/readyz` to 503 while `/healthz` stays 200, and `/readyz` returns to 200 on Redis restart with no service restart.
Alternative considered: one `/health` endpoint that checks everything — rejected for the restart-storm reason above.
Would flip if: the platform has one health URL and uses it only to route traffic, never to restart (a plain load balancer, no orchestrator) — then one check-everything endpoint loses nothing. (reconstructed 2026-09-30)

## [2026-09-27] Object storage: pinned `pgsty/minio` fork instead of the official MinIO image
Why: MinIO stopped publishing community Docker images; `minio/minio` is no longer pullable (`pull access denied`), and `quay.io/minio/minio` returns 401. `pgsty/minio` is a maintained build of the same server (same env vars, console, `mc ready` healthcheck), so nothing else in the plan changes. Pinned to `RELEASE.2026-08-04T00-00-00Z`, not `:latest`. Services will talk to it only through the S3 API (Phase 7), so swapping it is a one-line compose change.
Alternative considered: SeaweedFS or RustFS (both S3-compatible, both pullable) — rejected for now because they are not drop-in (different config and credential setup), and Phase 7 is the first time any S3 store is actually used. Revisit then if the fork stops being maintained.
Would flip if: `pgsty/minio` stops publishing releases or lags on security fixes — move to SeaweedFS or RustFS, ideally at Phase 7 when the S3 client is first written. (reconstructed 2026-09-30)

## [2026-09-27] Config from environment only (defaults in code), no config.yaml — diverges from HashVault
Why: three binaries that will run in containers. With defaults in `config.go` and overrides from env / `.env`, each binary starts from any working directory with zero files mounted. HashVault's required `config.yaml` makes the binary fail when started outside the repo root. Viper is used as an instance (`viper.New()`), not the global, so the three services and future tests share no hidden state.
Alternative considered: HashVault's pattern (required config.yaml for tunables + .env for secrets) — rejected because it adds a file every container must mount and a second place to look for a value, with no benefit at this size.
Would flip if: config grows values env vars express badly — lists, per-tenant maps, feature-flag tables — then a mounted config file earns its place. (reconstructed 2026-09-30)

## [2026-10-30] Tenancy: `tenant_id` on every owned table + composite foreign keys + `tenantID` in every repository signature
Why: a child row must never point at another tenant's parent. With `(tenant_id, project_id) REFERENCES projects (tenant_id, id)` the database checks the pair, so `tenant_id = B, project_id = <A's project>` is refused even if Go code has a bug (verified in `TestTenantIsolation`; corrected 2026-11-08: this holds for inserts, not for an upsert's update path, see below). Taking `tenantID` as a parameter makes forgetting the scope a compile error, not a leak.
Alternative considered: `tenant_id` only on `projects` with joins (a forgotten join is a leak); single-column FKs (they accept mismatched pairs); Postgres row-level security (scoping hidden in per-connection session state that every pooled connection must set correctly).
Would flip if: a non-Go client gets direct database access. Then RLS is the only enforcement that covers code you don't own.

## [2026-10-30] Versioned SQL migrations with a small embedded runner, not GORM AutoMigrate (diverges from the phase plan)
Why: the schema needs composite and three-column foreign keys, a partial unique index, CHECK regexes and mixed CASCADE/RESTRICT rules, none of which AutoMigrate can express. AutoMigrate also never drops or renames columns. One numbered SQL file is the schema, in one place, applied once, in one transaction, behind `LOCK TABLE schema_migrations`.
Alternative considered: AutoMigrate plus raw SQL for constraints (the schema split across two places); goose / golang-migrate (right at scale, but one migration doesn't need it while learning what a migration tool does).
Would flip if: down-migrations, non-transactional migrations (`CREATE INDEX CONCURRENTLY`) or a separate CI migrate step are needed. Then switch to goose, which reads the same numbered SQL files.

## [2026-10-30] Primary keys are random UUIDv4 generated in Go (`BeforeCreate`), not sequences or database defaults
Why: IDs appear in URLs, so they must not be guessable or leak volume. Random keys avoid the CockroachDB write hotspot (Phase 11b). Generating them in Go means the struct holds its ID straight after Create, on any database. BuildLayer keeps its digest as its natural key.
Alternative considered: bigserial (guessable, hotspot); `DEFAULT gen_random_uuid()` (works, but needs RETURNING and a default in every target database); UUIDv7 (time-ordered, which brings the hotspot back).
Would flip if: Postgres-only for good and random-insert index cost shows up in profiles. Then UUIDv7.

## [2026-10-30] A layer's reference count is the number of `build_layer_refs` rows, not a counter column
Why: builds are deleted by CASCADE (project or tenant delete), which runs no Go code, so a counter maintained in Go would drift upward and leak layers forever. Counting rows can't drift: deleting a project dropped the base layer's count from 2 to 1 on its own (`TestBuildLayerDedupAndRefCounting`). `ON DELETE RESTRICT` makes deleting a referenced layer impossible, which also settles the GC-vs-attach race inside the database.
Alternative considered: a `ref_count` column (HashVault's storage_objects style). It drifts under cascades and is a hot row for popular base images.
Would flip if: counting millions of refs gets slow on a hot path. Then add a counter maintained by a database trigger (triggers fire on cascades), never by application code.

## [2026-10-30] Status changes are one conditional UPDATE (`WHERE status IN (allowed)`), judged by RowsAffected
Why: read-then-write lets two workers both move a build from queued to building. The conditional update is atomic: exactly one wins and the other gets `ErrInvalidTransition` (verified). "One live deployment per project" is a unique partial index, so the database refuses a second live deployment (`ErrConflict`).
Alternative considered: SELECT ... FOR UPDATE in a transaction (correct, but a lock plus extra round trips for a single-row decision).
Would flip if: the decision depends on other rows (quotas: "fewer than N running builds per tenant", Phase 9). That needs a transaction with locking, or a counter in the same statement.

## [2026-11-08] The tenant check on an upsert lives in the statement (`DO UPDATE ... WHERE env_vars.tenant_id = EXCLUDED.tenant_id`)
Why: this fixes a real hole from Phase 1. On a conflict Postgres updates the existing row, tenant_id doesn't change, and the composite foreign key never fires, so tenant B could overwrite tenant A's variable by naming its project ID and key (`TestEnvVarSetCannotOverwriteAnotherTenantsVariable` was red before the fix). The 2026-10-30 claim that the foreign key refuses mismatched pairs "even if Go code has a bug" holds for inserts only.
Alternative considered: a project-ownership check in the service (leaves the repository unsafe for the next caller); conflicting on `(tenant_id, project_id, key)` (misses the existing row, then fails the old unique constraint and looks like a conflict).
Would flip if: the table moves to row-level security, which covers every statement.

## [2026-11-08] Auth is per-tenant API keys, stored as SHA-256, checked in the database on every request
Why: callers are curl and CI today. One indexed lookup resolves the tenant, and a revoke applies to the next request (verified). A leaked table holds nothing usable, and a 256-bit random key can't be guessed, so a slow hash adds only per-request cost.
Alternative considered: JWT (can't be revoked early without a server-side list, which brings the lookup back); bcrypt/argon2 (built for low-entropy passwords, and salting stops lookup by hash); a Redis cache (a revoked key keeps working until the entry expires).
Would flip if: the Phase 12 dashboard needs browser logins, or lookups show up in profiles. Then a short-lived cache with a stated revocation delay.
Note: `origin/Aditya` (68c7c15) proposes the same auth and bootstrap design. It was ignored at the author's request and isn't merged, but Claude had read its commit message before choosing, so this isn't independent agreement.

## [2026-11-08] The first tenant and key come from a server-side CLI (`controlplane admin create-tenant`)
Why: a new install has no credential, so no API call can authenticate. A CLI needs server access, adds no public route and no master secret, and also recovers a tenant that lost all its keys. Tenant and key are created in one transaction.
Alternative considered: an admin endpoint with a shared secret (a public route and a master credential to attack and rotate); a seed migration (a known key in every install).
Would flip if: public self-service signup arrives.

## [2026-11-08] A tenant is one actor: no users or roles inside it yet
Why: nothing in Phases 2–11 needs them, and every key having full tenant authority is simple to reason about. The cost is accepted and documented: a stolen key can mint more keys.
Alternative considered: users + roles now (a large model with no caller until the dashboard); scoped keys (useful, but no second scope exists yet).
Would flip if: the Phase 12 dashboard has multiple people in one account, or CI needs a read-only key.

## [2026-11-08] Env vars: AES-256-GCM, a versioned keyring, ciphertext bound to its row with AAD, write-only through the API
Why: the database is what's being protected against, so encryption happens in Go. Binding to tenant + project + key means ciphertext copied to another row fails to decrypt (verified, and the test goes red when the binding is removed). A version byte lets a new key be added while old rows stay readable. Values can't be read back, so one stolen key can't dump every secret. Only `Decrypted`, with no HTTP route, returns them.
Alternative considered: `pgcrypto` (sends plaintext and key to the database); envelope encryption with per-row data keys (needs a key store to justify it, planned with KMS in Phase 14); returning values on GET (one stolen key reads everything).
Would flip if: Phase 14 adds KMS, then envelope encryption. If users need to see non-secret config, add a per-variable flag, defaulting to write-only.

## [2026-11-08] Another tenant's resource gets the same 404 as a missing one; the tenant comes only from the credential
Why: 403 confirms an ID exists. `TestAnotherTenantsResourcesLookLikeTheyDoNotExist` compares the two responses byte for byte, so the difference can't creep back. No URL or body carries a tenant ID, and unknown JSON fields are a 400, so a caller has no way to name another tenant.
Alternative considered: 403 (leaks existence); tenant IDs in URLs (a second source of truth to check on every route).
Would flip if: resources can be shared across tenants, when knowing that something exists becomes legitimate.

## [2026-11-08] Keyset pagination with opaque, unsigned cursors, `(created_at, id)` or the natural unique key
Why: OFFSET reads and discards skipped rows and shifts when rows are inserted. Rows can share a timestamp, so `id` breaks ties (verified: the test goes red without it). Project names and env keys are unique in scope, so they are their own cursor. A cursor isn't signed because it grants nothing: it only positions a query already scoped to the caller's tenant. It's still decoded strictly, so bad values don't become 500s.
Alternative considered: OFFSET; signed cursors (a key to manage, protecting nothing); `(created_at)` alone (skips ties).
Would flip if: a cursor ever carries something the caller shouldn't choose, such as a tenant or a filter. Then sign it.
