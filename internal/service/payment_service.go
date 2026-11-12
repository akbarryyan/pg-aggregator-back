package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

type PaymentService struct {
	paymentRepo                paymentRepository
	merchantRepo               merchantRepository
	merchantProviderConfigRepo merchantProviderConfigRepository
	webhookEventRepo           webhookEventRepository
	callbackRepo               merchantCallbackRepository
	idempotencyRepo            idempotencyRepository
	providerRouter             *providerPkg.ProviderRouter
	sandboxProvider            providerPkg.PaymentProvider
	appBaseURL                 string

	// Merchant-callback SSRF guards, as fields so tests can reach httptest
	// servers on loopback. Production always uses netsafe: one client whose
	// dial-time IP check is always on; a payment's environment only decides
	// whether plain http is allowed (callbackValidate's allowHTTP).
	callbackValidate func(raw string, allowHTTP bool) error
	callbackClient   func() *http.Client
	// callbackBatchBudget bounds how long one DeliverMerchantCallbacks run
	// keeps sending (tests shrink it); see defaultCallbackBatchBudget.
	callbackBatchBudget time.Duration

	// Rotating position of the reconcile scan (see ReconcileRecentPayments).
	reconcileMu     sync.Mutex
	reconcileCursor *payment.ReconcileCursor
	reconcileBatch  int // tests override; defaults to reconcileBatchSize
}

type providerCandidate struct {
	provider        providerPkg.PaymentProvider
	failoverEnabled bool
}

func NewPaymentService(
	paymentRepo paymentRepository,
	merchantProviderConfigRepo merchantProviderConfigRepository,
	webhookEventRepo webhookEventRepository,
	providerRouter *providerPkg.ProviderRouter,
	appBaseURL string,
) *PaymentService {
	callbackHTTP := netsafe.NewSafeClient(10 * time.Second) // one client; the dial-time IP guard is always on
	return &PaymentService{
		paymentRepo:                paymentRepo,
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		webhookEventRepo:           webhookEventRepo,
		providerRouter:             providerRouter,
		appBaseURL:                 strings.TrimRight(appBaseURL, "/"),
		reconcileBatch:             reconcileBatchSize,
		callbackValidate:           netsafe.ValidateCallbackURL,
		callbackClient:             func() *http.Client { return callbackHTTP },
		callbackBatchBudget:        defaultCallbackBatchBudget,
	}
}

// QRImageURL is the public PNG URL of a sandbox payment's QR ("" otherwise).
func (s *PaymentService) QRImageURL(p *payment.Payment) string {
	return payment.SandboxQRImageURL(p, s.appBaseURL)
}

// WithMerchantCallbackDeps wires merchant lookup + outbound callback persistence.
func (s *PaymentService) WithMerchantCallbackDeps(
	merchantRepo merchantRepository,
	callbackRepo merchantCallbackRepository,
) *PaymentService {
	s.merchantRepo = merchantRepo
	s.callbackRepo = callbackRepo
	return s
}

// WithSandboxProvider sets the mock provider used for environment=sandbox (no external HTTP).
func (s *PaymentService) WithSandboxProvider(p providerPkg.PaymentProvider) *PaymentService {
	s.sandboxProvider = p
	return s
}

// checkMerchantForCreate is the single choke point for every payment-creation
// path (API key, dashboard, public payment-link checkout, admin): a merchant
// deactivated after a token or link was issued must not create new payments.
// It also reports who bears the provider fee. An unknown merchant is left to
// the foreign key; with no merchant repo (unit-test wiring) the check is
// skipped and the merchant bears the fee, as before. Any bearer value other
// than an explicit "customer" means the merchant bears it.
func (s *PaymentService) checkMerchantForCreate(ctx context.Context, merchantID uuid.UUID) (feeBearer string, err error) {
	if s.merchantRepo == nil {
		return merchant.FeeBearerMerchant, nil
	}
	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil {
		if errors.Is(err, merchant.ErrMerchantNotFound) {
			return merchant.FeeBearerMerchant, nil
		}
		return "", fmt.Errorf("failed to load merchant: %w", err)
	}
	if !m.IsActive {
		return "", merchant.ErrMerchantInactive
	}
	if m.FeeBearer == merchant.FeeBearerCustomer {
		return merchant.FeeBearerCustomer, nil
	}
	return merchant.FeeBearerMerchant, nil
}

