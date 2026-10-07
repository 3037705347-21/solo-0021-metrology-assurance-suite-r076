package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// PacketSchemaVersion identifies the canonical manifest format so that
	// external verifiers know exactly which byte layout the digest covers.
	PacketSchemaVersion = 1
	// PacketDigestAlgorithm is the only supported integrity algorithm.
	PacketDigestAlgorithm = "sha256"
)

// ReviewPacket is a frozen, independently verifiable bundle for one sealed
// case version. It is a value snapshot: later changes to the case,
// measurements, or standards cannot rewrite a stored packet.
type ReviewPacket struct {
	ID            string            `json:"id"`
	CaseID        string            `json:"case_id"`
	CaseVersion   int               `json:"case_version"`
	SchemaVersion int               `json:"schema_version"`
	DigestAlg     string            `json:"digest_alg"`
	Digest        string            `json:"digest"`
	Reviewer      string            `json:"reviewer"`
	GeneratedAt   time.Time         `json:"generated_at"`
	FrozenCase    VerificationCase  `json:"frozen_case"`
	FrozenDevice  Device            `json:"frozen_device"`
	FrozenSeal    EvidenceSeal      `json:"frozen_seal"`
	FrozenMeasure Measurement       `json:"frozen_measurement"`
	FrozenEvents  []AuditEvent      `json:"frozen_events"`
	Scope         ReviewPacketScope `json:"scope"`
}

// ReviewPacketScope declares exactly which evidence sections the packet
// covers, and is itself part of the digest.
type ReviewPacketScope struct {
	Sections       []string `json:"sections"`
	CaseID         string   `json:"case_id"`
	CaseVersion    int      `json:"case_version"`
	MeasurementIDs []string `json:"measurement_ids"`
	SealID         string   `json:"seal_id"`
	EventCount     int      `json:"event_count"`
}

// NewReviewPacket freezes one sealed case version into a review packet. The
// evidence is deep-copied, cross-checked for internal consistency, and bound
// together with a SHA-256 digest of its canonical manifest.
func NewReviewPacket(
	id string,
	reviewer string,
	at time.Time,
	item VerificationCase,
	device Device,
	seal EvidenceSeal,
	measurement Measurement,
	events []AuditEvent,
) (ReviewPacket, error) {
	if err := validatePacketEvidence(item, device, seal, measurement, events); err != nil {
		return ReviewPacket{}, err
	}

	frozenEvents := append([]AuditEvent(nil), events...)
	sort.SliceStable(frozenEvents, func(left, right int) bool {
		return frozenEvents[left].ID < frozenEvents[right].ID
	})

	stamp := at.UTC()
	packet := ReviewPacket{
		ID:            strings.TrimSpace(id),
		CaseID:        item.ID,
		CaseVersion:   item.Version,
		SchemaVersion: PacketSchemaVersion,
		DigestAlg:     PacketDigestAlgorithm,
		Reviewer:      strings.TrimSpace(reviewer),
		GeneratedAt:   stamp,
		FrozenCase:    item,
		FrozenDevice:  device,
		FrozenSeal:    seal,
		FrozenMeasure: measurement,
		FrozenEvents:  frozenEvents,
		Scope: ReviewPacketScope{
			Sections:       []string{"case", "device", "measurement", "seal", "audit_events"},
			CaseID:         item.ID,
			CaseVersion:    item.Version,
			MeasurementIDs: []string{measurement.ID},
			SealID:         seal.ID,
			EventCount:     len(frozenEvents),
		},
	}
	if packet.ID == "" || packet.Reviewer == "" {
		return ReviewPacket{}, fmt.Errorf("%w: packet id and reviewer are required", ErrInvalidInput)
	}
	if stamp.IsZero() {
		return ReviewPacket{}, fmt.Errorf("%w: packet generation time is required", ErrInvalidInput)
	}

	manifest, err := packet.canonicalManifest()
	if err != nil {
		return ReviewPacket{}, err
	}
	sum := sha256.Sum256(manifest)
	packet.Digest = hex.EncodeToString(sum[:])
	return packet, nil
}

