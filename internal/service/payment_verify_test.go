package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
)

func TestVerifyProviderPayment(t *testing.T) {
	p := &payment.Payment{Amount: 2000}
	paid := func(amount *int64) *domainProvider.NormalizedPaymentStatus {
		return &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: amount}
	}
	cases := []struct {
		name          string
		status        *domainProvider.NormalizedPaymentStatus
		webhookAmount *int64
		wantErr       error
		wantMsg       string
	}{
		{"settled and amounts match", paid(ptrInt64(2000)), ptrInt64(2000), nil, ""},
		{"settled, webhook has no amount", paid(ptrInt64(2000)), nil, nil, ""},
		{"not settled", &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending, Amount: ptrInt64(2000)}, nil, payment.ErrNotSettledAtProvider, ""},
		{"nil status", nil, nil, payment.ErrNotSettledAtProvider, ""},
		{"provider amount differs", paid(ptrInt64(1500)), ptrInt64(1500), payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider 1500, webhook 1500"},
		{"webhook amount differs", paid(ptrInt64(2000)), ptrInt64(1986), payment.ErrWebhookAmountMismatch, "webhook_amount_mismatch: expected 2000, provider 2000, webhook 1986"},
		{"provider amount unknown", paid(nil), nil, payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider none, webhook none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyProviderPayment(p, tc.status, tc.webhookAmount)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantMsg != "" && err.Error() != tc.wantMsg {
				t.Errorf("message = %q, want %q", err.Error(), tc.wantMsg)
			}
			if errors.Is(err, payment.ErrAmountMismatch) && !strings.HasPrefix(err.Error(), "amount_mismatch:") {
				t.Errorf("mismatch message must start with amount_mismatch:, got %q", err.Error())
			}
			// A webhook-only mismatch must not look like a provider-side one,
			// or reconcile's LIKE 'amount_mismatch%' filter would exclude it.
			if tc.wantErr == payment.ErrWebhookAmountMismatch && errors.Is(err, payment.ErrAmountMismatch) {
				t.Errorf("webhook-only mismatch must not satisfy ErrAmountMismatch: %v", err)
			}
		})
	}
}