// MerchantDisplayName is the name shown to a customer on the public checkout:
// the merchant's business name, falling back to its name. It is display-only
// and best-effort: an unwired lookup, an unknown merchant or a lookup error
// yield "" (the page then simply omits the name) rather than failing the
// checkout. A deactivated merchant is still named, because the payment exists.
func (s *PaymentService) MerchantDisplayName(ctx context.Context, merchantID uuid.UUID) string {
	if s.merchantRepo == nil {
		return ""
	}
	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil || m == nil {
		return ""
	}
	if name := strings.TrimSpace(m.BusinessName); name != "" {
		return name
	}
	return strings.TrimSpace(m.Name)
}

// chargeFor returns the amount to send to prov for a payment of nominal: the
// bare nominal, or - when the customer bears the fee - the provider's quoted
// total. A provider that cannot quote fails closed (ErrFeeNotSupported).
func chargeFor(prov providerPkg.PaymentProvider, feeBearer string, nominal int64) (int64, error) {
	if feeBearer != merchant.FeeBearerCustomer {
		return nominal, nil
	}
	q, ok := prov.(providerPkg.FeeQuoter)
	if !ok {
		return 0, fmt.Errorf("provider %s: %w", prov.GetName(), providerPkg.ErrFeeNotSupported)
	}
	total, _, err := q.QuoteTotal(nominal)
	if err != nil {
		return 0, fmt.Errorf("provider %s fee quote: %w", prov.GetName(), err)
	}
	return total, nil
}

