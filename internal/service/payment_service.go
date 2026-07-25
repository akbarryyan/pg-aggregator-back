package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
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
	providerRouter             *providerPkg.ProviderRouter
	sandboxProvider            providerPkg.PaymentProvider
	appBaseURL                 string
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
	return &PaymentService{
		paymentRepo:                paymentRepo,
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		webhookEventRepo:           webhookEventRepo,
		providerRouter:             providerRouter,
		appBaseURL:                 strings.TrimRight(appBaseURL, "/"),
	}
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

func (s *PaymentService) CreatePayment(ctx context.Context, req *payment.CreatePaymentRequest) (*payment.Payment, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	env := payment.NormalizeEnvironment(req.Environment)
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
		ExpiresAt:     expiresAt,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	var (
		providerResp *provider.ProviderPaymentResponse
		lastErr      error
		err          error
	)

	// Sandbox never calls real providers (Cashi, etc.)
	if env == payment.EnvironmentSandbox {
		if s.sandboxProvider == nil {
			return nil, fmt.Errorf("sandbox provider is not configured")
		}
		logger.Infof("Creating SANDBOX payment (mock provider, no Cashi call): %s", reference)
		providerReq := &provider.ProviderPaymentRequest{
			InternalReference:     reference,
			Amount:                req.Amount,
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
			// Never route production traffic to sandbox mock
			if selectedProvider.GetName() == "sandbox" {
				continue
			}
			providerReq := &provider.ProviderPaymentRequest{
				InternalReference:     reference,
				Amount:                req.Amount,
				Currency:              req.Currency,
				Description:           req.Description,
				CustomerName:          req.CustomerName,
				CustomerEmail:         req.CustomerEmail,
				ExpiresAt:             expiresAt,
				CallbackURL:           s.buildCallbackURL(selectedProvider.GetName()),
				UseCustomMerchantName: req.UseCustomMerchantName,
			}

			logger.Infof("Creating PRODUCTION payment with provider: %s", selectedProvider.GetName())

			providerResp, err = selectedProvider.CreatePayment(ctx, providerReq)
			if err == nil {
				break
			}

			lastErr = err
			logger.Errorf("Provider %s error: %v", selectedProvider.GetName(), err)
			if !candidate.failoverEnabled {
				break
			}
		}

		if lastErr != nil && providerResp == nil {
			return nil, fmt.Errorf("failed to create payment with available providers: %w", lastErr)
		}
	}

	p.ProviderName = providerResp.ProviderName
	p.ProviderReference = &providerResp.ProviderReference
	p.QRISData = providerResp.QRISData
	p.Status = providerResp.Status
	if providerResp.Amount > 0 {
		p.Amount = providerResp.Amount
	}
	if !providerResp.ExpiresAt.IsZero() {
		p.ExpiresAt = providerResp.ExpiresAt
	}

	if err := s.paymentRepo.Create(ctx, p); err != nil {
		logger.Errorf("Failed to save payment: %v", err)
		return nil, fmt.Errorf("failed to save payment: %w", err)
	}

	logger.Infof("Payment created successfully: %s (provider: %s)", p.Reference, p.ProviderName)
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

	result := &ReconcileResult{
		PaymentID:      p.ID,
		Reference:      p.Reference,
		PreviousStatus: p.Status,
		CurrentStatus:  p.Status,
		Action:         "unchanged",
		Message:        "Status unchanged",
	}

	if payment.IsTerminalStatus(p.Status) {
		result.Action = "skipped"
		result.Message = "Payment already in terminal status"
		return result, nil
	}

	// Local expire without needing provider when past expires_at.
	if p.Status == payment.StatusPending && time.Now().After(p.ExpiresAt) {
		if p.ProviderReference == nil || *p.ProviderReference == "" {
			if err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {
				return nil, err
			}
			p.Status = payment.StatusExpired
			result.CurrentStatus = payment.StatusExpired
			result.Action = "expired_local"
			result.Message = "Marked expired locally (no provider reference; expires_at passed)"
			s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusExpired))
			result.MerchantNotified = true
			return result, nil
		}
	}

	if p.ProviderReference == nil || *p.ProviderReference == "" {
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

	logger.Infof("Reconciling payment %s with provider %s ref %s", p.Reference, p.ProviderName, *p.ProviderReference)

	providerStatus, err := selectedProvider.GetPaymentStatus(ctx, *p.ProviderReference)
	if err != nil {
		logger.Warnf("Failed to check provider status for %s: %v", p.Reference, err)
		// Fall back: if past expiry, expire locally even if provider check failed.
		if p.Status == payment.StatusPending && time.Now().After(p.ExpiresAt) {
			if updErr := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); updErr != nil {
				result.Action = "error"
				result.Message = fmt.Sprintf("provider check failed: %v; local expire failed: %v", err, updErr)
				return result, nil
			}
			p.Status = payment.StatusExpired
			result.CurrentStatus = payment.StatusExpired
			result.Action = "expired_local"
			result.Message = fmt.Sprintf("Provider check failed (%v); marked expired locally (expires_at passed)", err)
			s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusExpired))
			result.MerchantNotified = true
			return result, nil
		}
		result.Action = "error"
		result.Message = fmt.Sprintf("Provider check failed: %v", err)
		return result, nil
	}

	ps := providerStatus.Status
	result.ProviderStatus = &ps

	if providerStatus.Status == p.Status {
		// Still pending at provider but past local expiry → expire locally.
		if p.Status == payment.StatusPending && time.Now().After(p.ExpiresAt) {
			if err := s.paymentRepo.UpdateStatus(ctx, p.ID, payment.StatusExpired, nil); err != nil {
				return nil, err
			}
			p.Status = payment.StatusExpired
			result.CurrentStatus = payment.StatusExpired
			result.Action = "expired_local"
			result.Message = "Provider still pending; marked expired locally (expires_at passed)"
			s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(payment.StatusExpired))
			result.MerchantNotified = true
			return result, nil
		}
		result.Message = "Local status matches provider"
		return result, nil
	}

	if !payment.CanTransitionTo(p.Status, providerStatus.Status) {
		result.Action = "error"
		result.Message = fmt.Sprintf(
			"Invalid transition from %s to provider status %s",
			p.Status,
			providerStatus.Status,
		)
		return result, nil
	}

	if err := s.paymentRepo.UpdateStatus(ctx, p.ID, providerStatus.Status, providerStatus.PaidAt); err != nil {
		return nil, err
	}
	p.Status = providerStatus.Status
	p.PaidAt = providerStatus.PaidAt
	result.CurrentStatus = providerStatus.Status
	result.Action = "updated"
	result.Message = fmt.Sprintf("Status updated from %s to %s via provider", result.PreviousStatus, providerStatus.Status)
	s.NotifyMerchantPaymentEvent(ctx, p, eventTypeForStatus(providerStatus.Status))
	result.MerchantNotified = true
	return result, nil
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
		r, err := s.ReconcilePayment(ctx, row.Payment.ID)
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
			candidates = append(candidates, providerCandidate{
				provider:        defaultProvider,
				failoverEnabled: true,
			})
		}
		return candidates, nil
	}

	selectedProviders := make([]providerCandidate, 0, len(merchantConfigs))
	for _, merchantConfig := range merchantConfigs {
		selectedProvider, exists := s.providerRouter.GetProvider(merchantConfig.ProviderName)
		if !exists {
			logger.Warnf(
				"Skipping unregistered provider %s for merchant %s and payment method %s",
				merchantConfig.ProviderName,
				merchantConfig.MerchantID,
				merchantConfig.PaymentMethod,
			)
			continue
		}

		health := s.providerRouter.GetProviderHealth(merchantConfig.ProviderName)
		if health.Status == provider.HealthStatusUnhealthy {
			logger.Warnf(
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
