# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, handles failed tolerance decisions, and seals traceable
evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow freeze-review-packet
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

Unit and integration tests cover the workflows, the canonical review-packet
digest, tamper detection, and concurrent freeze idempotency:

```bash
go test ./... -race
```

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `POST /cases/{id}/review-packet` — freeze a stable review packet (201 on first freeze, 200 when the same sealed version was already frozen)
- `GET /cases/{id}/review-packet` — read the stored frozen packet
- `GET /cases/{id}/review-packet/verify` — recompute the digest and return the canonical manifest for independent verification
- `GET /cases/{id}/events`

## Review Packet Integrity

Only a sealed, evidence-complete case can be frozen. The packet is a value
snapshot of the case, device, seal-selected measurement, seal, and audit events
present at freeze time; later changes (including newly appended audit events)
never alter a stored packet, and the same case version always yields the same
packet and digest.

The digest is `sha256` over the canonical manifest: one compact JSON object
with `schema_version`, `digest_alg`, `scope`, `case`, `device`, `seal`,
`measurement`, and `events`, encoded with Go's `encoding/json` (HTML escaping
disabled), scope slices sorted, events sorted by id, with no trailing newline.
An external reviewer hashes `canonical_manifest` from the verify endpoint and
compares it with `stored_digest`; `verified: false` means the stored payload no
longer matches its digest.

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