func (s *PaymentService) CreatePayment(ctx context.Context, req *payment.CreatePaymentRequest) (*payment.Payment, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	feeBearer, err := s.checkMerchantForCreate(ctx, req.MerchantID)
	if err != nil {
		return nil, err
	}

	env := req.Environment // already resolved by req.Validate()
	reference := s.generateReference()
	expiresAt := time.Now().Add(time.Duration(req.ExpiresInMinutes) * time.Minute)

	p := &payment.Payment{
		ID:            uuid.New(),
		Reference:     reference,
		MerchantID:    req.MerchantID,
		Amount:        req.Amount,
		Currency:      req.Currency,
		PaymentMethod: req.PaymentMethod,
		Status:        payment.StatusPending,
		Description:   req.Description,
		CustomerName:  req.CustomerName,
		CustomerEmail: req.CustomerEmail,
		CallbackURL:   req.CallbackURL,
		Environment:   env,
		PaymentLinkID: req.PaymentLinkID,
		ExpiresAt:     expiresAt,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	var (
		providerResp *provider.ProviderPaymentResponse
		lastErr      error
		// requestedCharge is what the winning provider was asked to charge:
		// the nominal, or the quoted total when the customer bears the fee.
		requestedCharge = req.Amount
	)

	// Sandbox never calls real providers (Cashi, etc.)
	if env == payment.EnvironmentSandbox {
		if s.sandboxProvider == nil {
			return nil, fmt.Errorf("sandbox provider is not configured")
		}
		logger.InfofCtx(ctx, "Creating SANDBOX payment (mock provider, no Cashi call): %s", reference)
		charge, chargeErr := chargeFor(s.sandboxProvider, feeBearer, req.Amount)
		if chargeErr != nil {
			return nil, chargeErr
		}
		requestedCharge = charge
		providerReq := &provider.ProviderPaymentRequest{
			InternalReference:     reference,
			Amount:                charge,
			Currency:              req.Currency,
			Description:           req.Description,
			CustomerName:          req.CustomerName,
			CustomerEmail:         req.CustomerEmail,
			ExpiresAt:             expiresAt,
			CallbackURL:           s.buildCallbackURL(s.sandboxProvider.GetName()),
			UseCustomMerchantName: req.UseCustomMerchantName,
		}
		providerResp, err = s.sandboxProvider.CreatePayment(ctx, providerReq)
		if err != nil {
			return nil, fmt.Errorf("sandbox payment failed: %w", err)
		}
	} else {
		candidateProviders, resolveErr := s.resolveProvidersForMerchant(ctx, req.MerchantID, req.PaymentMethod)
		if resolveErr != nil {
			return nil, fmt.Errorf("failed to select provider: %w", resolveErr)
		}

		for _, candidate := range candidateProviders {
			selectedProvider := candidate.provider
			// Quote per candidate: providers have different fee rates.
			charge, chargeErr := chargeFor(selectedProvider, feeBearer, req.Amount)
			if chargeErr != nil {
				providerResp = nil
				lastErr = chargeErr
				logger.ErrorfCtx(ctx, "Provider %s cannot quote a customer-borne fee: %v", selectedProvider.GetName(), chargeErr)
				if !candidate.failoverEnabled {
					break
				}
				continue
			}
			providerReq := &provider.ProviderPaymentRequest{
				InternalReference:     reference,
				Amount:                charge,
				Currency:              req.Currency,
				Description:           req.Description,
				CustomerName:          req.CustomerName,
				CustomerEmail:         req.CustomerEmail,
				ExpiresAt:             expiresAt,
				CallbackURL:           s.buildCallbackURL(selectedProvider.GetName()),
				UseCustomMerchantName: req.UseCustomMerchantName,
			}

			logger.InfofCtx(ctx, "Creating PRODUCTION payment with provider: %s", selectedProvider.GetName())

			providerResp, err = selectedProvider.CreatePayment(ctx, providerReq)
			if err == nil && providerResp == nil {
				// A (nil, nil) result is a provider bug; treat it as an error
				// so failover rules apply and nothing dereferences nil.
				err = fmt.Errorf("provider %s returned no response: %w", selectedProvider.GetName(), providerPkg.ErrProviderAPIError)
			}
			if err == nil {
				if req.UseCustomMerchantName {
					// Confirms whether the provider actually honored the
					// custom-merchant-name request (e.g. Cashi silently
					// falls back to its default name if QRIS Custom isn't
					// enabled on that account yet — see
					// docs/cashi-qris-custom.md). This is otherwise
					// invisible: RawResponse isn't persisted or returned
					// to the API caller.
					logger.InfofCtx(ctx,
						"Provider %s custom-merchant-name request for %s: is_qris_custom=%v expected_net=%v",
						selectedProvider.GetName(), reference,
						providerResp.RawResponse["is_qris_custom"], providerResp.RawResponse["expected_net"],
					)
				}
				requestedCharge = charge
				break
			}

			// Drop any response returned alongside an error so a failed
			// attempt can never be persisted as a payment.
			providerResp = nil
			lastErr = err
			logger.ErrorfCtx(ctx, "Provider %s error: %v", selectedProvider.GetName(), err)
			if !candidate.failoverEnabled {
				break
			}
		}

		if providerResp == nil {
			// Every candidate failed (or failover stopped after the first
			// error). Never fall through to the nil response below.
			if lastErr == nil {
				lastErr = providerPkg.ErrProviderNotAvailable
			}
			return nil, fmt.Errorf("failed to create payment with available providers: %w", lastErr)
		}
	}

	p.ProviderName = providerResp.ProviderName
	p.ProviderReference = &providerResp.ProviderReference
	p.QRISData = providerResp.QRISData
	p.Status = providerResp.Status
	charged := providerResp.Amount
	if charged <= 0 {
		charged = requestedCharge
	}
	if feeBearer == merchant.FeeBearerCustomer {
		// The merchant's nominal never changes; the fee is whatever the
		// provider actually charged on top of it.
		if charged < req.Amount {
			return nil, fmt.Errorf("provider %s charged %d, below the nominal %d: %w",
				providerResp.ProviderName, charged, req.Amount, payment.ErrProviderError)
		}
		p.FeeAmount = charged - req.Amount
	} else if providerResp.Amount > 0 {
		p.Amount = providerResp.Amount // unchanged legacy behaviour
	}
	if !providerResp.ExpiresAt.IsZero() {
		p.ExpiresAt = providerResp.ExpiresAt
	}

	if err := s.paymentRepo.Create(ctx, p); err != nil {
		logger.ErrorfCtx(ctx, "Failed to save payment: %v", err)
		return nil, fmt.Errorf("failed to save payment: %w", err)
	}

	logger.InfofCtx(ctx, "Payment created successfully: %s (provider: %s)", p.Reference, p.ProviderName)
	return p, nil
}

func (s *PaymentService) GetPayment(ctx context.Context, id uuid.UUID) (*payment.Payment, error) {
	p, err := s.paymentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PaymentService) GetPaymentByReference(ctx context.Context, reference string) (*payment.Payment, error) {
	p, err := s.paymentRepo.GetByReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PaymentService) GetPaymentStatus(ctx context.Context, id uuid.UUID) (*payment.PaymentStatusResponse, error) {
	p, err := s.paymentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return payment.ToPaymentStatusResponse(p), nil
}

func (s *PaymentService) CheckPaymentStatus(ctx context.Context, reference string) (*payment.Payment, error) {
	p, err := s.paymentRepo.GetByReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	result, err := s.ReconcilePayment(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return s.paymentRepo.GetByID(ctx, result.PaymentID)
}

// ReconcileResult is the outcome of an admin/provider status check for one payment.
type ReconcileResult struct {
	PaymentID        uuid.UUID `json:"payment_id"`
	Reference        string    `json:"reference"`
	PreviousStatus   string    `json:"previous_status"`
	CurrentStatus    string    `json:"current_status"`
	ProviderStatus   *string   `json:"provider_status,omitempty"`
	Action           string    `json:"action"` // updated | unchanged | expired_local | skipped | error
	Message          string    `json:"message"`
	MerchantNotified bool      `json:"merchant_notified"`
}

// ReconcilePayment polls the provider (when possible) and syncs local status.
// Also applies local expire when expires_at has passed and status is still pending.
func (s *PaymentService) ReconcilePayment(ctx context.Context, id uuid.UUID) (*ReconcileResult, error) {
	p, err := s.paymentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.reconcilePaymentData(ctx, p)
}

// reconcilePaymentData is ReconcilePayment's body operating on an
// already-fetched payment, so batch callers that already have the row (e.g.
// ReconcilePendingPayments, which lists via ListAdmin) don't re-fetch it by
// ID — that was a redundant SELECT per payment for every batch reconcile.
func (s *PaymentService) reconcilePaymentData(ctx context.Context, p *payment.Payment) (*ReconcileResult, error) {
	result := &ReconcileResult{
		PaymentID:      p.ID,
		Reference:      p.Reference,
		PreviousStatus: p.Status,
		CurrentStatus:  p.Status,
		Action:         "unchanged",
		Message:        "Status unchanged",
	}

	switch p.Status {
	case payment.StatusPaid, payment.StatusFailed, payment.StatusCancelled, payment.StatusRefunded:
		result.Action = "skipped"
		result.Message = "Payment already in terminal status"
		return result, nil
	}

	// A pending payment past its deadline (plus expireGracePeriod, as in the
	// expire job) may be expired locally; an expired one is only re-checked
	// for a late settlement and never changed locally.
	pastExpiry := p.Status == payment.StatusPending && time.Now().After(p.ExpiresAt.Add(expireGracePeriod))
	hasRef := p.ProviderReference != nil && *p.ProviderReference != ""

	if !hasRef {
		if pastExpiry {
			return s.expireLocally(ctx, p, result, "Marked expired locally (no provider reference; expires_at passed)")
		}
		result.Action = "skipped"
		result.Message = "No provider reference — cannot poll provider"
		return result, nil
	}

	selectedProvider, err := s.getProviderByName(p.ProviderName)
	if err != nil {
		result.Action = "error"
		result.Message = err.Error()
		return result, nil
	}

	// Debug: the automatic job runs this every 2 minutes per candidate; the
	// per-run summary in ReconcileRecentPayments stays at Info.
	logger.DebugfCtx(ctx, "Reconciling payment %s with provider %s ref %s", p.Reference, p.ProviderName, *p.ProviderReference)

	providerStatus, err := s.checkProviderStatus(ctx, selectedProvider, *p.ProviderReference)
	if err != nil {
		logger.WarnfCtx(ctx, "Failed to check provider status for %s: %v", p.Reference, err)
		if pastExpiry {
			return s.expireLocally(ctx, p, result, fmt.Sprintf("Provider check failed (%v); marked expired locally (expires_at passed)", err))
		}
		result.Action = "error"
		result.Message = fmt.Sprintf("Provider check failed: %v", err)
		return result, nil
	}

	ps := providerStatus.Status
	result.ProviderStatus = &ps

	if providerStatus.Status != payment.StatusPaid {
		if pastExpiry {
			return s.expireLocally(ctx, p, result, "Provider has not settled; marked expired locally (expires_at passed)")
		}
		result.Message = "Provider has not settled the payment"
		return result, nil
	}

	if verifyErr := verifyProviderPayment(p, providerStatus, nil, false); verifyErr != nil {
		logger.ErrorfCtx(ctx, "Payment %s: %v", p.Reference, verifyErr)
		s.recordReconcileMismatch(ctx, p, providerStatus, verifyErr)
		result.Action = "error"
		result.Message = verifyErr.Error()
		return result, nil
	}

	paidAt := providerStatus.PaidAt
	if paidAt == nil {
		now := time.Now()
		paidAt = &now
	}
	// The settlement is confirmed: finish the update and the merchant
	// callback even if the job is cancelled now (a paid payment is never
	// revisited, so a dropped callback would be lost for good).
	ctx, cancel := detached(ctx)
	defer cancel()
	net, err := merchantNetFor(p)
	if err != nil {
		logger.ErrorfCtx(ctx, "Payment %s: cannot compute merchant net: %v", p.Reference, err)
		return nil, err
	}
	previous, err := s.paymentRepo.MarkPaid(ctx, p.ID, paidAt, net)
	if err != nil {
		if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			result.Action = "skipped"
			result.Message = "Payment changed concurrently"
			return result, nil
		}
		return nil, err
	}

	late := previous == payment.StatusExpired
	p.Status = payment.StatusPaid
	p.PaidAt = paidAt
	p.MerchantNetAmount = net
	result.CurrentStatus = payment.StatusPaid
	result.Action = "updated"
	result.Message = fmt.Sprintf("Status updated from %s to paid via provider", previous)
	if late {
		result.Message += " (late payment)"
	}
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusPaid), late)
	result.MerchantNotified = true
	return result, nil
}

