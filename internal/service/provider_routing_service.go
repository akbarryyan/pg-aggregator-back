package service

import (
	"context"

	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

type ProviderRoutingService struct {
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository
	providerRouter             *providerPkg.ProviderRouter
}

func NewProviderRoutingService(
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository,
	providerRouter *providerPkg.ProviderRouter,
) *ProviderRoutingService {
	return &ProviderRoutingService{
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		providerRouter:             providerRouter,
	}
}

func (s *ProviderRoutingService) ListMerchantProviderConfigs(ctx context.Context, merchantID uuid.UUID) ([]*domainProvider.MerchantProviderConfig, error) {
	return s.merchantProviderConfigRepo.ListByMerchant(ctx, merchantID)
}

func (s *ProviderRoutingService) UpsertMerchantProviderConfig(ctx context.Context, merchantID uuid.UUID, req *domainProvider.UpsertMerchantProviderConfigRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}

	return s.merchantProviderConfigRepo.Upsert(
		ctx,
		merchantID,
		req.ProviderName,
		req.PaymentMethod,
		req.Priority,
		req.Weight,
		req.FailoverEnabled,
		req.IsEnabled,
	)
}

func (s *ProviderRoutingService) DeleteMerchantProviderConfig(ctx context.Context, merchantID uuid.UUID, paymentMethod, providerName string) error {
	return s.merchantProviderConfigRepo.Delete(ctx, merchantID, paymentMethod, providerName)
}

func (s *ProviderRoutingService) ListProviderHealths(ctx context.Context) ([]domainProvider.ProviderHealth, error) {
	return s.providerRouter.ListProviderHealths(), nil
}

func (s *ProviderRoutingService) UpdateProviderHealth(ctx context.Context, providerName string, req *domainProvider.UpdateProviderHealthRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}

	s.providerRouter.SetProviderHealth(providerName, req.Status, req.Reason)
	return nil
}
