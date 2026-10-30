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
