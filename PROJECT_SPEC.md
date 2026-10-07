# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, requests a
repeat measurement when tolerance is exceeded, and seals the final evidence
record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, and timestamp.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement.
- AuditEvent: an append-only trace of meaningful case and device changes.
- ReviewPacket: a frozen, digest-verifiable bundle for one exact sealed case
  version, covering the case, device, selected measurement, seal, and the audit
  events present at freeze time.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, tolerance, and
owner. The service verifies that the device exists, is active, and has no
unfinished case. A valid request creates a case in `awaiting_measurement`.

### 3. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement`. The service computes the signed deviation from the
nominal value. An in-tolerance measurement moves the case to
`review_ready`; an out-of-tolerance measurement moves it to
`recheck_required`.

### 4. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail.

### 5. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, creates exactly one evidence
seal, moves the case to `sealed`, and rejects later mutations.

### 6. Freeze Review Packet

After sealing, a reviewer sends `POST /cases/{id}/review-packet` with the
reviewer identity. The service freezes one stable packet for the exact sealed
case version. The packet contains the case, its device, the measurement
selected by the seal, the evidence seal, and every audit event that existed
for the case at freeze time. A canonical manifest of those sections is hashed
with SHA-256; the digest, digest algorithm, schema version, and scope are
stored with the packet.

Unsealed or incomplete cases (missing seal, missing selected measurement, or
cross-case references) are rejected and can never produce a packet that looks
like formal evidence. Repeating the request for the same case version returns
the same stored packet (HTTP 200) instead of creating a second one. The
freeze action appends a `review_packet_frozen` audit event after the snapshot,
so later retroactive entries never enter an already frozen packet.

`GET /cases/{id}/review-packet` reads the stored snapshot; live data never
replaces the frozen payload. `GET /cases/{id}/review-packet/verify` recomputes
the digest and returns the stored digest, computed digest, verification result,
and the exact canonical manifest bytes so an external reviewer can hash the
same content independently.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.
- Only a sealed, evidence-complete case can be frozen into a review packet.
- A review packet is bound to one case version: repeating the freeze for that
  version returns the identical stored packet and identical digest.
- A frozen packet is an immutable snapshot; later case, standard, or audit data
  cannot rewrite it.
- A packet's digest is SHA-256 over a canonical manifest of its declared scope
  and must recompute to the same value on verification.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `POST /cases/{id}/review-packet`
- `GET /cases/{id}/review-packet`
- `GET /cases/{id}/review-packet/verify`
- `GET /cases/{id}/events`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal. The review-packet check freezes a sealed case, proves that a repeated
freeze returns the same packet and digest, recomputes the digest from the
canonical manifest with an external hash, proves the freeze audit event stays
out of the frozen snapshot, and rejects unsealed or missing packets.

Domain and service unit tests cover canonical digest determinism, tamper
detection, evidence-completeness guards, and idempotent packet creation under
concurrency (`go test ./... -race`).
