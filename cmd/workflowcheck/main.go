package main

import (
	"bytes"
	"context"
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
	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
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

	caseID, err := h.readyCase("MET-1501")
	if err != nil {
		return err
	}

	// An unsealed case must not produce anything that looks like evidence.
	early, err := h.request(http.MethodPost, "/cases/"+caseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(early, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(early, "case_not_sealed"); err != nil {
		return err
	}
	missing, err := h.request(http.MethodGet, "/cases/"+caseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusNotFound); err != nil {
		return err
	}

	sealed, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Reviewer-A",
		"summary":  "accepted measurement sealed for review",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}
	sealID, err := nestedString(sealed.Body, "seal", "id")
	if err != nil {
		return err
	}
	sealMeasurementID, err := nestedString(sealed.Body, "seal", "measurement_id")
	if err != nil {
		return err
	}

	frozen, err := h.request(http.MethodPost, "/cases/"+caseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(frozen, http.StatusCreated); err != nil {
		return err
	}
	if err := expectPacketShape(frozen.Body, caseID, sealID, sealMeasurementID); err != nil {
		return err
	}

	// Regenerating the same sealed case version returns the identical packet.
	again, err := h.request(http.MethodPost, "/cases/"+caseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(again, http.StatusOK); err != nil {
		return err
	}
	if again.Raw != frozen.Raw {
		return fmt.Errorf("regenerated packet differs:\nfirst:  %s\nsecond: %s", frozen.Raw, again.Raw)
	}

	// Reading the frozen packet returns the identical frozen content.
	fetched, err := h.request(http.MethodGet, "/cases/"+caseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(fetched, http.StatusOK); err != nil {
		return err
	}
	if fetched.Raw != frozen.Raw {
		return fmt.Errorf("fetched packet differs from frozen packet:\nfrozen:  %s\nfetched: %s", frozen.Raw, fetched.Raw)
	}

	// Independent verification: recompute the digest from the packet content.
	var view contracts.ReviewPacketResponse
	if err := json.Unmarshal([]byte(fetched.Raw), &view); err != nil {
		return fmt.Errorf("decode review packet: %w", err)
	}
	if !view.Packet.VerifyDigest() {
		return errors.New("packet digest does not match its content")
	}
	recomputed := domain.ComputeReviewPacketDigest(view.Packet.Case, view.Packet.Measurement, view.Packet.Seal, view.Packet.Events)
	if recomputed != view.Packet.Digest {
		return fmt.Errorf("recomputed digest %q != packet digest %q", recomputed, view.Packet.Digest)
	}

	// Later case data changes are rejected and cannot rewrite the packet.
	lateMeasurement, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(lateMeasurement, http.StatusConflict); err != nil {
		return err
	}
	lateReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "late amendment attempt",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(lateReopen, http.StatusConflict); err != nil {
		return err
	}
	refetched, err := h.request(http.MethodGet, "/cases/"+caseID+"/review-packet", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(refetched, http.StatusOK); err != nil {
		return err
	}
	if refetched.Raw != frozen.Raw {
		return errors.New("packet changed after rejected mutation attempts")
	}

	// Concurrent freeze requests on a fresh sealed case produce exactly one
	// packet: one 201, one 200, identical content.
	raceCaseID, err := h.readyCase("MET-1502")
	if err != nil {
		return err
	}
	raceSealed, err := h.request(http.MethodPost, "/cases/"+raceCaseID+"/seal", map[string]any{
		"reviewer": "Reviewer-B",
		"summary":  "accepted measurement sealed for review",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(raceSealed, http.StatusCreated); err != nil {
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
		go func() {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+raceCaseID+"/review-packet", nil)
			results <- outcome{response: response, err: err}
		}()
	}
	wait.Wait()
	close(results)

	created := 0
	existing := 0
	var raceRaw string
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			existing++
		default:
			return fmt.Errorf("unexpected concurrent freeze status %d: %s", item.response.Status, item.response.Raw)
		}
		if raceRaw == "" {
			raceRaw = item.response.Raw
		} else if raceRaw != item.response.Raw {
			return errors.New("concurrent freeze returned contradictory packets")
		}
	}
	if created != 1 || existing != 1 {
		return fmt.Errorf("concurrent freeze outcomes: %d created, %d existing", created, existing)
	}
	return nil
}

func expectPacketShape(body map[string]any, caseID, sealID, measurementID string) error {
	packetCaseID, err := nestedString(body, "packet", "case", "id")
	if err != nil {
		return err
	}
	if packetCaseID != caseID {
		return fmt.Errorf("packet case id = %q, expected %q", packetCaseID, caseID)
	}
	state, err := nestedString(body, "packet", "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("packet case state = %q", state)
	}
	packetSealID, err := nestedString(body, "packet", "seal", "id")
	if err != nil {
		return err
	}
	if packetSealID != sealID {
		return fmt.Errorf("packet seal id = %q, expected %q", packetSealID, sealID)
	}
	packetMeasurementID, err := nestedString(body, "packet", "measurement", "id")
	if err != nil {
		return err
	}
	if packetMeasurementID != measurementID {
		return fmt.Errorf("packet measurement id = %q, expected %q", packetMeasurementID, measurementID)
	}
	digest, err := nestedString(body, "packet", "digest")
	if err != nil {
		return err
	}
	if len(digest) != 64 {
		return fmt.Errorf("packet digest %q is not a sha256 hex string", digest)
	}
	algorithm, err := nestedString(body, "packet", "digest_algorithm")
	if err != nil {
		return err
	}
	if algorithm != "sha256" {
		return fmt.Errorf("packet digest algorithm = %q", algorithm)
	}
	packet, ok := body["packet"].(map[string]any)
	if !ok {
		return errors.New("packet is not an object")
	}
	eventItems, ok := packet["events"].([]any)
	if !ok || len(eventItems) == 0 {
		return errors.New("packet events are missing")
	}
	scope, ok := packet["audit_scope"].(map[string]any)
	if !ok {
		return errors.New("packet audit scope is missing")
	}
	count, ok := scope["event_count"].(float64)
	if !ok || int(count) != len(eventItems) {
		return fmt.Errorf("audit scope count %v does not match %d events", scope["event_count"], len(eventItems))
	}
	last, ok := eventItems[len(eventItems)-1].(map[string]any)
	if !ok || last["kind"] != "case_sealed" {
		return errors.New("packet audit trail does not end at the seal event")
	}
	if scope["last_event_id"] != last["id"] {
		return errors.New("audit scope last event does not match the final packet event")
	}
	return nil
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