// Validate checks that the packet is a complete, self-consistent evidence
// bundle with a well-formed digest. It does not recompute the digest; callers
// that need tamper detection use Verify.
func (p ReviewPacket) Validate() error {
	if p.ID == "" || p.CaseID == "" || p.Reviewer == "" {
		return fmt.Errorf("%w: packet id, case id, and reviewer are required", ErrInvalidInput)
	}
	if p.SchemaVersion != PacketSchemaVersion {
		return fmt.Errorf("%w: unsupported packet schema version %d", ErrInvalidInput, p.SchemaVersion)
	}
	if p.DigestAlg != PacketDigestAlgorithm {
		return fmt.Errorf("%w: unsupported digest algorithm %q", ErrInvalidInput, p.DigestAlg)
	}
	if len(p.Digest) != sha256.Size*2 {
		return fmt.Errorf("%w: digest must be a %d-character hex string", ErrInvalidInput, sha256.Size*2)
	}
	if _, err := hex.DecodeString(p.Digest); err != nil {
		return fmt.Errorf("%w: digest is not valid hex", ErrInvalidInput)
	}
	if p.GeneratedAt.IsZero() {
		return fmt.Errorf("%w: packet generation time is required", ErrInvalidInput)
	}
	return validatePacketEvidence(p.FrozenCase, p.FrozenDevice, p.FrozenSeal, p.FrozenMeasure, p.FrozenEvents)
}

// Verify recomputes the canonical digest from the frozen payload and reports
// whether it matches the stored digest. A false result with no error means
// tampering or corruption of the packet payload.
func (p ReviewPacket) Verify() (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	manifest, err := p.canonicalManifest()
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(manifest)
	return hex.EncodeToString(sum[:]) == p.Digest, nil
}

// CanonicalManifest returns the exact bytes hashed into Digest, so external
// reviewers can recompute the digest without depending on the Go types.
func (p ReviewPacket) CanonicalManifest() ([]byte, error) {
	return p.canonicalManifest()
}

type canonicalPacket struct {
	SchemaVersion int               `json:"schema_version"`
	DigestAlg     string            `json:"digest_alg"`
	Scope         ReviewPacketScope `json:"scope"`
	Case          VerificationCase  `json:"case"`
	Device        Device            `json:"device"`
	Seal          EvidenceSeal      `json:"seal"`
	Measurement   Measurement       `json:"measurement"`
	Events        []AuditEvent      `json:"events"`
}

func (p ReviewPacket) canonicalManifest() ([]byte, error) {
	measurementIDs := append([]string(nil), p.Scope.MeasurementIDs...)
	sort.Strings(measurementIDs)
	sections := append([]string(nil), p.Scope.Sections...)
	sort.Strings(sections)
	scope := p.Scope
	scope.Sections = sections
	scope.MeasurementIDs = measurementIDs

	document := canonicalPacket{
		SchemaVersion: PacketSchemaVersion,
		DigestAlg:     PacketDigestAlgorithm,
		Scope:         scope,
		Case:          p.FrozenCase,
		Device:        p.FrozenDevice,
		Seal:          p.FrozenSeal,
		Measurement:   p.FrozenMeasure,
		Events:        p.FrozenEvents,
	}

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("%w: cannot encode packet manifest: %v", ErrInvalidInput, err)
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

func validatePacketEvidence(
	item VerificationCase,
	device Device,
	seal EvidenceSeal,
	measurement Measurement,
	events []AuditEvent,
) error {
	if item.State != CaseSealed {
		return fmt.Errorf("%w: case %s is %s, only a sealed case can be frozen", ErrStateConflict, item.ID, item.State)
	}
	if device.ID == "" || item.DeviceID != device.ID {
		return fmt.Errorf("%w: packet device does not belong to case %s", ErrInvalidInput, item.ID)
	}
	if seal.CaseID != item.ID {
		return fmt.Errorf("%w: seal %s does not belong to case %s", ErrInvalidInput, seal.ID, item.ID)
	}
	if seal.DeviceID != item.DeviceID {
		return fmt.Errorf("%w: seal %s references device %s, expected %s", ErrInvalidInput, seal.ID, seal.DeviceID, item.DeviceID)
	}
	if seal.MeasurementID != measurement.ID {
		return fmt.Errorf("%w: seal %s selects measurement %s, but the packet carries %s", ErrInvalidInput, seal.ID, seal.MeasurementID, measurement.ID)
	}
	if measurement.CaseID != item.ID {
		return fmt.Errorf("%w: measurement %s does not belong to case %s", ErrInvalidInput, measurement.ID, item.ID)
	}
	if !measurement.Accepted {
		return fmt.Errorf("%w: selected measurement %s is not accepted", ErrInvalidInput, measurement.ID)
	}
	if err := item.Validate(); err != nil {
		return err
	}
	if err := device.Validate(); err != nil {
		return err
	}
	if err := seal.Validate(); err != nil {
		return err
	}
	if err := measurement.Validate(); err != nil {
		return err
	}
	for _, event := range events {
		if event.CaseID != item.ID {
			return fmt.Errorf("%w: event %s does not belong to case %s", ErrInvalidInput, event.ID, item.ID)
		}
		if err := event.Validate(); err != nil {
			return err
		}
	}
	return nil
}
