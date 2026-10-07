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
- ReviewPacket: the frozen, independently verifiable snapshot of a sealed case,
  bundling the case at its sealed version, the seal-selected measurement, the
  evidence seal, and the bounded audit scope, plus a deterministic integrity
  digest.

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

A reviewer sends `POST /cases/{id}/review-packet` for a sealed case. The
service freezes a review packet containing the case at its sealed version, the
seal-selected measurement, the evidence seal, and the audit trail as a bounded
scope, and stamps it with a SHA-256 digest over the canonical content. The
packet is created at most once per case: a repeat request returns the already
frozen packet unchanged (200 instead of 201), so the same case version can
never yield contradictory packets. `GET /cases/{id}/review-packet` reads the
frozen packet. Unsealed cases are rejected with `case_not_sealed`, and a case
whose seal, selected measurement, or audit trail is incomplete is rejected
with `incomplete_evidence`, so a partial record can never be mistaken for
formal evidence.

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
- A review packet can be frozen only for a sealed case with a complete seal,
  selected measurement, and audit trail.
- A case has at most one review packet; packets are append-only and are never
  rewritten after the freeze.
- A packet digest is a deterministic function of the sealed case version's
  evidence content, so regenerating for the same case version reproduces the
  same packet identity and digest.

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
- `GET /cases/{id}/events`
- `POST /cases/{id}/review-packet`
- `GET /cases/{id}/review-packet`

## Review Packet Integrity

The packet digest is the lowercase hex SHA-256 of the canonical JSON object
`{"schema":"metrology.review-packet/v1","case":...,"measurement":...,"seal":...,"events":[...]}`,
serialized with Go `encoding/json` semantics (struct field declaration order,
shortest round-trip floats, RFC3339Nano timestamps). The digest covers only
the evidence content, not the freeze time, so it is a pure function of the
sealed case version. Any auditor can recompute it from the packet alone; the
`audit_scope` block bounds the included audit trail so later additions to the
live trail are visibly outside the frozen scope. The digest schema is
versioned: a future standard change produces a new schema marker and never
rewrites already frozen packets.

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
