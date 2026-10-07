package assurance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			"verification case opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, s.now()); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = "measurement_accepted"
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

// GenerateReviewPacket freezes a sealed case version into a stable review
// packet. Repeating the request for the same case version returns the same
// stored packet; unsealed or incomplete cases are rejected.
func (s *Service) GenerateReviewPacket(ctx context.Context, caseID string, input contracts.FreezeReviewPacketRequest) (domain.ReviewPacket, bool, error) {
	reviewer := strings.TrimSpace(input.Reviewer)
	if reviewer == "" {
		return domain.ReviewPacket{}, false, fmt.Errorf("%w: reviewer is required", domain.ErrInvalidInput)
	}

	var packet domain.ReviewPacket
	created := false
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if item.State != domain.CaseSealed {
			return fmt.Errorf("%w: case %s is %s, only a sealed case can be frozen", domain.ErrStateConflict, item.ID, item.State)
		}
		if existing, exists := m.Packet(item.ID, item.Version); exists {
			packet = existing
			return nil
		}

		seal, exists := m.Seal(item.ID)
		if !exists {
			return fmt.Errorf("%w: case %s has no evidence seal", domain.ErrStateConflict, item.ID)
		}
		var selected *domain.Measurement
		for _, measurement := range m.Measurements(item.ID) {
			measurement := measurement
			if measurement.ID == seal.MeasurementID {
				selected = &measurement
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("%w: seal %s selects missing measurement %s", domain.ErrStateConflict, seal.ID, seal.MeasurementID)
		}
		device, exists := m.Device(item.DeviceID)
		if !exists {
			return fmt.Errorf("%w: device %s for case %s", domain.ErrNotFound, item.DeviceID, item.ID)
		}
		// Snapshot the audit trail before appending the freeze event, so the
		// packet proves evidence that existed at freeze time and later
		// retroactive entries can never bleed into an old packet.
		events := m.Events(item.ID)

		built, err := domain.NewReviewPacket(
			m.NextID("packet"),
			reviewer,
			s.now(),
			item,
			device,
			seal,
			*selected,
			events,
		)
		if err != nil {
			return err
		}
		if err := m.PutFreeze(built); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"review_packet",
			built.ID,
			item.ID,
			"review_packet_frozen",
			reviewer,
			fmt.Sprintf("review packet %s frozen for case version %d with digest %s", built.ID, item.Version, built.Digest),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		packet = built
		created = true
		return nil
	})
	return packet, created, err
}

// ReviewPacket reads the frozen packet for a sealed case. The stored snapshot
// is returned as-is; live case data never replaces the frozen payload.
func (s *Service) ReviewPacket(ctx context.Context, caseID string) (domain.ReviewPacket, error) {
	var packet domain.ReviewPacket
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		id := strings.TrimSpace(caseID)
		item, exists := snapshot.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if found, exists := snapshot.PacketForCase(item.ID); exists {
			packet = found
			return nil
		}
		return fmt.Errorf("%w: no review packet has been frozen for case %s", domain.ErrNotFound, item.ID)
	})
	return packet, err
}

// VerifyReviewPacket recomputes the digest of a stored packet from its frozen
// payload and returns both digests plus the canonical manifest bytes, so an
// external reviewer can independently hash the same content.
func (s *Service) VerifyReviewPacket(ctx context.Context, caseID string) (contracts.ReviewPacketVerificationResponse, error) {
	packet, err := s.ReviewPacket(ctx, caseID)
	if err != nil {
		return contracts.ReviewPacketVerificationResponse{}, err
	}
	manifest, err := packet.CanonicalManifest()
	if err != nil {
		return contracts.ReviewPacketVerificationResponse{}, err
	}
	verified, err := packet.Verify()
	if err != nil {
		return contracts.ReviewPacketVerificationResponse{}, err
	}
	return contracts.ReviewPacketVerificationResponse{
		CaseID:            packet.CaseID,
		PacketID:          packet.ID,
		CaseVersion:       packet.CaseVersion,
		SchemaVersion:     packet.SchemaVersion,
		DigestAlg:         packet.DigestAlg,
		StoredDigest:      packet.Digest,
		ComputedDigest:    sha256Hex(manifest),
		Verified:          verified,
		CanonicalManifest: string(manifest),
	}, nil
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
