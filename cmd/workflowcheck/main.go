package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
		"freeze-review-packet":     checkFreezeReviewPacket,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

func checkFreezeReviewPacket() error {
	h := newHarness()
	defer h.close()

	// An unsealed (review_ready) case must never produce a packet that could
	// be mistaken for formal evidence.
	readyCaseID, err := h.readyCase("MET-1501")
	if err != nil {
		return err
	}
	rejected, err := h.request(http.MethodPost, "/cases/"+readyCaseID+"/review-packet", map[string]any{
		"reviewer": "Auditor-An",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(rejected, "state_conflict"); err != nil {
		return err
	}

	// A missing reviewer is rejected before any packet is created.
	invalid, err := h.request(http.MethodPost, "/cases/"+readyCaseID+"/review-packet", map[string]any{
		"reviewer": "   ",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(invalid, http.StatusBadRequest); err != nil {
		return err
	}

	sealed, err := h.request(http.MethodPost, "/cases/"+readyCaseID+"/seal", map[string]any{
		"reviewer": "Reviewer-He",
		"summary":  "accepted measurement sealed for external review",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}
	sealedVersion, err := nestedFloat(sealed.Body, "case", "version")
	if err != nil {
		return err
	}

	first, err := h.request(http.MethodPost, "/cases/"+readyCaseID+"/review-packet", map[string]any{
		"reviewer": "Auditor-An",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(first, http.StatusCreated); err != nil {
		return err
	}
	packetID, err := nestedString(first.Body, "packet", "id")
	if err != nil {
		return err
	}
	digest, err := nestedString(first.Body, "packet", "digest")
	if err != nil {
		return err
	}
	digestAlg, err := nestedString(first.Body, "packet", "digest_alg")
	if err != nil {
		return err
	}
	if digestAlg != "sha256" || len(digest) != 64 {
		return fmt.Errorf("unexpected digest %s (%s)", digest, digestAlg)
	}
	frozenCaseID, err := nestedString(first.Body, "packet", "frozen_case", "id")
	if err != nil {
		return err
	}
	if frozenCaseID != readyCaseID {
		return fmt.Errorf("frozen case id = %q, expected %q", frozenCaseID, readyCaseID)
	}
	frozenVersion, err := nestedFloat(first.Body, "packet", "case_version")
	if err != nil {
		return err
	}
	if frozenVersion != sealedVersion {
		return fmt.Errorf("frozen version = %v, expected %v", frozenVersion, sealedVersion)
	}
	frozenState, err := nestedString(first.Body, "packet", "frozen_case", "state")
	if err != nil {
		return err
	}
	if frozenState != "sealed" {
		return fmt.Errorf("frozen case state = %q", frozenState)
	}
	frozenMeasurementCase, err := nestedString(first.Body, "packet", "frozen_measurement", "case_id")
	if err != nil {
		return err
	}
	if frozenMeasurementCase != readyCaseID {
		return fmt.Errorf("frozen measurement belongs to case %q", frozenMeasurementCase)
	}
	selectedMeasurementID, err := nestedString(first.Body, "packet", "frozen_seal", "measurement_id")
	if err != nil {
		return err
	}
	frozenMeasurementID, err := nestedString(first.Body, "packet", "frozen_measurement", "id")
	if err != nil {
		return err
	}
	if selectedMeasurementID != frozenMeasurementID {
		return fmt.Errorf("seal selects %s but packet carries measurement %s", selectedMeasurementID, frozenMeasurementID)
	}
	frozenEvents, err := nestedSlice(first.Body["packet"].(map[string]any), "frozen_events")
	if err != nil {
		return err
	}
	if len(frozenEvents) == 0 {
		return errors.New("frozen packet has no audit scope")
	}
	scopeSections, err := nestedAny(first.Body, "packet", "scope", "sections")
	if err != nil {
		return err
	}
	sections, ok := scopeSections.([]any)
	if !ok || len(sections) != 5 {
		return fmt.Errorf("packet scope sections = %v", scopeSections)
	}

	// Repeating the freeze for the same sealed version is idempotent and
	// cannot yield a contradictory packet.
	second, err := h.request(http.MethodPost, "/cases/"+readyCaseID+"/review-packet", map[string]any{
		"reviewer": "Auditor-An",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusOK); err != nil {
		return err
	}
	secondID, err := nestedString(second.Body, "packet", "id")
	if err != nil {
		return err
	}
	secondDigest, err := nestedString(second.Body, "packet", "digest")
	if err != nil {
		return err
	}
	if secondID != packetID || secondDigest != digest {
		return errors.New("repeated freeze produced a different packet")
	}

	// The stored packet is independently readable.
	read, err := h.request(http.MethodGet, "/cases/"+readyCaseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readDigest, err := nestedString(read.Body, "packet", "digest")
	if err != nil {
		return err
	}
	if readDigest != digest {
		return errors.New("stored packet read returned a different digest")
	}

	// Verification recomputes the digest and exposes the canonical manifest so
	// a reviewer can hash the bytes with an external tool.
	verification, err := h.request(http.MethodGet, "/cases/"+readyCaseID+"/review-packet/verify", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(verification, http.StatusOK); err != nil {
		return err
	}
	verified, err := nestedBool(verification.Body, "verified")
	if err != nil {
		return err
	}
	if !verified {
		return errors.New("freshly frozen packet failed verification")
	}
	computed, err := nestedString(verification.Body, "computed_digest")
	if err != nil {
		return err
	}
	stored, err := nestedString(verification.Body, "stored_digest")
	if err != nil {
		return err
	}
	if computed != stored || stored != digest {
		return fmt.Errorf("digest mismatch: stored %s computed %s", stored, computed)
	}
	manifest, err := nestedString(verification.Body, "canonical_manifest")
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(manifest))
	independentDigest := hex.EncodeToString(sum[:])
	if independentDigest != digest {
		return fmt.Errorf("external sha256 of manifest = %s, expected %s", independentDigest, digest)
	}

	// The freeze event belongs to the live audit trail, but it was appended
	// after the snapshot and a repeated freeze does not add another event.
	liveCase, err := h.request(http.MethodGet, "/cases/"+readyCaseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(liveCase, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(liveCase.Body, "review_packet_frozen") {
		return errors.New("freeze audit event is missing from the live case view")
	}
	liveEvents, err := nestedSlice(liveCase.Body, "events")
	if err != nil {
		return err
	}
	freezeEvents := 0
	for _, item := range liveEvents {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == "review_packet_frozen" {
			freezeEvents++
		}
	}
	if freezeEvents != 1 {
		return fmt.Errorf("expected exactly one freeze audit event, got %d", freezeEvents)
	}
	if len(frozenEvents) != len(liveEvents)-1 {
		return fmt.Errorf("freeze event leaked into the frozen packet: %d frozen vs %d live events", len(frozenEvents), len(liveEvents))
	}
	secondFrozenEvents, ok := second.Body["packet"].(map[string]any)
	if !ok {
		return errors.New("second packet is not an object")
	}
	entries, err := nestedSlice(secondFrozenEvents, "frozen_events")
	if err != nil {
		return err
	}
	if len(entries) != len(frozenEvents) {
		return errors.New("later audit activity changed the stored frozen packet")
	}

	// A case without a frozen packet reads as not found, not as evidence.
	otherCaseID, err := h.sealedCase("MET-1502")
	if err != nil {
		return err
	}
	missing, err := h.request(http.MethodGet, "/cases/"+otherCaseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusNotFound); err != nil {
		return err
	}
	if err := expectErrorCode(missing, "not_found"); err != nil {
		return err
	}

	// A nonexistent case fails fast.
	unknown, err := h.request(http.MethodPost, "/cases/case-9999/review-packet", map[string]any{
		"reviewer": "Auditor-An",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(unknown, http.StatusNotFound); err != nil {
		return err
	}
	return expectErrorCode(unknown, "not_found")
}

func (h *harness) sealedCase(tag string) (string, error) {
	caseID, err := h.readyCase(tag)
	if err != nil {
		return "", err
	}
	response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Reviewer-He",
		"summary":  "accepted measurement sealed for external review",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedFloat(root map[string]any, path ...string) (float64, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%s is not a number", strings.Join(path, "."))
	}
	return number, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