// expireLocally marks a pending payment expired after its deadline. Losing a
// race to a concurrent settlement is reported in the result, not an error.
func (s *PaymentService) expireLocally(ctx context.Context, p *payment.Payment, result *ReconcileResult, message string) (*ReconcileResult, error) {
	// Detached so a cancellation can't commit the expiry and then drop its
	// merchant callback.
	ctx, cancel := detached(ctx)
	defer cancel()
	if _, err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {
		if errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			result.Action = "skipped"
			result.Message = "Payment changed concurrently"
			return result, nil
		}
		return nil, err
	}
	p.Status = payment.StatusExpired
	result.CurrentStatus = payment.StatusExpired
	result.Action = "expired_local"
	result.Message = message
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusExpired), false)
	result.MerchantNotified = true
	return result, nil
}

// recordReconcileMismatch stores an amount mismatch found by reconcile as a
// webhook_events row, so it shows up in the admin logs/notifications like a
// webhook mismatch and excludes the payment from later automatic runs.
func (s *PaymentService) recordReconcileMismatch(ctx context.Context, p *payment.Payment, status *provider.NormalizedPaymentStatus, cause error) {
	msg := cause.Error()
	pid := p.ID
	var providerAmount interface{}
	if status.Amount != nil {
		providerAmount = *status.Amount
	}
	ev := &provider.WebhookEvent{
		PaymentID:         &pid,
		ProviderName:      p.ProviderName,
		ProviderReference: *p.ProviderReference,
		EventType:         "reconcile.amount_mismatch",
		Status:            "rejected",
		RawPayload: map[string]interface{}{
			"source":          "reconcile",
			"provider_status": status.Status,
			"provider_amount": providerAmount,
			"expected_amount": p.TotalAmount(),
		},
		IsProcessed:     false,
		ProcessingError: &msg,
	}
	if err := s.webhookEventRepo.Create(ctx, ev); err != nil {
		logger.ErrorfCtx(ctx, "Failed to record reconcile amount mismatch for %s: %v", p.Reference, err)
	}
}

