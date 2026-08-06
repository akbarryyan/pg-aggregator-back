package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// AdminProviderService owns the admin-facing view over payment providers:
// provider info/health, merchant routing config listing, and (skeleton)
// reconciliation candidates. Split out of the former AdminService
// god-object — see project backlog item #9. Reconciliation is bundled here
// rather than its own service/file: it's a single small read-only method
// today (paymentRepo.ListAdmin + status labeling), not enough surface to
// justify a separate type — split it out if it grows.
type AdminProviderService struct {
	paymentRepo                *repository.PaymentRepository
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository
	providerRouter             *providerPkg.ProviderRouter
}

func NewAdminProviderService(
	paymentRepo *repository.PaymentRepository,
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository,
	providerRouter *providerPkg.ProviderRouter,
) *AdminProviderService {
	return &AdminProviderService{
		paymentRepo:                paymentRepo,
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		providerRouter:             providerRouter,
	}
}

type ProviderDetail struct {
	Provider       providerPkg.ProviderInfo `json:"provider"`
	MerchantCount  int64                    `json:"merchant_count"`
	MerchantRoutes []RoutingItem            `json:"merchant_routes"`
}

type RoutingItem struct {
	ID              uuid.UUID `json:"id"`
	MerchantID      uuid.UUID `json:"merchant_id"`
	MerchantName    string    `json:"merchant_name"`
	MerchantEmail   string    `json:"merchant_email"`
	ProviderName    string    `json:"provider_name"`
	PaymentMethod   string    `json:"payment_method"`
	Priority        int       `json:"priority"`
	Weight          int       `json:"weight"`
	FailoverEnabled bool      `json:"failover_enabled"`
	IsEnabled       bool      `json:"is_enabled"`
	CreatedAt       string    `json:"created_at"`
	UpdatedAt       string    `json:"updated_at"`
}

type PaginatedRouting struct {
	Items  []RoutingItem `json:"items"`
	Total  int64         `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

type ReconciliationItem struct {
	PaymentID         uuid.UUID `json:"payment_id"`
	Reference         string    `json:"reference"`
	MerchantName      string    `json:"merchant_name"`
	ProviderName      string    `json:"provider_name"`
	ProviderReference *string   `json:"provider_reference,omitempty"`
	Status            string    `json:"status"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	CreatedAt         string    `json:"created_at"`
	ExpiresAt         string    `json:"expires_at"`
	CheckStatus       string    `json:"check_status"` // pending_review | ready (skeleton)
	Note              string    `json:"note"`
}

type ReconciliationResponse struct {
	Items   []ReconciliationItem `json:"items"`
	Total   int                  `json:"total"`
	Message string               `json:"message"`
}

func (s *AdminProviderService) ListProviders() []providerPkg.ProviderInfo {
	if s.providerRouter == nil {
		return []providerPkg.ProviderInfo{}
	}
	return s.providerRouter.ListProviders()
}

// findProvider looks a provider up in-memory (via ProviderRouter, no DB
// round-trip) — use this to validate a provider is registered without
// paying for GetProviderDetail's two DB queries when only existence
// matters (see UpdateProviderHealth).
func (s *AdminProviderService) findProvider(name string) (*providerPkg.ProviderInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("provider name is required")
	}
	for _, p := range s.ListProviders() {
		if strings.EqualFold(p.Name, name) {
			cp := p
			return &cp, nil
		}
	}
	return nil, errors.New("provider not found")
}

func (s *AdminProviderService) GetProviderDetail(ctx context.Context, name string) (*ProviderDetail, error) {
	found, err := s.findProvider(name)
	if err != nil {
		return nil, err
	}

	count := int64(0)
	routes := []RoutingItem{}
	if s.merchantProviderConfigRepo != nil {
		var err error
		count, err = s.merchantProviderConfigRepo.CountByProvider(ctx, found.Name)
		if err != nil {
			return nil, err
		}
		rows, err := s.merchantProviderConfigRepo.ListByProvider(ctx, found.Name, 50, 0)
		if err != nil {
			return nil, err
		}
		routes = make([]RoutingItem, 0, len(rows))
		for _, row := range rows {
			routes = append(routes, toRoutingItem(row))
		}
	}

	return &ProviderDetail{
		Provider:       *found,
		MerchantCount:  count,
		MerchantRoutes: routes,
	}, nil
}

