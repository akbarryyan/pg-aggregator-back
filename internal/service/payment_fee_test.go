package service

import (
	"context"
	"errors"
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/google/uuid"
)

// feeMerchantSvc wires a merchant repo holding one active merchant with the
// given fee bearer and returns the service plus that merchant's ID.
func feeMerchantSvc(t *testing.T, bearer string) (*PaymentService, *fakePaymentRepo, *fakeMerchantProviderConfigRepo, *providerPkg.ProviderRouter, uuid.UUID) {
	t.Helper()
	svc, paymentRepo, configRepo, router := newTestPaymentService()
	merchantRepo := newFakeMerchantRepo()
	m := &merchant.Merchant{ID: uuid.New(), IsActive: true, FeeBearer: bearer}
	merchantRepo.byID[m.ID] = m
	svc = svc.WithMerchantCallbackDeps(merchantRepo, newFakeMerchantCallbackRepo())
	return svc, paymentRepo, configRepo, router, m.ID
}

func sandboxFeeReq(id uuid.UUID, amount int64) *payment.CreatePaymentRequest {
	r := sandboxReq(id)
	r.Amount = amount
	return r
}

func TestCreatePayment_CustomerBearsFee_SendsQuotedTotal(t *testing.T) {
	// 2000 -> 2015 alone cannot tell 70 from 71 bps; 100000 -> 100705 can.
	cases := []struct{ nominal, total, fee int64 }{
		{2000, 2015, 15},
		{100000, 100705, 705},
	}
	for _, tc := range cases {
		svc, paymentRepo, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
		fp := &fakeProvider{name: "sandbox"}
		svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})

		p, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, tc.nominal))
		if err != nil {
			t.Fatalf("nominal %d: %v", tc.nominal, err)
		}
		if fp.lastCreateReq == nil || fp.lastCreateReq.Amount != tc.total {
			t.Errorf("nominal %d: provider received %+v, want Amount %d", tc.nominal, fp.lastCreateReq, tc.total)
		}
		if p.Amount != tc.nominal || p.FeeAmount != tc.fee || p.TotalAmount() != tc.total {
			t.Errorf("nominal %d: amount=%d fee=%d total=%d, want %d/%d/%d", tc.nominal, p.Amount, p.FeeAmount, p.TotalAmount(), tc.nominal, tc.fee, tc.total)
		}
		stored, err := paymentRepo.GetByID(context.Background(), p.ID)
		if err != nil || stored.Amount != tc.nominal || stored.FeeAmount != tc.fee {
			t.Errorf("nominal %d: stored %+v err %v", tc.nominal, stored, err)
		}
	}
}

func TestCreatePayment_CustomerBearsFee_ProviderChargesMore_FeeReflectsCharge(t *testing.T) {
	svc, _, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	fp := &fakeProvider{name: "sandbox", createResp: &domainProvider.ProviderPaymentResponse{
		ProviderReference: "R", ProviderName: "sandbox", Status: "pending", Amount: 2020}}
	svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})

	p, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, 2000))
	if err != nil {
		t.Fatal(err)
	}
	if p.FeeAmount != 20 || p.Amount != 2000 || p.TotalAmount() != 2020 {
		t.Errorf("amount=%d fee=%d total=%d, want 2000/20/2020", p.Amount, p.FeeAmount, p.TotalAmount())
	}
}

func TestCreatePayment_CustomerBearsFee_ProviderResponseWithoutAmount_UsesRequestedCharge(t *testing.T) {
	svc, _, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	fp := &fakeProvider{name: "sandbox", createResp: &domainProvider.ProviderPaymentResponse{
		ProviderReference: "R", ProviderName: "sandbox", Status: "pending"}}
	svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})

	p, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, 100000))
	if err != nil {
		t.Fatal(err)
	}
	if p.Amount != 100000 || p.FeeAmount != 705 {
		t.Errorf("amount=%d fee=%d, want 100000/705", p.Amount, p.FeeAmount)
	}
}