// Automatic reconcile (scheduler job "reconcile-payments"). Cashi doesn't
// document webhook retries, so this is the safety net that settles a payment
// whose webhook was lost or failed verification, and catches late
// settlements of payments that had already expired.
const (
	ReconcileInterval      = 2 * time.Minute
	reconcilePendingWindow = 24 * time.Hour // pending: by created_at
	reconcileExpiredWindow = 6 * time.Hour  // expired: by expires_at — a QRIS settlement later than this is implausible; admin reconcile remains for stragglers
	reconcileBatchSize     = 50
)

// ReconcileRecentPayments re-checks recent pending/expired production
// payments with their provider. Per-payment problems are logged and skipped
// so one bad payment never stops the batch.
//
// Each run checks one batch and remembers where it stopped, so the whole
// window is covered every ceil(N/batch) runs instead of only the newest
// batch being re-checked forever (older payments' late settlements and lost
// webhooks would otherwise be missed). The cursor is in memory: a restart
// just starts again from the newest, which is harmless.
func (s *PaymentService) ReconcileRecentPayments(ctx context.Context) error {
	// The scheduler runs this serially, but the method is exported.
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	candidates, err := s.paymentRepo.ListReconcileCandidates(ctx, time.Now(), reconcilePendingWindow, reconcileExpiredWindow, s.reconcileBatch, s.reconcileCursor)
	if err != nil {
		return fmt.Errorf("failed to list reconcile candidates: %w", err)
	}
	// A short batch means the scan reached the end of the window: wrap.
	// Otherwise advance past the last row even if some payments below fail,
	// so one bad payment never pins the scan.
	wrapped := len(candidates) < s.reconcileBatch
	if wrapped {
		s.reconcileCursor = nil
	} else {
		last := candidates[len(candidates)-1]
		s.reconcileCursor = &payment.ReconcileCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	changed := 0
	for _, p := range candidates {
		r, err := s.reconcilePaymentData(ctx, p)
		if err != nil {
			logger.ErrorfCtx(ctx, "reconcile-payments: payment %s: %v", p.Reference, err)
			continue
		}
		if r.Action == "updated" || r.Action == "expired_local" {
			changed++
		}
	}
	if len(candidates) > 0 {
		logger.InfofCtx(ctx, "reconcile-payments: checked %d, changed %d, wrapped %t", len(candidates), changed, wrapped)
	}
	return nil
}

// ReconcilePendingPayments checks up to limit pending payments (admin batch).
func (s *PaymentService) ReconcilePendingPayments(ctx context.Context, limit int) ([]*ReconcileResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.paymentRepo.ListAdmin(ctx, payment.StatusPending, "", nil, nil, nil, "", limit, 0)
	if err != nil {
		return nil, err
	}
	results := make([]*ReconcileResult, 0, len(rows))
	for _, row := range rows {
		// row.Payment is already fetched via ListAdmin — reconcile it
		// directly instead of re-fetching by ID (see reconcilePaymentData).
		r, err := s.reconcilePaymentData(ctx, row.Payment)
		if err != nil {
			msg := err.Error()
			results = append(results, &ReconcileResult{
				PaymentID:      row.Payment.ID,
				Reference:      row.Payment.Reference,
				PreviousStatus: row.Payment.Status,
				CurrentStatus:  row.Payment.Status,
				Action:         "error",
				Message:        msg,
			})
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

func (s *PaymentService) generateReference() string {
	timestamp := time.Now().Unix()
	return fmt.Sprintf("PAY-%d-%s", timestamp, uuid.New().String()[:8])
}

func (s *PaymentService) buildCallbackURL(providerName string) string {
	return fmt.Sprintf("%s/api/v1/provider-webhooks/%s", s.appBaseURL, providerName)
}

func (s *PaymentService) getProviderByName(name string) (providerPkg.PaymentProvider, error) {
	selectedProvider, exists := s.providerRouter.GetProvider(name)
	if !exists {
		return nil, fmt.Errorf("provider %q not registered: %w", name, providerPkg.ErrProviderNotAvailable)
	}
	return selectedProvider, nil
}

func (s *PaymentService) resolveProvidersForMerchant(ctx context.Context, merchantID uuid.UUID, paymentMethod string) ([]providerCandidate, error) {
	merchantConfigs, err := s.merchantProviderConfigRepo.ListEnabledByMerchantAndPaymentMethod(ctx, merchantID, paymentMethod)
	if err != nil {
		return nil, err
	}

	if len(merchantConfigs) == 0 {
		defaultProviders, err := s.providerRouter.SelectProviders(paymentMethod)
		if err != nil {
			return nil, err
		}

		candidates := make([]providerCandidate, 0, len(defaultProviders))
		for _, defaultProvider := range defaultProviders {
			// Never route production traffic to the sandbox mock.
			if defaultProvider.GetName() == provider.ProviderSandbox {
				continue
			}
			candidates = append(candidates, providerCandidate{
				provider:        defaultProvider,
				failoverEnabled: true,
			})
		}
		if len(candidates) == 0 {
			return nil, providerPkg.ErrProviderNotAvailable
		}
		return candidates, nil
	}

	selectedProviders := make([]providerCandidate, 0, len(merchantConfigs))
	for _, merchantConfig := range merchantConfigs {
		selectedProvider, exists := s.providerRouter.GetProvider(merchantConfig.ProviderName)
		if !exists {
			logger.WarnfCtx(ctx,
				"Skipping unregistered provider %s for merchant %s and payment method %s",
				merchantConfig.ProviderName,
				merchantConfig.MerchantID,
				merchantConfig.PaymentMethod,
			)
			continue
		}

		// Compare the resolved provider, not the stored string: GetProvider
		// normalizes case/whitespace, so " Sandbox " would bypass a name check.
		if selectedProvider.GetName() == provider.ProviderSandbox {
			logger.WarnfCtx(ctx, "Skipping sandbox provider in production config for merchant %s", merchantConfig.MerchantID)
			continue
		}

		health := s.providerRouter.GetProviderHealth(merchantConfig.ProviderName)
		if health.Status == provider.HealthStatusUnhealthy {
			logger.WarnfCtx(ctx,
				"Skipping unhealthy provider %s for merchant %s and payment method %s",
				merchantConfig.ProviderName,
				merchantConfig.MerchantID,
				merchantConfig.PaymentMethod,
			)
			continue
		}

		selectedProviders = append(selectedProviders, providerCandidate{
			provider:        selectedProvider,
			failoverEnabled: merchantConfig.FailoverEnabled,
		})
	}

	if len(selectedProviders) == 0 {
		return nil, providerPkg.ErrProviderNotAvailable
	}

	return selectedProviders, nil
}
