package domain

import (
	"errors"
	"testing"
	"time"
)

func packetFixtureTime() time.Time {
	return time.Date(2026, time.October, 7, 9, 30, 0, 0, time.UTC)
}

func packetFixture(t *testing.T, state CaseState) (VerificationCase, Device, EvidenceSeal, Measurement, []AuditEvent) {
	t.Helper()
	at := packetFixtureTime()
	device, err := NewDevice("device-0001", " met-2001 ", "Bench Meter", "Room A", "Lin", at)
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	item, err := NewVerificationCase("case-0001", device.ID, 25.0, 0.5, "Qiao", at)
	if err != nil {
		t.Fatalf("case: %v", err)
	}
	measurement, err := NewMeasurement("measurement-0001", item.ID, 25.2, item.Nominal, item.Tolerance, "Rui", at.Add(time.Minute))
	if err != nil {
		t.Fatalf("measurement: %v", err)
	}
	if state == CaseSealed {
		if err := item.RecordMeasurement(measurement.Accepted, at.Add(time.Minute)); err != nil {
			t.Fatalf("record: %v", err)
		}
		if err := item.Seal(at.Add(2 * time.Minute)); err != nil {
			t.Fatalf("seal transition: %v", err)
		}
	}
	seal, err := NewEvidenceSeal("seal-0001", item.ID, device.ID, measurement.ID, "He", "ready for external review", at.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	events := []AuditEvent{
		mustEvent(t, "event-0003", "case", item.ID, item.ID, "case_sealed", "He", "case sealed", at.Add(3*time.Minute)),
		mustEvent(t, "event-0001", "case", item.ID, item.ID, "case_opened", "Qiao", "case opened", at),
		mustEvent(t, "event-0002", "case", item.ID, item.ID, "measurement_accepted", "Rui", "measurement accepted", at.Add(time.Minute)),
	}
	return item, device, seal, measurement, events
}

func mustEvent(t *testing.T, id, subjectType, subjectID, caseID, kind, actor, detail string, at time.Time) AuditEvent {
	t.Helper()
	event, err := NewAuditEvent(id, subjectType, subjectID, caseID, kind, actor, detail, at)
	if err != nil {
		t.Fatalf("event %s: %v", id, err)
	}
	return event
}

func TestNewReviewPacketFreezesSealedCaseWithStableDigest(t *testing.T) {
	item, device, seal, measurement, events := packetFixture(t, CaseSealed)
	at := packetFixtureTime().Add(4 * time.Minute)

	first, err := NewReviewPacket("packet-0001", "Auditor-An", at, item, device, seal, measurement, events)
	if err != nil {
		t.Fatalf("first packet: %v", err)
	}
	if first.DigestAlg != PacketDigestAlgorithm || len(first.Digest) != 64 {
		t.Fatalf("unexpected digest %q (%q)", first.Digest, first.DigestAlg)
	}
	if first.CaseVersion != item.Version {
		t.Fatalf("case version = %d, expected %d", first.CaseVersion, item.Version)
	}
	// Events arrive out of order in the fixture; the frozen order must be the
	// stable, deterministic order covered by the digest.
	if first.FrozenEvents[0].ID != "event-0001" {
		t.Fatalf("events were not ordered deterministically: %s first", first.FrozenEvents[0].ID)
	}

	// Different packet id, reviewer, and generation time must not change the
	// evidence digest: the digest covers only the sealed case version.
	later, err := NewReviewPacket("packet-9999", "Someone Else", at.Add(time.Hour), item, device, seal, measurement, events)
	if err != nil {
		t.Fatalf("second packet: %v", err)
	}
	if later.Digest != first.Digest {
		t.Fatalf("same case version produced different digests: %q vs %q", later.Digest, first.Digest)
	}
	manifest, err := first.CanonicalManifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	secondManifest, err := later.CanonicalManifest()
	if err != nil {
		t.Fatalf("second manifest: %v", err)
	}
	if string(manifest) != string(secondManifest) {
		t.Fatalf("canonical manifests differ for the same case version")
	}
	verified, err := first.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verified {
		t.Fatal("fresh packet failed its own digest check")
	}
}

func TestNewReviewPacketRejectsUnsealedCase(t *testing.T) {
	item, device, seal, measurement, events := packetFixture(t, CaseReviewReady)
	_, err := NewReviewPacket("packet-0002", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, events)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state conflict, got %v", err)
	}
}

func TestNewReviewPacketRejectsCrossCaseReferences(t *testing.T) {
	item, device, seal, measurement, events := packetFixture(t, CaseSealed)

	otherDevice := device
	otherDevice.ID = "device-9999"
	if _, err := NewReviewPacket("p", "Auditor-An", packetFixtureTime(), item, otherDevice, seal, measurement, events); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign device: expected invalid input, got %v", err)
	}

	foreignSeal := seal
	foreignSeal.CaseID = "case-9999"
	if _, err := NewReviewPacket("p", "Auditor-An", packetFixtureTime(), item, device, foreignSeal, measurement, events); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign seal: expected invalid input, got %v", err)
	}

	foreignMeasurement := measurement
	foreignMeasurement.ID = "measurement-9999"
	if _, err := NewReviewPacket("p", "Auditor-An", packetFixtureTime(), item, device, seal, foreignMeasurement, events); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("seal/measurement mismatch: expected invalid input, got %v", err)
	}

	rejected := measurement
	rejected.Accepted = false
	if _, err := NewReviewPacket("p", "Auditor-An", packetFixtureTime(), item, device, seal, rejected, events); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rejected measurement: expected invalid input, got %v", err)
	}

	foreignEvent := events[0]
	foreignEvent.CaseID = "case-9999"
	if _, err := NewReviewPacket("p", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, []AuditEvent{foreignEvent}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign event: expected invalid input, got %v", err)
	}
}

func TestReviewPacketDetectsTamperedPayload(t *testing.T) {
	item, device, seal, measurement, events := packetFixture(t, CaseSealed)
	packet, err := NewReviewPacket("packet-0003", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, events)
	if err != nil {
		t.Fatalf("packet: %v", err)
	}
	packet.FrozenCase.Nominal = 99.9
	verified, err := packet.Verify()
	if err != nil {
		t.Fatalf("verify should flag mismatch without error, got %v", err)
	}
	if verified {
		t.Fatal("tampered packet passed verification")
	}

	// A rewrite of the digest to a well-formed but wrong value is reported as
	// a verification failure, so the packet cannot present itself as intact.
	packet, _ = NewReviewPacket("packet-0004", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, events)
	packet.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
	forged, err := packet.Verify()
	if err != nil {
		t.Fatalf("forged digest should verify false without error, got %v", err)
	}
	if forged {
		t.Fatal("forged digest passed verification")
	}

	// A malformed digest is rejected as invalid input.
	packet, _ = NewReviewPacket("packet-0007", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, events)
	packet.Digest = "not-hex"
	if _, err := packet.Verify(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed digest: expected invalid input, got %v", err)
	}
}

func TestCanonicalManifestIsStableAcrossEventOrder(t *testing.T) {
	item, device, seal, measurement, events := packetFixture(t, CaseSealed)
	first, err := NewReviewPacket("packet-0005", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, events)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	reversed := []AuditEvent{events[2], events[1], events[0]}
	second, err := NewReviewPacket("packet-0006", "Auditor-An", packetFixtureTime(), item, device, seal, measurement, reversed)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("event input order changed the digest: %q vs %q", first.Digest, second.Digest)
	}
}