// In production cashi/client.go copies cashiResp.Amount, which is 0 when Cashi
// omits it. The fee must then come from the charge we requested; without that
// fallback a customer-bears payment is stored with fee 0 while the QR charges
// the total, and the genuine settlement would be rejected.
func TestCreatePayment_CustomerBearsFee_Production_ResponseWithoutAmount_UsesRequestedCharge(t *testing.T) {
	svc, paymentRepo, configRepo, router, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	fp := &fakeProvider{name: "cashi", createResp: &domainProvider.ProviderPaymentResponse{
		ProviderReference: "R", ProviderName: "cashi", Status: "pending", Amount: 0}}
	router.RegisterProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: mid, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, Priority: 1},
	}
	req := baseCreateRequest(mid)
	req.Amount = 100000
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Amount != 100000 || p.FeeAmount != 705 || p.TotalAmount() != 100705 {
		t.Fatalf("amount=%d fee=%d total=%d, want 100000/705/100705", p.Amount, p.FeeAmount, p.TotalAmount())
	}
	stored, err := paymentRepo.GetByID(context.Background(), p.ID)
	if err != nil || stored.FeeAmount != 705 {
		t.Fatalf("stored %+v err %v", stored, err)
	}

	// The genuine settlement (provider reports the total) must verify; the
	// bare nominal must not.
	total, nominal := int64(100705), int64(100000)
	if err := verifyProviderPayment(stored, &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: &total}, &total); err != nil {
		t.Errorf("settlement at the total must verify, got %v", err)
	}
	err = verifyProviderPayment(stored, &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: &nominal}, &nominal)
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Errorf("settlement at the nominal: want ErrAmountMismatch, got %v", err)
	}
}

func TestCreatePayment_CustomerBearsFee_ProductionFailover_ResponseWithoutAmount_UsesWinningCandidateCharge(t *testing.T) {
	svc, _, configRepo, router, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	first := &fakeQuoterProvider{fakeProvider: &fakeProvider{name: "cashi", createErr: providerPkg.ErrProviderAPIError}, bps: 70}
	second := &fakeQuoterProvider{fakeProvider: &fakeProvider{name: "midtrans", createResp: &domainProvider.ProviderPaymentResponse{
		ProviderReference: "R", ProviderName: "midtrans", Status: "pending", Amount: 0}}, bps: 100}
	router.RegisterProvider(first)
	router.RegisterProvider(second)
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: mid, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 1},
		{MerchantID: mid, ProviderName: "midtrans", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 2},
	}
	req := baseCreateRequest(mid)
	req.Amount = 100000
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// The fee is the winning (100 bps) candidate's, not the failed 70 bps one.
	if p.ProviderName != "midtrans" || p.Amount != 100000 || p.FeeAmount != 1011 {
		t.Errorf("provider=%s amount=%d fee=%d, want midtrans/100000/1011", p.ProviderName, p.Amount, p.FeeAmount)
	}
}

func TestCreatePayment_CustomerBearsFee_ProviderChargesBelowNominal_Rejected(t *testing.T) {
	svc, paymentRepo, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	fp := &fakeProvider{name: "sandbox", createResp: &domainProvider.ProviderPaymentResponse{
		ProviderReference: "R", ProviderName: "sandbox", Status: "pending", Amount: 1990}}
	svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})

	_, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, 2000))
	if !errors.Is(err, payment.ErrProviderError) {
		t.Fatalf("want ErrProviderError, got %v", err)
	}
	if len(paymentRepo.byID) != 0 {
		t.Error("no payment may be stored")
	}
}

func TestCreatePayment_CustomerBearsFee_ProviderWithoutQuoter_FailsClosed(t *testing.T) {
	t.Run("sandbox", func(t *testing.T) {
		svc, paymentRepo, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
		fp := &fakeProvider{name: "sandbox"}
		svc = svc.WithSandboxProvider(fp)
		_, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, 2000))
		if !errors.Is(err, providerPkg.ErrFeeNotSupported) {
			t.Fatalf("want ErrFeeNotSupported, got %v", err)
		}
		if fp.createCalls != 0 || len(paymentRepo.byID) != 0 {
			t.Errorf("createCalls=%d stored=%d, want 0/0", fp.createCalls, len(paymentRepo.byID))
		}
	})
	t.Run("production", func(t *testing.T) {
		svc, paymentRepo, _, router, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
		fp := &fakeProvider{name: "cashi"}
		router.RegisterProvider(fp)
		router.RegisterPaymentMethodProvider("qris", "cashi")
		req := baseCreateRequest(mid)
		req.Environment = payment.EnvironmentProduction
		_, err := svc.CreatePayment(context.Background(), req)
		if !errors.Is(err, providerPkg.ErrFeeNotSupported) {
			t.Fatalf("want ErrFeeNotSupported, got %v", err)
		}
		if fp.createCalls != 0 || len(paymentRepo.byID) != 0 {
			t.Errorf("createCalls=%d stored=%d, want 0/0", fp.createCalls, len(paymentRepo.byID))
		}
	})
}

