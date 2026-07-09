package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

type PaymentService struct {
	paymentRepo                *repository.PaymentRepository
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository
	providerRouter             *providerPkg.ProviderRouter
	appBaseURL                 string
}

type providerCandidate struct {
	provider        providerPkg.PaymentProvider
	failoverEnabled bool
}

func NewPaymentService(
	paymentRepo *repository.PaymentRepository,
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository,
	providerRouter *providerPkg.ProviderRouter,
	appBaseURL string,
) *PaymentService {
	return &PaymentService{
		paymentRepo:                paymentRepo,
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		providerRouter:             providerRouter,
		appBaseURL:                 strings.TrimRight(appBaseURL, "/"),
	}
}

func (s *PaymentService) CreatePayment(ctx context.Context, req *payment.CreatePaymentRequest) (*payment.Payment, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	candidateProviders, err := s.resolveProvidersForMerchant(ctx, req.MerchantID, req.PaymentMethod)
	if err != nil {
		return nil, fmt.Errorf("failed to select provider: %w", err)
	}

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
		ExpiresAt:     expiresAt,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	var (
		providerResp *provider.ProviderPaymentResponse
		lastErr      error
	)

	for _, candidate := range candidateProviders {
		selectedProvider := candidate.provider
		providerReq := &provider.ProviderPaymentRequest{
			InternalReference: reference,
			Amount:            req.Amount,
			Currency:          req.Currency,
			Description:       req.Description,
			CustomerName:      req.CustomerName,
			CustomerEmail:     req.CustomerEmail,
			ExpiresAt:         expiresAt,
			CallbackURL:       s.buildCallbackURL(selectedProvider.GetName()),
		}

		logger.Infof("Creating payment with provider: %s", selectedProvider.GetName())

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

	p.ProviderName = providerResp.ProviderName
	p.ProviderReference = &providerResp.ProviderReference
	p.QRISData = providerResp.QRISData
	p.Status = providerResp.Status

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

	if payment.IsTerminalStatus(p.Status) {
		return p, nil
	}

	if p.ProviderReference == nil {
		return p, nil
	}

	selectedProvider, err := s.getProviderByName(p.ProviderName)
	if err != nil {
		return nil, err
	}

	logger.Infof("Checking payment status with provider: %s", *p.ProviderReference)

	providerStatus, err := selectedProvider.GetPaymentStatus(ctx, *p.ProviderReference)
	if err != nil {
		logger.Warnf("Failed to check provider status: %v", err)
		return p, nil
	}

	if providerStatus.Status != p.Status {
		logger.Infof("Payment status changed from %s to %s", p.Status, providerStatus.Status)
		if err := s.paymentRepo.UpdateStatus(ctx, p.ID, providerStatus.Status, providerStatus.PaidAt); err != nil {
			logger.Errorf("Failed to update payment status: %v", err)
			return p, nil
		}
		p.Status = providerStatus.Status
		p.PaidAt = providerStatus.PaidAt
	}

	return p, nil
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
