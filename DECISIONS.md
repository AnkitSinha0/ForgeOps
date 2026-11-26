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

## [2026-11-11] [security] Migrations take a per-schema advisory lock before anything else, including creating `schema_migrations`
Tags: security, severity:medium, finding:M1, area:migrations
Why: the audit started 6 controlplanes at once against a fresh schema and 5 of 6 crashed with `23505 pg_type_typname_nsp_index`. `CREATE TABLE IF NOT EXISTS` isn't safe under concurrency in Postgres, and it ran before, and outside, the `LOCK TABLE` meant to serialise boots. A table lock can't guard the creation of its own table, so the lock is now `pg_advisory_xact_lock(const, hashtext(current_schema()))` as the first statement of the one migration transaction. The `_xact_` form is released on commit or rollback, so a crashed boot can't leave it held. `TestConcurrentFirstBootAllSucceed` was red before the fix (6 replicas, 3 rounds) and is green now.
Alternative considered: keep `LOCK TABLE` and retry on 23505/42P07 (papers over the race and needs a retry budget); create `schema_migrations` in the Dockerfile or an init job (a second place the schema lives).
Would flip if: migrations move to a separate one-shot job (a Kubernetes Job or an init container) that is guaranteed to run alone. The lock would then cost nothing but protect nothing either.

