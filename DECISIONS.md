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
