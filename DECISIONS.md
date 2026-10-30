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
Why: a child row must never point at another tenant's parent. With `(tenant_id, project_id) REFERENCES projects (tenant_id, id)` the database checks the pair, so `tenant_id = B, project_id = <A's project>` is refused even if Go code has a bug (verified in `TestTenantIsolation`). Taking `tenantID` as a parameter makes forgetting the scope a compile error, not a leak.
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
