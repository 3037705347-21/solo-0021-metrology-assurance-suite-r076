package contracts

import "example.com/solo-0021-metrology-assurance-suite/internal/domain"

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

type OpenCaseRequest struct {
	DeviceID  string  `json:"device_id"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
	Steward   string  `json:"steward"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

type ReopenCaseRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
}

type FreezeReviewPacketRequest struct {
	Reviewer string `json:"reviewer"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

type CaseResponse struct {
	Case         domain.VerificationCase `json:"case"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ReviewPacketResponse struct {
	Packet domain.ReviewPacket `json:"packet"`
}

type ReviewPacketVerificationResponse struct {
	CaseID            string `json:"case_id"`
	PacketID          string `json:"packet_id"`
	CaseVersion       int    `json:"case_version"`
	SchemaVersion     int    `json:"schema_version"`
	DigestAlg         string `json:"digest_alg"`
	StoredDigest      string `json:"stored_digest"`
	ComputedDigest    string `json:"computed_digest"`
	Verified          bool   `json:"verified"`
	CanonicalManifest string `json:"canonical_manifest"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
