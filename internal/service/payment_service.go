package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"pg-aggregator/internal/domain/payment"
	"pg-aggregator/internal/domain/provider"
	providerPkg "pg-aggregator/internal/provider"
	"pg-aggregator/internal/repository"
	"pg-aggregator/pkg/logger"
)

type PaymentService struct {
	paymentRepo     *repository.PaymentRepository
	providerAdapter providerPkg.PaymentProvider
}

func NewPaymentService(paymentRepo *repository.PaymentRepository, providerAdapter providerPkg.PaymentProvider) *PaymentService {
	return &PaymentService{
		paymentRepo:     paymentRepo,
		providerAdapter: providerAdapter,
	}
}

func (s *PaymentService) CreatePayment(ctx context.Context, req *payment.CreatePaymentRequest) (*payment.Payment, error) {
	if err := req.Validate(); err != nil {
		return nil, err
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

	providerReq := &provider.ProviderPaymentRequest{
		InternalReference: reference,
		Amount:            req.Amount,
		Currency:          req.Currency,
		Description:       req.Description,
		CustomerName:      req.CustomerName,
		CustomerEmail:     req.CustomerEmail,
		ExpiresAt:         expiresAt,
		CallbackURL:       s.buildCallbackURL(reference),
	}

	logger.Infof("Creating payment with provider: %s", s.providerAdapter.GetName())

	providerResp, err := s.providerAdapter.CreatePayment(ctx, providerReq)
	if err != nil {
		logger.Errorf("Provider error: %v", err)
		return nil, fmt.Errorf("failed to create payment with provider: %w", err)
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

	logger.Infof("Checking payment status with provider: %s", *p.ProviderReference)

	providerStatus, err := s.providerAdapter.GetPaymentStatus(ctx, *p.ProviderReference)
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

func (s *PaymentService) buildCallbackURL(reference string) string {
	return fmt.Sprintf("http://localhost:8080/api/v1/provider-webhooks/%s", s.providerAdapter.GetName())
}
