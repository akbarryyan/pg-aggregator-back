package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
)

func TestCreatePayment_RejectsAmountBelowMinimum(t *testing.T) {
	a := NewAdapter()
	_, err := a.CreatePayment(context.Background(), &provider.ProviderPaymentRequest{
		InternalReference: "REF-1",
		Amount:            1999,
	})
	if err == nil {
		t.Fatalf("expected error for amount below 2000")
	}
}

func TestCreatePayment_Success(t *testing.T) {
	a := NewAdapter()
	expiresAt := time.Now().Add(5 * time.Minute)

	resp, err := a.CreatePayment(context.Background(), &provider.ProviderPaymentRequest{
		InternalReference: "REF-2",
		Amount:            15000,
		ExpiresAt:         expiresAt,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ProviderName != ProviderName {
		t.Errorf("expected provider name %q, got %q", ProviderName, resp.ProviderName)
	}
	if resp.Status != "pending" {
		t.Errorf("expected status pending, got %q", resp.Status)
	}
	if !strings.HasPrefix(resp.ProviderReference, "SBX-") {
		t.Errorf("expected reference to start with SBX-, got %q", resp.ProviderReference)
	}
	if resp.Amount < 15001 || resp.Amount > 15099 {
		t.Errorf("expected amount to include 1-99 unique suffix, got %d", resp.Amount)
	}
	if resp.QRISData == nil || *resp.QRISData == "" {
		t.Errorf("expected QRISData to be set")
	}
	if resp.PaymentURL == nil || *resp.PaymentURL == "" {
		t.Errorf("expected PaymentURL to be set")
	}
	if !resp.ExpiresAt.Equal(expiresAt) {
		t.Errorf("expected ExpiresAt to be preserved from request, got %v want %v", resp.ExpiresAt, expiresAt)
	}

	// Never calls out to Cashi: response must be explicitly flagged sandbox.
	if sandboxFlag, _ := resp.RawResponse["sandbox"].(bool); !sandboxFlag {
		t.Errorf("expected RawResponse to flag sandbox=true")
	}
}

func TestCreatePayment_DefaultExpiryWhenNotProvided(t *testing.T) {
	a := NewAdapter()
	before := time.Now()
	resp, err := a.CreatePayment(context.Background(), &provider.ProviderPaymentRequest{
		InternalReference: "REF-3",
		Amount:            5000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt.Before(before.Add(9*time.Minute)) || resp.ExpiresAt.After(before.Add(11*time.Minute)) {
		t.Errorf("expected default ~10 minute expiry, got %v", resp.ExpiresAt)
	}
}

func TestGetPaymentStatus_UnknownReferenceIsPending(t *testing.T) {
	a := NewAdapter()
	status, err := a.GetPaymentStatus(context.Background(), "never-created")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending for unknown reference, got %q", status.Status)
	}
}

func TestGetPaymentStatus_ExpiresAfterDeadline(t *testing.T) {
	a := NewAdapter()
	resp, err := a.CreatePayment(context.Background(), &provider.ProviderPaymentRequest{
		InternalReference: "REF-4",
		Amount:            5000,
		ExpiresAt:         time.Now().Add(-1 * time.Minute), // already expired
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status, err := a.GetPaymentStatus(context.Background(), resp.ProviderReference)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "expired" {
		t.Errorf("expected expired status once past deadline, got %q", status.Status)
	}
}

func TestGetPaymentStatus_ForcePaidDevHelper(t *testing.T) {
	a := NewAdapter()
	// Seed state directly (internal test file, same package) with a reference
	// containing the manual-test marker used by GetPaymentStatus.
	ref := "SBX-FORCEPAID-TEST"
	a.orders[ref] = &orderState{
		Status:    "pending",
		Amount:    5000,
		ExpiresAt: time.Now().Add(10 * time.Minute),
		CreatedAt: time.Now(),
	}

	status, err := a.GetPaymentStatus(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "paid" {
		t.Errorf("expected FORCEPAID reference to report paid, got %q", status.Status)
	}
}

func TestMarkPaid(t *testing.T) {
	a := NewAdapter()
	resp, err := a.CreatePayment(context.Background(), &provider.ProviderPaymentRequest{
		InternalReference: "REF-5",
		Amount:            5000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !a.MarkPaid(resp.ProviderReference) {
		t.Fatalf("expected MarkPaid to succeed for existing reference")
	}
	status, err := a.GetPaymentStatus(context.Background(), resp.ProviderReference)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "paid" {
		t.Errorf("expected paid after MarkPaid, got %q", status.Status)
	}

	if a.MarkPaid("does-not-exist") {
		t.Errorf("expected MarkPaid to fail for unknown reference")
	}
}

func TestValidateAndParseWebhook_NotSupported(t *testing.T) {
	a := NewAdapter()
	if err := a.ValidateWebhook([]byte(`{}`), "any-signature"); err != nil {
		t.Errorf("sandbox should never reject webhook validation, got %v", err)
	}
	if _, err := a.ParseWebhook([]byte(`{}`)); err == nil {
		t.Errorf("expected sandbox to reject external webhook parsing")
	}
}

func TestSandboxNormalizeStatus(t *testing.T) {
	a := NewAdapter()
	paidLike := []string{"paid", "PAID", "settled", "success"}
	for _, s := range paidLike {
		if got := a.NormalizeStatus(s); got != "paid" {
			t.Errorf("NormalizeStatus(%q) = %q, want paid", s, got)
		}
	}
	if got := a.NormalizeStatus("expired"); got != "expired" {
		t.Errorf("NormalizeStatus(expired) = %q, want expired", got)
	}
	if got := a.NormalizeStatus("failed"); got != "failed" {
		t.Errorf("NormalizeStatus(failed) = %q, want failed", got)
	}
	if got := a.NormalizeStatus("whatever"); got != "pending" {
		t.Errorf("NormalizeStatus(whatever) = %q, want pending", got)
	}
}
