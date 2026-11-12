package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/google/uuid"
)

func seedSimPayment(t *testing.T, repo *fakePaymentRepo, env, providerName, status string, fee int64) *payment.Payment {
	t.Helper()
	ref := "SBX-" + uuid.NewString()[:8]
	p := &payment.Payment{
		ID: uuid.New(), Reference: "PAY-" + ref, MerchantID: uuid.New(),
		Amount: 2000, FeeAmount: fee, Currency: payment.CurrencyIDR,
		PaymentMethod: payment.PaymentMethodQRIS, ProviderName: providerName,
		ProviderReference: &ref, Status: status, Environment: env,
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return p
}

func TestSimulateSandboxOutcome_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		outcome, wantEvent string
		fee, wantNet       int64
	}{
		{"paid", "payment.paid", 0, 1986},
		{"paid", "payment.paid", 14, 2000},
		{"failed", "payment.failed", 0, 0},
		{"expired", "payment.expired", 0, 0},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			svc, repo, _, _ := newTestPaymentService()
			p := seedSimPayment(t, repo, payment.EnvironmentSandbox, "sandbox", payment.StatusPending, tc.fee)
			callbacks := newCallbackMerchant(t, svc, p.MerchantID)

			got, err := svc.SimulateSandboxOutcome(context.Background(), p, tc.outcome)
			if err != nil {
				t.Fatalf("simulate: %v", err)
			}
			if got.Status != tc.outcome {
				t.Fatalf("returned status = %s, want %s", got.Status, tc.outcome)
			}
			stored, _ := repo.GetByID(context.Background(), p.ID)
			if stored.Status != tc.outcome {
				t.Fatalf("stored status = %s, want %s", stored.Status, tc.outcome)
			}
			if tc.outcome == "paid" {
				if stored.PaidAt == nil || stored.MerchantNetAmount != tc.wantNet {
					t.Fatalf("paid: paid_at=%v net=%d, want net %d", stored.PaidAt, stored.MerchantNetAmount, tc.wantNet)
				}
			}
			if len(callbacks.created) != 1 || callbacks.created[0].EventType != tc.wantEvent {
				t.Fatalf("want one %s callback, got %+v", tc.wantEvent, callbacks.created)
			}
		})
	}
}

func TestSimulateSandboxOutcome_RecordsEvent(t *testing.T) {
	svc, repo, _, _ := newTestPaymentService()
	webhookRepo := svc.webhookEventRepo.(*fakeWebhookEventRepo)
	p := seedSimPayment(t, repo, payment.EnvironmentSandbox, "sandbox", payment.StatusPending, 0)

	if _, err := svc.SimulateSandboxOutcome(context.Background(), p, "failed"); err != nil {
		t.Fatalf("simulate: %v", err)
	}
	ev := webhookRepo.last()
	if ev == nil || ev.EventType != "sandbox.simulate.failed" || ev.ProviderName != "sandbox" ||
		ev.Status != "failed" || !ev.IsProcessed || ev.ProcessedAt == nil || ev.PaymentID == nil || *ev.PaymentID != p.ID {
		t.Fatalf("unexpected simulation event: %+v", ev)
	}
}

func TestSimulateSandboxOutcome_Guards(t *testing.T) {
	for _, tc := range []struct {
		name, env, provider, status, outcome string
		want                                 error
	}{
		{"production rejected", payment.EnvironmentProduction, "cashi", payment.StatusPending, "paid", payment.ErrSimulationNotAllowed},
		{"production with sandbox provider rejected", payment.EnvironmentProduction, "sandbox", payment.StatusPending, "paid", payment.ErrSimulationNotAllowed},
		{"sandbox env but real provider rejected", payment.EnvironmentSandbox, "cashi", payment.StatusPending, "paid", payment.ErrSimulationNotAllowed},
		{"not pending", payment.EnvironmentSandbox, "sandbox", payment.StatusPaid, "failed", payment.ErrPaymentAlreadyTerminal},
		{"invalid outcome", payment.EnvironmentSandbox, "sandbox", payment.StatusPending, "refunded", payment.ErrInvalidSimulationOutcome},
		{"empty outcome", payment.EnvironmentSandbox, "sandbox", payment.StatusPending, "", payment.ErrInvalidSimulationOutcome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _, _ := newTestPaymentService()
			p := seedSimPayment(t, repo, tc.env, tc.provider, tc.status, 0)
			callbacks := newCallbackMerchant(t, svc, p.MerchantID)

			_, err := svc.SimulateSandboxOutcome(context.Background(), p, tc.outcome)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			stored, _ := repo.GetByID(context.Background(), p.ID)
			if stored.Status != tc.status {
				t.Fatalf("status changed to %s on a rejected simulation", stored.Status)
			}
			if len(callbacks.created) != 0 {
				t.Fatalf("rejected simulation sent callbacks: %+v", callbacks.created)
			}
		})
	}
}