## [2026-11-11] [security] The `go` directive pins a patched toolchain, and govulncheck counts as a test
Tags: security, severity:medium, finding:M2, area:dependencies
Why: govulncheck reported 15 vulnerabilities the code actually reaches: 13 in the Go 1.26.4 standard library (several HTTP/2 server DoS bugs in `net/http`, plus `crypto/tls` and `crypto/x509`) and 2 in `golang.org/x/text` v0.29.0 (an infinite loop and a panic, pulled in through GORM and pgx). `go 1.26.10` in go.mod makes the toolchain refuse to build with an older Go, and GOTOOLCHAIN=auto downloads it. x/text went to v0.41.0. The result is 0 reachable vulnerabilities. CLAUDE.md said "match HashVault, 1.26.4", but matching an unpatched version isn't a reason to stay on it.
Alternative considered: a `toolchain` line only (it doesn't stop a contributor building with 1.26.4 under GOTOOLCHAIN=local); waiting for Phase 14's CI (that leaves months on known HTTP/2 crashes).
Would flip if: never back down. Phase 14's CI runs govulncheck on every push and fails on any reachable finding.

## [2026-11-11] [security] Env var names: a denylist of platform-owned names, not an allowlist
Tags: security, severity:medium, finding:M3, area:env-vars
Why: a tenant could set `LD_PRELOAD`, `PATH` or `FORGEOPS_*`. In Phases 4-5 those reach a real process. The loader's variables (`LD_*`, `DYLD_*`) decide what code runs before `main`. `PORT` and `FORGEOPS_*` belong to the platform, so a tenant value would either be overwritten silently or, if the executor ever applied tenant values last, override it. Names are compared in uppercase. This protects the tenant's own container from surprises and keeps the platform's names the platform's. It isn't the isolation boundary; Phase 6's sandbox is.
Alternative considered: an allowlist (every app has its own names, so it can't be written); blocking `NODE_OPTIONS` and similar (they only affect the tenant's own process, and blocking them breaks real apps).
Would flip if: the executor injects variables through a mechanism with namespacing (a mounted file the app reads, say). Then names stop colliding and the denylist can shrink.

## [2026-11-11] [security] Env var count and total size are capped, checked under a lock on the project row
Tags: security, severity:medium, finding:M4, area:env-vars
Why: only single values were capped (64 KiB). The audit stored 300 x 60 KB, 18 MB in one project: far past what `execve` and Docker accept, and `Decrypted` loads it all at once. The limits are 200 variables and 512 KiB of stored bytes per project, sized against execve's combined limit for arguments and environment. `SetWithin` locks the project row (`SELECT ... FOR UPDATE`, tenant-scoped) before counting, so two concurrent writes can't both see 199 and both insert. Replacing a variable counts only its new size.
Alternative considered: count without a lock (racy); a trigger enforcing the limits in Postgres (moves policy into SQL, harder to test and to message); a counter column on projects (the drifting-counter problem DECISIONS already rejected for layers).
Would flip if: Phase 9's quotas make limits per plan. The numbers then come from the tenant's plan, and the lock and count stay as they are.

## [2026-11-11] [security] A hostname has one canonical spelling: ASCII LDH, lowercase, no trailing dot, checked in Go and by a CHECK
Tags: security, severity:medium (latent until the domains API), finding:M5, area:domains
Why: domains are claimed globally through a unique index, and a unique index compares strings, not hosts. `shop.example.com.` (trailing dot) could be claimed next to another tenant's `shop.example.com`. `strings.ToLower` folds U+212A (Kelvin) to `k` and U+0130 to `i`, which Postgres `lower()` may not, so the two layers disagreed. `*.com`, `a b` and `""` were stored. `hostnames.Canonical` now takes ASCII only (international names arrive as punycode), applies label and length rules, rejects IPv4 literals, single labels and `localhost`, and lowercases. Migration 0003 adds the same shape as a CHECK, so a code path that skips Go still can't store a second spelling. The check sits in the repository because it's what makes the index mean "one host".
Alternative considered: normalise with IDNA (`golang.org/x/net/idna`) and accept Unicode input (more surface, and the router and the ACME code would then have to agree on normalisation too); validate only in a future domain service (the repository could still be called without it).
Would flip if: a dashboard wants Unicode input. Convert to punycode at the edge, and keep storage ASCII.

## [2026-11-11] [security] Only a succeeded build with an image can be deployed; failure reasons are cleaned, never refused
Tags: security, severity:low, finding:L1, area:builds
Why: `MarkSucceeded(id, "")` stored `succeeded` with no image, and a deployment could be created from a queued or failed build. Both now return ErrInvalid, and migration 0003 adds `status <> 'succeeded' OR image_ref <> ''`. The status check before the insert can't race, because `succeeded` is terminal. A failure reason is the opposite case: refusing one would leave the build stuck in `building` forever. So it's cleaned instead: NUL bytes dropped (Postgres text can't hold them; it was a raw 22021 error) and the rest cut to 4096 bytes on a UTF-8 boundary, with a CHECK as the backstop.
Alternative considered: reject long or NUL reasons (a failure that can't be recorded is worse than a truncated one); check build status in the Phase 5 service only (the repository could still be called around it).
Would flip if: build logs move to object storage (Phase 8). Then `error` becomes a short summary plus a pointer, and the cap can shrink.

## [2026-11-11] [security] One spelling per request: canonical IDs only, exactly one Authorization header, unknown fields echoed only when short
Tags: security, severity:low, finding:L4 L5 L6, area:http
Why: three places took input in more than one form. `uuid.Parse` accepted uppercase, `{braces}`, `urn:uuid:` and no hyphens, so one project had five URLs, and anything keyed on the path (a rate limit, a cache) could be sidestepped. Only `id.String() == text` resolves now. `Header.Get` read the first of several Authorization headers, while a proxy in front may read another or join them, so two headers are now a 401. The unknown-field error quoted the caller's field name in full (a 900 KB name came back as a 900 KB error). Names over 64 characters are no longer quoted, and short ones like `tenant_id` still are, because that helps a client fix the request.
Alternative considered: redirect non-canonical IDs (still two spellings reach the handler); drop field names from errors entirely (worse for real clients).
Would flip if: an API client the platform must support sends uppercase UUIDs. Then normalise in one middleware and still key everything on the canonical form.

## [2026-11-11] [security] Dev infrastructure listens on localhost only, and production refuses the dev shortcuts
Tags: security, severity:low, finding:L10 L11, area:deploy config
Why: compose published Postgres, Redis and MinIO on every interface (`0.0.0.0` and `[::]`), with credentials committed in the file and no Redis password. On a laptop on public Wi-Fi, or any cloud VM, the database was open to the network. Every port is now `127.0.0.1:` (verified with `docker ps` after recreating the stack). `config.Load` also refuses, with `APP_ENV=production`, `sslmode=disable` and an empty `REDIS_PASSWORD`, and range-checks the pool settings (0 open connections means "unlimited" to database/sql, not "none").
Alternative considered: a Redis password in dev too (every contributor's .env changes, for no gain once the port is local-only).
Would flip if: the dev stack has to be reachable from another machine (a phone, a second PC). Then add a Redis password and keep the database on localhost behind an SSH tunnel.

## [2026-11-11] [security] Audit findings accepted for now, each with the phase that owns it
Tags: security, severity:low/info, area:risk-register
Why: these were found, are real, and are deliberately not fixed in Phase 2, because the right fix depends on something a later phase builds:
- API keys never expire, and there's no rate limit on failed auth and no cap on keys per tenant. Owned by Phase 9 (quotas). Guessing a 256-bit key is infeasible, so the risk is abuse and noise, not takeover.
- An old ciphertext restored by someone with database write access still decrypts, because the AAD binds tenant, project and key but no version. Copying across rows is already blocked. That attacker already holds the database, and the fix (a version in the AAD) belongs with KMS envelope encryption in Phase 14.
- The global `build_layers` table: the first tenant to register a digest fixes its size, `created` tells a tenant that someone else already has a layer, and attaching only needs the digest. Owned by Phase 7 (proof of possession and server-computed sizes; already in CLAUDE.md's Phase 7 notes).
- `/readyz` keeps answering ready for a moment after SIGTERM. Owned by Phase 5, when a load balancer first depends on it.
Would flip if: any of these becomes reachable earlier than its phase, for example a public API for layers before Phase 7.

## [2026-11-26] Services never call each other: they communicate through Redis (queue, registry, pub/sub) and Postgres (state), not gRPC
Tags: architecture, area:inter-service. Planned at project start; built in Phases 4, 5 and 8.
Why: the three services have very different jobs and lifetimes, and a direct call would couple them where it hurts most:
- controlplane → executor is a **build queue**. Builds take minutes and executors may be busy or down, so a push must be recorded and wait, not fail. Executors take work when they have capacity, which gives backpressure for free.
- The executor reports results by writing build status and image to **Postgres** with the conditional `UPDATE`s from Phase 1, so every service reads state from the same source of truth.
- Build logs go out on **Redis pub/sub** and the controlplane fans them out over SSE (Phase 8). Any number of viewers, and the executor doesn't know who is watching.
- controlplane → router is the **Redis backend registry** plus a pub/sub message to refresh it (Phase 5). Every router instance gets the update, with no restart and no list of routers to call.
- It keeps the router least-privileged: it reads one Redis registry and can't reach the controlplane or Postgres (the 2026-09-27 entry).
- No `.proto` files, generated code, service discovery or TLS between services. Redis and Postgres run anyway.
Alternative considered: gRPC between services. It's good for typed request/response and streaming between known peers, but a build isn't a request/response: the caller would need retries when no executor is free, and a list of every router to notify. Plain REST calls between services have the same problems, with no types.
Would flip if: something needs an immediate answer from one specific instance. The first candidate is cancelling a running build: it can still go through Redis (a cancel key the executor checks, or a pub/sub message), but if that turns out too slow or unreliable, add a direct gRPC channel to executors for that one operation. Executors running on other machines (Phase 14) don't change this on their own: a shared queue already works across machines.

## [2026-11-26] The build queue uses Redis, not RabbitMQ (or a Postgres-backed queue)
Tags: architecture, area:queue. Planned at project start; built in Phase 4.
Why: ForgeOps already needs Redis for the router registry, the status cache and log pub/sub. Using it for the queue too means one less stateful service, and that matters for a product whose promise is that anyone can self-host it. The queue isn't the source of truth: a build row in Postgres is created as `queued` first, and the queue message only tells an executor to pick it up. So if Redis loses a message (it's an in-memory store, and with AOF `appendfsync everysec` a crash can lose about the last second), a sweep can re-enqueue builds that have been `queued` too long, and the executor's conditional `UPDATE` from queued to building stops a duplicate from running twice. Plain `LPOP` isn't enough: a job is lost if the executor crashes after taking it. Phase 4 must use a reliable pattern, either Redis Streams with consumer groups (acks, a pending list, `XAUTOCLAIM` to take over a dead consumer's jobs) or `BLMOVE` into a processing list.
Alternative considered:
- **RabbitMQ** is a real message broker. It acks and redelivers messages and has dead-letter queues, TTLs, routing and publisher confirms out of the box, and writes messages to disk. That's more delivery guarantee than Redis, but it adds a second stateful service to run, monitor and secure, for one queue with one kind of consumer. HashVault uses RabbitMQ for its email queue, and its A21 finding (the worker and publisher never reconnect after a broker restart) shows the extra operational work is real. Most of what RabbitMQ adds is already covered here: Postgres keeps the build state, and Streams provide acks and redelivery.
- **A Postgres-backed queue** (`SELECT … FOR UPDATE SKIP LOCKED`) is strong: transactional with the build row, so an enqueue can't be lost, and no new service. It was rejected for now because executors would poll the main database, and `SKIP LOCKED` is on the Phase 11b portability audit list. It's the best fallback if Redis delivery ever causes trouble.
Would flip if: the queue grows into several job types with retry and backoff rules, dead-lettering, priorities or routing between consumer groups (then RabbitMQ, or NATS JetStream, earns its place); or lost or duplicated jobs show up in practice despite the sweep (then the Postgres `SKIP LOCKED` queue, which makes enqueueing part of the same transaction as the build row).
