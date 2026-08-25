# VesselTrial

VesselTrial is a production-oriented collaboration backend for the CCS class
review and canal trial of green intelligent demonstration vessels. The initial
domain model follows the operational needs around "Beigang Yunhe 001": class
review, trial planning, voyage-leg release, vessel-to-shore telemetry, defect
disposition, and delivery-batch release.

The repository is a backend-only Go service. It does not include task branches,
private evaluation tests, known defects, or reference fixes.

## Runtime

- Go 1.26.1, with `go.mod` declaring Go 1.26.0
- SQLite through the pure-Go `modernc.org/sqlite` driver
- `CGO_ENABLED=0` and `GOTOOLCHAIN=local`
- HTTP server entry point: `./cmd/server`
- Container entry point: `/app/vesseltrial`
- Default listen address: `:8080`

Start locally:

```sh
cp .env.example .env
GOTOOLCHAIN=local CGO_ENABLED=0 go run ./cmd/server
```

The process applies ordered migrations before it starts accepting traffic. A
history conflict, unknown future migration, failed integrity check, or database
connection failure prevents readiness and startup. SQLite uses foreign keys,
WAL mode, and a bounded busy timeout.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VESSELTRIAL_ADDR` | `:8080` | HTTP listen address |
| `VESSELTRIAL_DB` | `vesseltrial.db` | SQLite database path |
| `VESSELTRIAL_SESSION_TTL` | `8h` | Opaque bearer session lifetime |
| `VESSELTRIAL_WORKER_INTERVAL` | `5s` | Durable worker poll and periodic-job interval |
| `VESSELTRIAL_SHUTDOWN_TIMEOUT` | `10s` | Graceful HTTP shutdown deadline |
| `VESSELTRIAL_MAX_REQUEST_BYTES` | `1048576` | Maximum JSON request body |

Do not commit production credentials or database files. Passwords are stored as
bcrypt hashes and bearer tokens are stored only as SHA-256 hashes. Logout
revokes one server-side session; other sessions remain valid until revoked or
expired.

## Business Roles

- `class_surveyor` decides submitted CCS class reviews.
- `trial_coordinator` registers vessels and manages plans and voyage legs.
- `shore_operator` registers shore stations, ingests telemetry, and opens trial
  defects.
- `quality_manager` contains, verifies, and closes defects, then releases a
  delivery batch after all gates pass.

Authorization is checked by the service layer in addition to route-level
authentication. This keeps worker, tests, and future non-HTTP callers subject
to the same business rules.

## Core State Flows

Class review:

```text
draft -> submitted -> approved
                   -> rejected
```

Only an approved review for the same vessel can back a trial plan. Review
submission is idempotent within actor, HTTP method, and route scope. Decisions
use conditional version updates so stale surveyors cannot overwrite a newer
decision.

Trial plan:

```text
draft -> scheduled -> active -> completed
                     \-> cancelled (reserved lifecycle state)
```

A plan must contain at least one non-overlapping leg before scheduling. Releasing
a leg checks all active time windows for the vessel in the same transaction.
Completing the last leg completes its plan.

Voyage leg:

```text
planned -> released -> underway -> completed
   ^          |
   +-- held <-+
```

Telemetry associated with a leg is accepted only while the leg belongs to the
same vessel and is released or underway. The sample insert and shore-station
freshness update commit atomically. A canceled context leaves neither half.

Defect disposition:

```text
open -> contained -> verified -> closed
```

Each transition records a durable action and correlated audit event in the same
transaction. A delivery batch cannot pass its quality gate until at least one
trial plan is complete and every defect for the vessel is closed.

Delivery batch:

```text
preparing -> quality_gate -> released -> delivered
```

The API can move directly from `preparing` to `released` when the caller asks
for release and every gate passes. The transition, actor, timestamp, and audit
event are atomic.

## Persistence

The first migration creates these related tables:

- `users`, `sessions`
- `vessels`, `class_reviews`, `review_decisions`
- `trial_plans`, `voyage_legs`
- `shore_stations`, `telemetry_samples`
- `defects`, `defect_actions`
- `delivery_batches`
- `worker_jobs`, `idempotency_keys`, `audit_events`

Foreign keys, unique constraints, status checks, version columns, and targeted
indexes enforce durable invariants alongside service transactions. Integration
tests use temporary real SQLite databases and explicitly close and reopen a
database to verify recovery.

## Durable Worker

The server ensures a `telemetry_staleness_check` job exists before serving. A
worker claims eligible jobs with a lease, increments the attempt counter, and
propagates its cancellable context to storage. Failures use bounded exponential
backoff and become permanent after `max_attempts`. Successful telemetry checks
atomically reset and reschedule the same periodic job. Other successful jobs
move to `succeeded`.

SIGINT or SIGTERM cancels the worker and starts HTTP graceful shutdown. The
worker does not use random sleeps or online services.

## HTTP Contract

Unauthenticated endpoints:

- `GET /healthz` checks process liveness.
- `GET /readyz` checks the database dependency.
- `POST /v1/auth/register` creates one of the four business identities.
- `POST /v1/auth/login` creates an expiring opaque session.

Authenticated commands:

- `POST /v1/auth/logout`
- `POST /v1/vessels`
- `POST /v1/reviews`
- `POST /v1/reviews/{id}/submit`
- `POST /v1/reviews/{id}/decision`
- `POST /v1/plans`
- `POST /v1/plans/{id}/legs`
- `POST /v1/plans/{id}/schedule`
- `POST /v1/legs/{id}/release`
- `POST /v1/legs/{id}/complete`
- `POST /v1/stations`
- `POST /v1/telemetry`
- `POST /v1/defects`
- `POST /v1/defects/{id}/transition`
- `POST /v1/delivery-batches`
- `POST /v1/delivery-batches/{id}/gate?release=true`

Authenticated queries:

- `GET /v1/vessels/{id}/legs?status=&limit=&offset=`
- `GET /v1/vessels/{id}/telemetry?since=&limit=&offset=`
- `GET /v1/vessels/{id}/defects?status=&severity=&limit=&offset=`

Versioned commands require the current positive version in `If-Match`.
Submission idempotency accepts `Idempotency-Key`. JSON parsing rejects unknown
fields, multiple values, and oversized bodies.

Every response includes `X-Request-ID`. Errors use a stable JSON form:

```json
{
  "error": {
    "code": "stale_review",
    "message": "review was changed by another operator",
    "request_id": "shore-gateway-42"
  }
}
```

## Verification

Run all foundation gates from the repository root:

```sh
GOTOOLCHAIN=local CGO_ENABLED=0 go test ./... -count=1
GOTOOLCHAIN=local CGO_ENABLED=0 go test -race ./... -count=1
GOTOOLCHAIN=local CGO_ENABLED=0 go vet ./...
GOTOOLCHAIN=local CGO_ENABLED=0 go build ./...
```

The tests cover migration and restart recovery, authentication expiry and
revocation, role authorization, transaction rollback, optimistic conflicts,
deterministic concurrent release, telemetry context cancellation, filtering and
pagination, defect ordering, delivery gates, worker retry/permanent failure,
and HTTP errors and request correlation.

## Container

The multi-stage Dockerfile compiles inside the official Go image. It uses
BuildKit's target platform values and does not copy a host binary or hard-code
one CPU architecture.

```sh
docker build --platform linux/amd64 -t vesseltrial:amd64 .
docker build --platform linux/arm64 -t vesseltrial:arm64 .
```

Each image starts the same `/app/vesseltrial` entry point and stores SQLite data
under `/data`. Map container port 8080 to an available host port, then check both
`/healthz` and `/readyz` on that exact mapping.