func TestCreatePayment_CustomerBearsFee_ProductionFailover_QuotesPerCandidate(t *testing.T) {
	svc, _, configRepo, router, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	first := &fakeQuoterProvider{fakeProvider: &fakeProvider{name: "cashi", createErr: providerPkg.ErrProviderAPIError}, bps: 70}
	second := &fakeQuoterProvider{fakeProvider: &fakeProvider{name: "midtrans"}, bps: 100}
	router.RegisterProvider(first)
	router.RegisterProvider(second)
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: mid, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 1},
		{MerchantID: mid, ProviderName: "midtrans", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 2},
	}
	req := baseCreateRequest(mid)
	req.Amount = 100000
	req.Environment = payment.EnvironmentProduction

	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// 100 bps: smallest G with G - ceil(G/100) >= 100000 is 101011.
	if first.lastCreateReq == nil || first.lastCreateReq.Amount != 100705 {
		t.Errorf("first candidate got %+v, want Amount 100705 (70 bps)", first.lastCreateReq)
	}
	if second.lastCreateReq == nil || second.lastCreateReq.Amount != 101011 {
		t.Errorf("second candidate got %+v, want Amount 101011 (100 bps)", second.lastCreateReq)
	}
	if p.ProviderName != "midtrans" || p.Amount != 100000 || p.FeeAmount != 1011 {
		t.Errorf("provider=%s amount=%d fee=%d, want midtrans/100000/1011", p.ProviderName, p.Amount, p.FeeAmount)
	}
}

func TestCreatePayment_CustomerBearsFee_Production_FailoverSkipsCandidateWithoutQuoter(t *testing.T) {
	svc, _, configRepo, router, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	plain := &fakeProvider{name: "cashi"}
	quoter := &fakeQuoterProvider{fakeProvider: &fakeProvider{name: "midtrans"}, bps: 70}
	router.RegisterProvider(plain)
	router.RegisterProvider(quoter)
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: mid, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 1},
		{MerchantID: mid, ProviderName: "midtrans", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 2},
	}
	req := baseCreateRequest(mid)
	req.Environment = payment.EnvironmentProduction
	p, err := svc.CreatePayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plain.createCalls != 0 || p.ProviderName != "midtrans" {
		t.Errorf("plain calls=%d provider=%s, want 0/midtrans", plain.createCalls, p.ProviderName)
	}
}

func TestCreatePayment_CustomerBearsFee_Production_FailoverDisabled_QuoteFailureStops(t *testing.T) {
	svc, paymentRepo, configRepo, router, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	plain := &fakeProvider{name: "cashi"}
	quoter := &fakeQuoterProvider{fakeProvider: &fakeProvider{name: "midtrans"}, bps: 70}
	router.RegisterProvider(plain)
	router.RegisterProvider(quoter)
	configRepo.configs = []*domainProvider.MerchantProviderConfig{
		{MerchantID: mid, ProviderName: "cashi", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: false, Priority: 1},
		{MerchantID: mid, ProviderName: "midtrans", PaymentMethod: "qris", IsEnabled: true, FailoverEnabled: true, Priority: 2},
	}
	req := baseCreateRequest(mid)
	req.Environment = payment.EnvironmentProduction
	_, err := svc.CreatePayment(context.Background(), req)
	if !errors.Is(err, providerPkg.ErrFeeNotSupported) {
		t.Fatalf("want ErrFeeNotSupported, got %v", err)
	}
	if quoter.createCalls != 0 || len(paymentRepo.byID) != 0 {
		t.Errorf("failover-disabled candidate must stop: quoter calls=%d stored=%d", quoter.createCalls, len(paymentRepo.byID))
	}
}

