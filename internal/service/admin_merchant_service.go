package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// AdminMerchantService owns merchant resource CRUD and merchant-scoped
// provider-config CRUD for the admin panel. Split out of the former
// AdminService god-object — see project backlog item #9. It composes
// AdminPaymentService (rather than duplicating pagination/formatting logic)
// for ListMerchantPayments, since "list this merchant's payments" is really
// a payment-listing concern with a merchant_id filter.
type AdminMerchantService struct {
	merchantRepo               *repository.MerchantRepository
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository
	paymentService             *AdminPaymentService
}

func NewAdminMerchantService(
	merchantRepo *repository.MerchantRepository,
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository,
	paymentService *AdminPaymentService,
) *AdminMerchantService {
	return &AdminMerchantService{
		merchantRepo:               merchantRepo,
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		paymentService:             paymentService,
	}
}

type PaginatedMerchants struct {
	Items  []*merchant.MerchantResponse `json:"items"`
	Total  int64                        `json:"total"`
	Limit  int                          `json:"limit"`
	Offset int                          `json:"offset"`
}

func parseMerchantActiveFilter(status string) *bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "active":
		v := true
		return &v
	case "inactive":
		v := false
		return &v
	default:
		return nil
	}
}

func (s *AdminMerchantService) ListMerchants(
	ctx context.Context,
	search, status string,
	limit, offset int,
) (*PaginatedMerchants, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	search = strings.TrimSpace(search)
	isActive := parseMerchantActiveFilter(status)

	rows, err := s.merchantRepo.ListAdmin(ctx, search, isActive, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.merchantRepo.CountAdmin(ctx, search, isActive)
	if err != nil {
		return nil, err
	}

	items := make([]*merchant.MerchantResponse, 0, len(rows))
	for _, m := range rows {
		items = append(items, merchant.ToMerchantResponse(m))
	}

	return &PaginatedMerchants{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

func (s *AdminMerchantService) CreateMerchant(ctx context.Context, req *merchant.CreateMerchantRequest) (*merchant.MerchantResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Phone = strings.TrimSpace(req.Phone)
	req.BusinessName = strings.TrimSpace(req.BusinessName)
	if req.WebhookURL != nil {
		trimmed := strings.TrimSpace(*req.WebhookURL)
		if trimmed == "" {
			req.WebhookURL = nil
		} else {
			req.WebhookURL = &trimmed
		}
	}

	if existing, err := s.merchantRepo.GetByEmail(ctx, req.Email); err == nil && existing != nil {
		return nil, merchant.ErrMerchantAlreadyExists
	} else if err != nil && err != merchant.ErrMerchantNotFound {
		return nil, err
	}

	created, err := s.merchantRepo.Create(ctx, req)
	if err != nil {
		return nil, err
	}
	return merchant.ToMerchantResponse(created), nil
}

func (s *AdminMerchantService) ExportMerchants(ctx context.Context, search, status string) ([]*merchant.Merchant, error) {
	search = strings.TrimSpace(search)
	isActive := parseMerchantActiveFilter(status)
	// Cap export size for safety
	return s.merchantRepo.ListAdmin(ctx, search, isActive, 5000, 0)
}

func (s *AdminMerchantService) GetMerchant(ctx context.Context, id uuid.UUID) (*merchant.MerchantResponse, error) {
	m, err := s.merchantRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return merchant.ToMerchantResponse(m), nil
}

func (s *AdminMerchantService) UpdateMerchant(ctx context.Context, id uuid.UUID, req *merchant.UpdateMerchantRequest) (*merchant.MerchantResponse, error) {
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
		if trimmed == "" {
			return nil, merchant.ErrMerchantNameRequired
		}
	}
	if req.BusinessName != nil {
		trimmed := strings.TrimSpace(*req.BusinessName)
		req.BusinessName = &trimmed
		if trimmed == "" {
			return nil, merchant.ErrBusinessNameRequired
		}
	}
	if req.Phone != nil {
		trimmed := strings.TrimSpace(*req.Phone)
		req.Phone = &trimmed
	}
	if req.WebhookURL != nil {
		trimmed := strings.TrimSpace(*req.WebhookURL)
		if trimmed == "" {
			req.WebhookURL = nil
		} else {
			req.WebhookURL = &trimmed
		}
	}

	updated, err := s.merchantRepo.Update(ctx, id, req)
	if err != nil {
		return nil, err
	}
	return merchant.ToMerchantResponse(updated), nil
}

func (s *AdminMerchantService) SetMerchantActive(ctx context.Context, id uuid.UUID, isActive bool) (*merchant.MerchantResponse, error) {
	// Fetch once: doubles as the existence check (SetActive itself doesn't
	// error on a missing row — it's an UPDATE with no RowsAffected check)
	// and supplies every other field for the response, so no second
	// GetByID is needed after the write.
	m, err := s.merchantRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.merchantRepo.SetActive(ctx, id, isActive); err != nil {
		return nil, err
	}
	m.IsActive = isActive
	m.UpdatedAt = time.Now()
	return merchant.ToMerchantResponse(m), nil
}

func (s *AdminMerchantService) ListMerchantPayments(ctx context.Context, merchantID uuid.UUID, limit, offset int) (*PaginatedPayments, error) {
	return s.paymentService.ListPayments(ctx, "", "", &merchantID, nil, nil, "", limit, offset)
}

func (s *AdminMerchantService) ListMerchantProviderConfigs(ctx context.Context, merchantID uuid.UUID) ([]*provider.MerchantProviderConfig, error) {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		return nil, err
	}
	if s.merchantProviderConfigRepo == nil {
		return []*provider.MerchantProviderConfig{}, nil
	}
	return s.merchantProviderConfigRepo.ListByMerchant(ctx, merchantID)
}

// UpsertMerchantProviderConfig saves the config and returns the merchant's
// full provider-config list in the same call, so callers that need the
// refreshed list (the admin UI does, to show the result immediately) don't
// have to make a second ListMerchantProviderConfigs call afterward — which
// would re-run the merchant existence check this call already did.
func (s *AdminMerchantService) UpsertMerchantProviderConfig(
	ctx context.Context,
	merchantID uuid.UUID,
	req *provider.UpsertMerchantProviderConfigRequest,
) ([]*provider.MerchantProviderConfig, error) {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if s.merchantProviderConfigRepo == nil {
		return nil, errors.New("provider config repository not available")
	}
	if err := s.merchantProviderConfigRepo.Upsert(
		ctx,
		merchantID,
		req.ProviderName,
		req.PaymentMethod,
		req.Priority,
		req.Weight,
		req.FailoverEnabled,
		req.IsEnabled,
	); err != nil {
		return nil, err
	}
	return s.merchantProviderConfigRepo.ListByMerchant(ctx, merchantID)
}

func (s *AdminMerchantService) DeleteMerchantProviderConfig(
	ctx context.Context,
	merchantID uuid.UUID,
	paymentMethod, providerName string,
) error {
	if s.merchantProviderConfigRepo == nil {
		return errors.New("provider config repository not available")
	}
	return s.merchantProviderConfigRepo.Delete(ctx, merchantID, paymentMethod, providerName)
}
