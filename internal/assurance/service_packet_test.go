package assurance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

func sealedCaseSetup(t *testing.T) (*Service, string) {
	t.Helper()
	service := NewService(repository.New())
	ctx := context.Background()
	device, err := service.RegisterDevice(ctx, contracts.RegisterDeviceRequest{
		AssetTag: "MET-3001",
		Model:    "Bench Meter",
		Room:     "Room A",
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	opened, err := service.OpenCase(ctx, contracts.OpenCaseRequest{
		DeviceID:  device.ID,
		Nominal:   25,
		Tolerance: 0.5,
		Steward:   "Qiao",
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}
	if _, err := service.RecordMeasurement(ctx, opened.Case.ID, contracts.RecordMeasurementRequest{
		Observed:   25.1,
		Technician: "Rui",
	}); err != nil {
		t.Fatalf("record measurement: %v", err)
	}
	if _, err := service.SealCase(ctx, opened.Case.ID, contracts.SealCaseRequest{
		Reviewer: "He",
		Summary:  "sealed for external review",
	}); err != nil {
		t.Fatalf("seal case: %v", err)
	}
	return service, opened.Case.ID
}

func TestGenerateReviewPacketIsIdempotentForOneCaseVersion(t *testing.T) {
	service, caseID := sealedCaseSetup(t)
	ctx := context.Background()

	first, created, err := service.GenerateReviewPacket(ctx, caseID, contracts.FreezeReviewPacketRequest{Reviewer: "Auditor-An"})
	if err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	if !created {
		t.Fatal("first freeze must report created=true")
	}

	second, created, err := service.GenerateReviewPacket(ctx, caseID, contracts.FreezeReviewPacketRequest{Reviewer: "Auditor-An"})
	if err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	if created {
		t.Fatal("second freeze must report created=false")
	}
	if second.ID != first.ID || second.Digest != first.Digest {
		t.Fatalf("repeated freeze changed the packet: %q vs %q", second.ID, first.ID)
	}

	read, err := service.ReviewPacket(ctx, caseID)
	if err != nil {
		t.Fatalf("read packet: %v", err)
	}
	if read.Digest != first.Digest {
		t.Fatal("stored packet digest changed between reads")
	}
}

func TestConcurrentReviewPacketFreezeProducesOnePacket(t *testing.T) {
	service, caseID := sealedCaseSetup(t)
	ctx := context.Background()

	const goroutines = 8
	type outcome struct {
		packet  domain.ReviewPacket
		created bool
		err     error
	}
	results := make(chan outcome, goroutines)
	var wait sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			packet, created, err := service.GenerateReviewPacket(ctx, caseID, contracts.FreezeReviewPacketRequest{Reviewer: "Auditor-An"})
			results <- outcome{packet: packet, created: created, err: err}
		}()
	}
	wait.Wait()
	close(results)

	creates := 0
	var digest string
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent freeze: %v", item.err)
		}
		if item.created {
			creates++
		}
		if digest == "" {
			digest = item.packet.Digest
		} else if item.packet.Digest != digest {
			t.Fatal("concurrent freezes produced different digests")
		}
	}
	if creates != 1 {
		t.Fatalf("expected exactly one created packet, got %d", creates)
	}
}

func TestGenerateReviewPacketRejectsUnsealedCase(t *testing.T) {
	service := NewService(repository.New())
	ctx := context.Background()
	device, err := service.RegisterDevice(ctx, contracts.RegisterDeviceRequest{
		AssetTag: "MET-3002", Model: "Bench Meter", Room: "Room A", Steward: "Lin",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	opened, err := service.OpenCase(ctx, contracts.OpenCaseRequest{
		DeviceID: device.ID, Nominal: 25, Tolerance: 0.5, Steward: "Qiao",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := service.GenerateReviewPacket(ctx, opened.Case.ID, contracts.FreezeReviewPacketRequest{Reviewer: "Auditor-An"}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("expected state conflict for unsealed case, got %v", err)
	}
	if _, err := service.ReviewPacket(ctx, opened.Case.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found for missing packet, got %v", err)
	}
}

func TestGenerateReviewPacketRejectsBlankReviewer(t *testing.T) {
	service, caseID := sealedCaseSetup(t)
	_, _, err := service.GenerateReviewPacket(context.Background(), caseID, contracts.FreezeReviewPacketRequest{Reviewer: " "})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input, got %v", err)
	}
}

func TestVerifyReviewPacketRecomputesDigest(t *testing.T) {
	service, caseID := sealedCaseSetup(t)
	ctx := context.Background()
	packet, _, err := service.GenerateReviewPacket(ctx, caseID, contracts.FreezeReviewPacketRequest{Reviewer: "Auditor-An"})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	view, err := service.VerifyReviewPacket(ctx, caseID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !view.Verified || view.ComputedDigest != packet.Digest || view.StoredDigest != packet.Digest {
		t.Fatalf("verification view mismatch: %+v", view)
	}
	if view.CanonicalManifest == "" {
		t.Fatal("canonical manifest was not returned for independent verification")
	}
}