func TestCreatePayment_MerchantBearsFee_Unchanged(t *testing.T) {
	// Every shape that must behave exactly as before the fee feature: no
	// merchant repo, explicit merchant, empty and unknown bearer values, and
	// an unknown merchant. A quoter provider is used so that sending the
	// quoted total (the bug this guards against) is observable.
	cases := []struct {
		name   string
		bearer string
		repo   bool
		known  bool
	}{
		{"no merchant repo", "", false, false},
		{"explicit merchant", merchant.FeeBearerMerchant, true, true},
		{"empty bearer", "", true, true},
		{"unknown bearer", "something-else", true, true},
		{"unknown merchant", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestPaymentService()
			mid := uuid.New()
			if tc.repo {
				repo := newFakeMerchantRepo()
				if tc.known {
					repo.byID[mid] = &merchant.Merchant{ID: mid, IsActive: true, FeeBearer: tc.bearer}
				}
				svc = svc.WithMerchantCallbackDeps(repo, newFakeMerchantCallbackRepo())
			}
			fp := &fakeProvider{name: "sandbox"}
			svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})

			p, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, 100000))
			if err != nil {
				t.Fatal(err)
			}
			if fp.lastCreateReq.Amount != 100000 {
				t.Errorf("provider received %d, want the bare nominal 100000", fp.lastCreateReq.Amount)
			}
			if p.FeeAmount != 0 || p.Amount != 100000 || p.TotalAmount() != 100000 {
				t.Errorf("amount=%d fee=%d, want 100000/0", p.Amount, p.FeeAmount)
			}
		})
	}
}

func TestCreatePayment_MerchantBearsFee_ProviderAmountStillOverwrites(t *testing.T) {
	// Nil merchant repo and an explicit merchant-bearer merchant must both
	// keep the legacy behaviour of storing the provider's amount.
	cases := []struct {
		name string
		repo bool
	}{{"no merchant repo", false}, {"explicit merchant bearer", true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestPaymentService()
			mid := uuid.New()
			if tc.repo {
				repo := newFakeMerchantRepo()
				repo.byID[mid] = &merchant.Merchant{ID: mid, IsActive: true, FeeBearer: merchant.FeeBearerMerchant}
				svc = svc.WithMerchantCallbackDeps(repo, newFakeMerchantCallbackRepo())
			}
			fp := &fakeProvider{name: "sandbox", createResp: &domainProvider.ProviderPaymentResponse{
				ProviderReference: "R", ProviderName: "sandbox", Status: "pending", Amount: 15007}}
			svc = svc.WithSandboxProvider(fp)
			p, err := svc.CreatePayment(context.Background(), sandboxReq(mid))
			if err != nil {
				t.Fatal(err)
			}
			if p.Amount != 15007 || p.FeeAmount != 0 {
				t.Errorf("amount=%d fee=%d, want legacy overwrite 15007/0", p.Amount, p.FeeAmount)
			}
		})
	}
}

func TestCreatePayment_CustomerBearsFee_IdempotentReplay(t *testing.T) {
	svc, paymentRepo, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	fp := &fakeProvider{name: "sandbox"}
	svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70}).WithIdempotency(newFakeIdempotencyRepo())

	a, err := svc.CreatePaymentIdempotent(context.Background(), sandboxFeeReq(mid, 100000), "k1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreatePaymentIdempotent(context.Background(), sandboxFeeReq(mid, 100000), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Replayed || a.Payment.ID != b.Payment.ID {
		t.Fatalf("second call must replay the first (replayed=%v)", b.Replayed)
	}
	if fp.createCalls != 1 || len(paymentRepo.byID) != 1 {
		t.Errorf("createCalls=%d stored=%d, want 1/1", fp.createCalls, len(paymentRepo.byID))
	}
	if b.Payment.FeeAmount != 705 || b.Payment.Amount != 100000 {
		t.Errorf("replayed amount=%d fee=%d, want 100000/705", b.Payment.Amount, b.Payment.FeeAmount)
	}
}

