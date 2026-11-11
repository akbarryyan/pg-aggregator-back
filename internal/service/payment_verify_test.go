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
			// The table models webhook verification (a webhook is in play).
			err := verifyProviderPayment(p, tc.status, tc.webhookAmount, true)
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

func TestVerifyProviderPayment_FeeBearing(t *testing.T) {
	paid := func(amount *int64) *domainProvider.NormalizedPaymentStatus {
		return &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: amount}
	}
	cases := []struct {
		name          string
		p             *payment.Payment
		status        *domainProvider.NormalizedPaymentStatus
		webhookAmount *int64
		wantErr       error
		wantMsg       string
	}{
		{"total matches", &payment.Payment{Amount: 2000, FeeAmount: 15}, paid(ptrInt64(2015)), ptrInt64(2015), nil, ""},
		{"total matches, no webhook amount", &payment.Payment{Amount: 2000, FeeAmount: 15}, paid(ptrInt64(2015)), nil, nil, ""},
		{"larger nominal total matches", &payment.Payment{Amount: 100000, FeeAmount: 705}, paid(ptrInt64(100705)), ptrInt64(100705), nil, ""},
		{"provider reports nominal only", &payment.Payment{Amount: 2000, FeeAmount: 15}, paid(ptrInt64(2000)), nil, payment.ErrAmountMismatch, "amount_mismatch: expected 2015, provider 2000, webhook none"},
		{"webhook reports nominal only", &payment.Payment{Amount: 2000, FeeAmount: 15}, paid(ptrInt64(2015)), ptrInt64(2000), payment.ErrWebhookAmountMismatch, "webhook_amount_mismatch: expected 2015, provider 2015, webhook 2000"},
		{"provider amount unknown", &payment.Payment{Amount: 2000, FeeAmount: 15}, paid(nil), nil, payment.ErrAmountMismatch, "amount_mismatch: expected 2015, provider none, webhook none"},
		{"no fee behaves as before", &payment.Payment{Amount: 2000}, paid(ptrInt64(2000)), ptrInt64(2000), nil, ""},
		{"no fee rejects a fee-inflated amount", &payment.Payment{Amount: 2000}, paid(ptrInt64(2015)), nil, payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider 2015, webhook none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyProviderPayment(tc.p, tc.status, tc.webhookAmount, true)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err.Error() != tc.wantMsg {
				t.Errorf("message = %q, want %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// Cashi confirms a settled order without restating its amount. The webhook path
// then needs the signed webhook to state the expected amount; reconcile accepts
// the settled status alone; an amount that IS stated must still match.
func TestVerifyProviderPayment_ProviderOmitsAmount(t *testing.T) {
	settledNoAmount := &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid}
	merchantBears := &payment.Payment{Amount: 2000}
	customerBears := &payment.Payment{Amount: 2000, FeeAmount: 15}

	cases := []struct {
		name          string
		p             *payment.Payment
		status        *domainProvider.NormalizedPaymentStatus
		webhookAmount *int64
		viaWebhook    bool
		wantErr       error
		wantMsg       string
	}{
		{"webhook states the expected amount", merchantBears, settledNoAmount, ptrInt64(2000), true, nil, ""},
		{"webhook states the expected total (fee on top)", customerBears, settledNoAmount, ptrInt64(2015), true, nil, ""},
		{"webhook states a different amount", merchantBears, settledNoAmount, ptrInt64(1500), true, payment.ErrWebhookAmountMismatch, "webhook_amount_mismatch: expected 2000, provider none, webhook 1500"},
		{"webhook states only the nominal when a fee was added", customerBears, settledNoAmount, ptrInt64(2000), true, payment.ErrWebhookAmountMismatch, "webhook_amount_mismatch: expected 2015, provider none, webhook 2000"},
		{"nobody states an amount via webhook fails closed", merchantBears, settledNoAmount, nil, true, payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider none, webhook none"},
		{"reconcile accepts the settled status alone", merchantBears, settledNoAmount, nil, false, nil, ""},
		{"reconcile with a customer-borne fee", customerBears, settledNoAmount, nil, false, nil, ""},
		{"not settled is still rejected", merchantBears, &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPending}, ptrInt64(2000), true, payment.ErrNotSettledAtProvider, ""},
		{"a stated provider amount must still match (reconcile)", merchantBears, &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(0)}, nil, false, payment.ErrAmountMismatch, "amount_mismatch: expected 2000, provider 0, webhook none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyProviderPayment(tc.p, tc.status, tc.webhookAmount, tc.viaWebhook)
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
		})
	}
}
