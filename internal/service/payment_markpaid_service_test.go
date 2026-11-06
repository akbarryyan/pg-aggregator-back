package service

import (
	"context"
	"errors"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
)

func TestMarkPaidNet_WebhookAndReconcile(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		fee        int64
		wantNet    int64
		viaWebhook bool
	}{
		{"webhook fee on customer", payment.StatusPending, 70, 10000, true},
		{"webhook fee on merchant", payment.StatusPending, 0, 9930, true},
		{"webhook late settlement", payment.StatusExpired, 0, 9930, true},
		{"reconcile fee on customer", payment.StatusPending, 70, 10000, false},
		{"reconcile fee on merchant", payment.StatusPending, 0, 9930, false},
		{"reconcile late settlement", payment.StatusExpired, 70, 10000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _, router := newWebhookTestService()
			p := seedPayment(t, repo, tc.status, "REF-NET")
			repo.mu.Lock()
			repo.byID[p.ID].Amount = 10000
			repo.byID[p.ID].FeeAmount = tc.fee
			repo.mu.Unlock()
			paidAmount := 10000 + tc.fee
			router.RegisterProvider(&fakeProvider{name: "cashi",
				parseResp:  &domainProvider.ProviderWebhookPayload{ProviderReference: "REF-NET", Status: payment.StatusPaid},
				statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(paidAmount)},
			})
			if tc.viaWebhook {
				if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
					t.Fatalf("webhook: %v", err)
				}
			} else {
				if _, err := svc.ReconcilePayment(context.Background(), p.ID); err != nil {
					t.Fatalf("reconcile: %v", err)
				}
			}
			if len(repo.markPaidNets) != 1 || repo.markPaidNets[0] != tc.wantNet {
				t.Fatalf("MarkPaid nets = %v, want [%d]", repo.markPaidNets, tc.wantNet)
			}
			got, _ := repo.GetByID(context.Background(), p.ID)
			if got.Status != payment.StatusPaid || got.MerchantNetAmount != tc.wantNet {
				t.Fatalf("stored status=%q net=%d", got.Status, got.MerchantNetAmount)
			}
		})
	}
}

// A payment whose net cannot be computed must never be marked paid: no
// MarkPaid call, status stays pending (retryable), the error surfaces.
func TestMarkPaidNet_InvalidAmountNeverMarksPaid(t *testing.T) {
	for _, amount := range []int64{0, fee.MaxNominal + 1} {
		for _, viaWebhook := range []bool{true, false} {
			svc, repo, webhookRepo, router := newWebhookTestService()
			p := seedPayment(t, repo, payment.StatusPending, "REF-BAD")
			repo.mu.Lock()
			repo.byID[p.ID].Amount = amount
			repo.mu.Unlock()
			router.RegisterProvider(&fakeProvider{name: "cashi",
				parseResp:  &domainProvider.ProviderWebhookPayload{ProviderReference: "REF-BAD", Status: payment.StatusPaid},
				statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(amount)},
			})
			if viaWebhook {
				err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
				if !errors.Is(err, fee.ErrInvalidNominal) {
					t.Fatalf("amount %d webhook: err = %v, want fee.ErrInvalidNominal", amount, err)
				}
				e := lastEvent(webhookRepo)
				if e == nil || e.IsProcessed || e.ProcessingError == nil {
					t.Fatalf("amount %d webhook: expected failed event, got %+v", amount, e)
				}
			} else {
				_, err := svc.ReconcilePayment(context.Background(), p.ID)
				if !errors.Is(err, fee.ErrInvalidNominal) {
					t.Fatalf("amount %d reconcile: err = %v, want fee.ErrInvalidNominal", amount, err)
				}
			}
			if len(repo.markPaidNets) != 0 {
				t.Fatalf("amount %d: MarkPaid must not be called, got %v", amount, repo.markPaidNets)
			}
			got, _ := repo.GetByID(context.Background(), p.ID)
			if got.Status != payment.StatusPending {
				t.Fatalf("amount %d: status = %q, want pending", amount, got.Status)
			}
		}
	}
}