func TestCreatePayment_InactiveCustomerBearerMerchantStillRejected(t *testing.T) {
	svc, _, _, _, mid := feeMerchantSvc(t, merchant.FeeBearerCustomer)
	repo := newFakeMerchantRepo()
	repo.byID[mid] = &merchant.Merchant{ID: mid, IsActive: false, FeeBearer: merchant.FeeBearerCustomer}
	svc = svc.WithMerchantCallbackDeps(repo, newFakeMerchantCallbackRepo())
	fp := &fakeProvider{name: "sandbox"}
	svc = svc.WithSandboxProvider(&fakeQuoterProvider{fakeProvider: fp, bps: 70})
	_, err := svc.CreatePayment(context.Background(), sandboxFeeReq(mid, 2000))
	if !errors.Is(err, merchant.ErrMerchantInactive) || fp.createCalls != 0 {
		t.Fatalf("err=%v calls=%d, want ErrMerchantInactive/0", err, fp.createCalls)
	}
}

// ---- verification of fee-bearing payments through every settle path ----

func paymentRepoMu(repo *fakePaymentRepo, id uuid.UUID, fn func(*payment.Payment)) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	fn(repo.byID[id])
}

func seedFeePayment(t *testing.T, repo *fakePaymentRepo, ref string) *payment.Payment {
	t.Helper()
	p := seedPayment(t, repo, payment.StatusPending, ref)
	// The fake repo stores a copy, so adjust the stored row directly.
	paymentRepoMu(repo, p.ID, func(stored *payment.Payment) { stored.Amount, stored.FeeAmount = 100000, 705 })
	p.Amount, p.FeeAmount = 100000, 705
	return p
}

func TestProcessWebhook_FeeBearingPayment_SettlesOnTotal(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedFeePayment(t, paymentRepo, "REF-FEE-OK")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-FEE-OK", ptrInt64(100705)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(100705)},
	})
	if err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig"); err != nil {
		t.Fatalf("a genuine paid fee-bearing payment must settle: %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), seeded.ID); got.Status != payment.StatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
}

func TestProcessWebhook_FeeBearingPayment_NominalAtProviderRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedFeePayment(t, paymentRepo, "REF-FEE-NOM")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-FEE-NOM", ptrInt64(100000)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(100000)},
	})
	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("want ErrAmountMismatch, got %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), seeded.ID); got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestProcessWebhook_FeeBearingPayment_WebhookNominalRejected(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	seeded := seedFeePayment(t, paymentRepo, "REF-FEE-WH")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		parseResp:  paidWebhook("REF-FEE-WH", ptrInt64(100000)),
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(100705)},
	})
	err := svc.ProcessWebhook(context.Background(), "cashi", []byte(`{}`), "sig")
	if !errors.Is(err, payment.ErrWebhookAmountMismatch) {
		t.Fatalf("want ErrWebhookAmountMismatch, got %v", err)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), seeded.ID); got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestReconcilePayment_FeeBearingPayment_SettlesOnTotal(t *testing.T) {
	svc, paymentRepo, _, router := newWebhookTestService()
	p := seedFeePayment(t, paymentRepo, "REF-FEE-REC")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(100705)}})
	res, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action == "error" {
		t.Fatalf("genuine fee-bearing payment rejected by reconcile: %s", res.Message)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != payment.StatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
}

func TestReconcilePayment_FeeBearingPayment_NominalMismatchRecordsTotalAsExpected(t *testing.T) {
	svc, paymentRepo, webhookRepo, router := newWebhookTestService()
	p := seedFeePayment(t, paymentRepo, "REF-FEE-RMM")
	router.RegisterProvider(&fakeProvider{name: "cashi",
		statusResp: &domainProvider.NormalizedPaymentStatus{Status: payment.StatusPaid, Amount: ptrInt64(100000)}})
	res, err := svc.ReconcilePayment(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "error" {
		t.Fatalf("action = %q, want error", res.Action)
	}
	if got, _ := paymentRepo.GetByID(context.Background(), p.ID); got.Status != payment.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	e := lastEvent(webhookRepo)
	if e == nil || e.RawPayload["expected_amount"] != int64(100705) {
		t.Fatalf("event expected_amount = %+v, want 100705", e)
	}
}
