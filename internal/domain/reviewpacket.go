package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// ReviewPacketDigestAlgorithm names the hash used for packet integrity.
	ReviewPacketDigestAlgorithm = "sha256"
	// ReviewPacketDigestSchema versions the canonical digest layout so a later
	// standard change never silently rewrites an already frozen packet.
	ReviewPacketDigestSchema = "metrology.review-packet/v1"
)

// AuditScope bounds the audit trail captured inside a review packet. Events
// recorded after the freeze stay outside the scope and cannot be mistaken for
// packet content.
type AuditScope struct {
	EventCount   int       `json:"event_count"`
	FirstEventID string    `json:"first_event_id"`
	LastEventID  string    `json:"last_event_id"`
	ThroughAt    time.Time `json:"through_at"`
}

// ReviewPacket is the immutable, independently verifiable snapshot of a
// sealed case: the case at its sealed version, the seal-selected measurement,
// the evidence seal, and the bounded audit trail. The digest is a pure
// function of that evidence content, so regenerating the packet for the same
// case version always yields the same identity.
type ReviewPacket struct {
	ID              string           `json:"id"`
	CaseID          string           `json:"case_id"`
	CaseVersion     int              `json:"case_version"`
	GeneratedAt     time.Time        `json:"generated_at"`
	Case            VerificationCase `json:"case"`
	Measurement     Measurement      `json:"measurement"`
	Seal            EvidenceSeal     `json:"seal"`
	Events          []AuditEvent     `json:"events"`
	AuditScope      AuditScope       `json:"audit_scope"`
	DigestAlgorithm string           `json:"digest_algorithm"`
	DigestSchema    string           `json:"digest_schema"`
	Digest          string           `json:"digest"`
}

// FreezeReviewPacket builds the review packet for a sealed case. It refuses
// unsealed cases and any case whose seal, selected measurement, or audit
// trail is incomplete, so a partial record can never be mistaken for formal
// evidence.
func FreezeReviewPacket(item VerificationCase, measurement Measurement, seal EvidenceSeal, events []AuditEvent, at time.Time) (ReviewPacket, error) {
	if item.State != CaseSealed {
		return ReviewPacket{}, fmt.Errorf("%w: case %s is %s", ErrCaseNotSealed, item.ID, item.State)
	}
	if err := item.Validate(); err != nil {
		return ReviewPacket{}, err
	}
	if err := seal.Validate(); err != nil {
		return ReviewPacket{}, fmt.Errorf("%w: %v", ErrIncompleteEvidence, err)
	}
	if seal.CaseID != item.ID || seal.DeviceID != item.DeviceID {
		return ReviewPacket{}, fmt.Errorf("%w: seal %s does not belong to case %s", ErrIncompleteEvidence, seal.ID, item.ID)
	}
	if err := measurement.Validate(); err != nil {
		return ReviewPacket{}, fmt.Errorf("%w: %v", ErrIncompleteEvidence, err)
	}
	if measurement.ID != seal.MeasurementID || measurement.CaseID != item.ID || !measurement.Accepted {
		return ReviewPacket{}, fmt.Errorf("%w: seal %s does not select an accepted measurement of case %s", ErrIncompleteEvidence, seal.ID, item.ID)
	}
	frozen := append([]AuditEvent(nil), events...)
	if len(frozen) == 0 {
		return ReviewPacket{}, fmt.Errorf("%w: case %s has no audit trail", ErrIncompleteEvidence, item.ID)
	}
	sealEventFound := false
	for _, event := range frozen {
		if err := event.Validate(); err != nil {
			return ReviewPacket{}, fmt.Errorf("%w: %v", ErrIncompleteEvidence, err)
		}
		if event.CaseID != item.ID {
			return ReviewPacket{}, fmt.Errorf("%w: event %s does not belong to case %s", ErrIncompleteEvidence, event.ID, item.ID)
		}
		if event.Kind == "case_sealed" {
			sealEventFound = true
		}
	}
	if !sealEventFound {
		return ReviewPacket{}, fmt.Errorf("%w: case %s has no seal audit event", ErrIncompleteEvidence, item.ID)
	}
	last := frozen[len(frozen)-1]
	packet := ReviewPacket{
		ID:              fmt.Sprintf("packet-%s-v%d", item.ID, item.Version),
		CaseID:          item.ID,
		CaseVersion:     item.Version,
		GeneratedAt:     at.UTC(),
		Case:            item,
		Measurement:     measurement,
		Seal:            seal,
		Events:          frozen,
		AuditScope:      AuditScope{EventCount: len(frozen), FirstEventID: frozen[0].ID, LastEventID: last.ID, ThroughAt: last.CreatedAt},
		DigestAlgorithm: ReviewPacketDigestAlgorithm,
		DigestSchema:    ReviewPacketDigestSchema,
	}
	packet.Digest = ComputeReviewPacketDigest(item, measurement, seal, frozen)
	if err := packet.Validate(); err != nil {
		return ReviewPacket{}, err
	}
	return packet, nil
}

// reviewPacketContent is the canonical digest layout. Field order is fixed by
// declaration and encoding/json emits it deterministically, so any auditor
// can recompute the digest from the packet content alone.
type reviewPacketContent struct {
	Schema      string           `json:"schema"`
	Case        VerificationCase `json:"case"`
	Measurement Measurement      `json:"measurement"`
	Seal        EvidenceSeal     `json:"seal"`
	Events      []AuditEvent     `json:"events"`
}

// ComputeReviewPacketDigest deterministically recomputes the integrity digest
// from the sealed case version's evidence content.
func ComputeReviewPacketDigest(item VerificationCase, measurement Measurement, seal EvidenceSeal, events []AuditEvent) string {
	content := reviewPacketContent{
		Schema:      ReviewPacketDigestSchema,
		Case:        item,
		Measurement: measurement,
		Seal:        seal,
		Events:      events,
	}
	// The content holds only strings, numbers, booleans, times, and slices of
	// structs, so marshaling cannot fail.
	encoded, _ := json.Marshal(content)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// VerifyDigest reports whether the stored digest still matches the packet
// content, i.e. nothing was rewritten after the freeze.
func (p ReviewPacket) VerifyDigest() bool {
	if p.Digest == "" {
		return false
	}
	return p.Digest == ComputeReviewPacketDigest(p.Case, p.Measurement, p.Seal, p.Events)
}

func (p ReviewPacket) Validate() error {
	if p.ID == "" || p.CaseID == "" || p.Digest == "" || p.DigestAlgorithm == "" || p.DigestSchema == "" {
		return fmt.Errorf("%w: packet id, case id, digest, digest algorithm, and digest schema are required", ErrInvalidInput)
	}
	if strings.TrimSpace(p.CaseID) == "" || p.CaseVersion < 1 {
		return fmt.Errorf("%w: packet case id and version are required", ErrInvalidInput)
	}
	if p.GeneratedAt.IsZero() {
		return fmt.Errorf("%w: packet generation time is required", ErrInvalidInput)
	}
	if p.AuditScope.EventCount != len(p.Events) || p.AuditScope.EventCount == 0 {
		return fmt.Errorf("%w: packet audit scope does not match its events", ErrInvalidInput)
	}
	return nil
}