// UpdateProviderHealth sets in-memory provider health used by routing (skips unhealthy).
func (s *AdminProviderService) UpdateProviderHealth(
	ctx context.Context,
	name string,
	req *provider.UpdateProviderHealthRequest,
) (*ProviderDetail, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("provider name is required")
	}
	if req == nil {
		return nil, provider.ErrInvalidProviderHealthStatus
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// Ensure provider is registered before updating health — in-memory
	// check (no DB round-trip); the DB-backed GetProviderDetail below
	// already re-validates this when building the response, so checking
	// it twice via GetProviderDetail would cost 4 queries for what is
	// otherwise an in-memory health toggle.
	if _, err := s.findProvider(name); err != nil {
		return nil, err
	}

	if s.providerRouter == nil {
		return nil, errors.New("provider router not available")
	}

	s.providerRouter.SetProviderHealth(name, req.Status, strings.TrimSpace(req.Reason))
	return s.GetProviderDetail(ctx, name)
}

func (s *AdminProviderService) ListRouting(ctx context.Context, limit, offset int) (*PaginatedRouting, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if s.merchantProviderConfigRepo == nil {
		return &PaginatedRouting{Items: []RoutingItem{}, Limit: limit, Offset: offset}, nil
	}

	rows, err := s.merchantProviderConfigRepo.ListAllRouting(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.merchantProviderConfigRepo.CountAllRouting(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]RoutingItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRoutingItem(row))
	}
	return &PaginatedRouting{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// ListReconciliationCandidates returns pending payments that may need status checks.
// Skeleton: does not call external providers yet.
func (s *AdminProviderService) ListReconciliationCandidates(ctx context.Context, limit int) (*ReconciliationResponse, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.paymentRepo.ListAdmin(ctx, payment.StatusPending, "", nil, nil, nil, "", limit, 0)
	if err != nil {
		return nil, err
	}

	items := make([]ReconciliationItem, 0, len(rows))
	now := time.Now()
	for _, row := range rows {
		p := row.Payment
		checkStatus := "pending_review"
		note := "Awaiting provider status check (job not run yet)"
		if p.ExpiresAt.Before(now) {
			checkStatus = "possibly_expired"
			note = "Local expires_at already passed — candidate for expire/reconcile job"
		} else if p.ProviderReference == nil || *p.ProviderReference == "" {
			checkStatus = "missing_provider_ref"
			note = "No provider reference stored — cannot poll provider yet"
		}

		items = append(items, ReconciliationItem{
			PaymentID:         p.ID,
			Reference:         p.Reference,
			MerchantName:      row.MerchantName,
			ProviderName:      p.ProviderName,
			ProviderReference: p.ProviderReference,
			Status:            p.Status,
			Amount:            p.Amount,
			Currency:          p.Currency,
			CreatedAt:         p.CreatedAt.UTC().Format(time.RFC3339),
			ExpiresAt:         p.ExpiresAt.UTC().Format(time.RFC3339),
			CheckStatus:       checkStatus,
			Note:              note,
		})
	}

	return &ReconciliationResponse{
		Items:   items,
		Total:   len(items),
		Message: "Pending payments that may need a provider status check. Use Check or Check all to poll the provider and sync status.",
	}, nil
}

func toRoutingItem(row repository.RoutingRow) RoutingItem {
	c := row.Config
	return RoutingItem{
		ID:              c.ID,
		MerchantID:      c.MerchantID,
		MerchantName:    row.MerchantName,
		MerchantEmail:   row.MerchantEmail,
		ProviderName:    c.ProviderName,
		PaymentMethod:   c.PaymentMethod,
		Priority:        c.Priority,
		Weight:          c.Weight,
		FailoverEnabled: c.FailoverEnabled,
		IsEnabled:       c.IsEnabled,
		CreatedAt:       c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
