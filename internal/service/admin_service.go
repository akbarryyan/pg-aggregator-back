package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

var ErrWebhookEventNotFound = errors.New("webhook event not found")

type AdminService struct {
	merchantRepo               *repository.MerchantRepository
	paymentRepo                *repository.PaymentRepository
	webhookEventRepo           *repository.WebhookEventRepository
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository
	callbackRepo               *repository.MerchantCallbackRepository
	providerRouter             *providerPkg.ProviderRouter
}

func NewAdminService(
	merchantRepo *repository.MerchantRepository,
	paymentRepo *repository.PaymentRepository,
	webhookEventRepo *repository.WebhookEventRepository,
	merchantProviderConfigRepo *repository.MerchantProviderConfigRepository,
	providerRouter *providerPkg.ProviderRouter,
) *AdminService {
	return &AdminService{
		merchantRepo:               merchantRepo,
		paymentRepo:                paymentRepo,
		webhookEventRepo:           webhookEventRepo,
		merchantProviderConfigRepo: merchantProviderConfigRepo,
		providerRouter:             providerRouter,
	}
}

func (s *AdminService) WithCallbackRepo(repo *repository.MerchantCallbackRepository) *AdminService {
	s.callbackRepo = repo
	return s
}

type DashboardSummary struct {
	TotalPayments   int64 `json:"total_payments"`
	PendingPayments int64 `json:"pending_payments"`
	PaidPayments    int64 `json:"paid_payments"`
	ExpiredPayments int64 `json:"expired_payments"`
	FailedPayments  int64 `json:"failed_payments"`
	TotalMerchants  int64 `json:"total_merchants"`
	PaidAmount      int64 `json:"paid_amount"`
	WebhookEvents   int64 `json:"webhook_events"`
}

type DashboardDailyPoint struct {
	Date       string `json:"date"`
	Label      string `json:"label"`
	Total      int64  `json:"total"`
	Paid       int64  `json:"paid"`
	Pending    int64  `json:"pending"`
	Failed     int64  `json:"failed"`
	Expired    int64  `json:"expired"`
	Cancelled  int64  `json:"cancelled"`
	PaidAmount int64  `json:"paid_amount"`
}

type DashboardStatusPoint struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type DashboardCharts struct {
	Days             int                    `json:"days"`
	Daily            []DashboardDailyPoint  `json:"daily"`
	StatusBreakdown  []DashboardStatusPoint `json:"status_breakdown"`
}

type AdminPaymentItem struct {
	ID                uuid.UUID  `json:"id"`
	Reference         string     `json:"reference"`
	MerchantID        uuid.UUID  `json:"merchant_id"`
	MerchantName      string     `json:"merchant_name"`
	Amount            int64      `json:"amount"`
	Currency          string     `json:"currency"`
	PaymentMethod     string     `json:"payment_method"`
	ProviderName      string     `json:"provider_name"`
	ProviderReference *string    `json:"provider_reference,omitempty"`
	Status            string     `json:"status"`
	Description       string     `json:"description"`
	CustomerName      *string    `json:"customer_name,omitempty"`
	CustomerEmail     *string    `json:"customer_email,omitempty"`
	Environment       string     `json:"environment"`
	ExpiresAt         string     `json:"expires_at"`
	PaidAt            *string    `json:"paid_at,omitempty"`
	CreatedAt         string     `json:"created_at"`
	UpdatedAt         string     `json:"updated_at"`
}

type AdminPaymentDetail struct {
	AdminPaymentItem
	QRISData    *string `json:"qris_data,omitempty"`
	CallbackURL *string `json:"callback_url,omitempty"`
}

type PaginatedPayments struct {
	Items  []AdminPaymentItem `json:"items"`
	Total  int64              `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

type PaginatedMerchants struct {
	Items  []*merchant.MerchantResponse `json:"items"`
	Total  int64                        `json:"total"`
	Limit  int                          `json:"limit"`
	Offset int                          `json:"offset"`
}

type AdminLogItem struct {
	ID                uuid.UUID  `json:"id"`
	PaymentID         *uuid.UUID `json:"payment_id,omitempty"`
	ProviderName      string     `json:"provider_name"`
	ProviderReference string     `json:"provider_reference"`
	EventType         string     `json:"event_type"`
	Status            string     `json:"status"`
	IsProcessed       bool       `json:"is_processed"`
	ProcessingError   *string    `json:"processing_error,omitempty"`
	ProcessedAt       *string    `json:"processed_at,omitempty"`
	CreatedAt         string     `json:"created_at"`
}

type PaginatedLogs struct {
	Items  []AdminLogItem `json:"items"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func (s *AdminService) GetDashboardSummary(ctx context.Context) (*DashboardSummary, error) {
	total, err := s.paymentRepo.CountAll(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := s.paymentRepo.CountAllByStatus(ctx, payment.StatusPending)
	if err != nil {
		return nil, err
	}
	paid, err := s.paymentRepo.CountAllByStatus(ctx, payment.StatusPaid)
	if err != nil {
		return nil, err
	}
	expired, err := s.paymentRepo.CountAllByStatus(ctx, payment.StatusExpired)
	if err != nil {
		return nil, err
	}
	failed, err := s.paymentRepo.CountAllByStatus(ctx, payment.StatusFailed)
	if err != nil {
		return nil, err
	}
	merchants, err := s.merchantRepo.Count(ctx)
	if err != nil {
		return nil, err
	}
	paidAmount, err := s.paymentRepo.SumPaidAmount(ctx)
	if err != nil {
		return nil, err
	}
	webhooks, err := s.webhookEventRepo.Count(ctx)
	if err != nil {
		return nil, err
	}

	return &DashboardSummary{
		TotalPayments:   total,
		PendingPayments: pending,
		PaidPayments:    paid,
		ExpiredPayments: expired,
		FailedPayments:  failed,
		TotalMerchants:  merchants,
		PaidAmount:      paidAmount,
		WebhookEvents:   webhooks,
	}, nil
}

// MerchantDailyStats returns chart payload scoped to one merchant + environment.
func (s *AdminService) MerchantDailyStats(
	ctx context.Context,
	merchantID uuid.UUID,
	environment string,
	days int,
) (*DashboardCharts, error) {
	if days <= 0 {
		days = 14
	}
	if days > 90 {
		days = 90
	}
	env := payment.NormalizeEnvironment(environment)
	raw, err := s.paymentRepo.DailyStatsFiltered(ctx, days, &merchantID, env)
	if err != nil {
		return nil, err
	}
	// Status breakdown from latest list totals for this merchant+env
	paid, _ := s.ListPayments(ctx, payment.StatusPaid, "", &merchantID, nil, nil, env, 1, 0)
	pend, _ := s.ListPayments(ctx, payment.StatusPending, "", &merchantID, nil, nil, env, 1, 0)
	failed, _ := s.ListPayments(ctx, payment.StatusFailed, "", &merchantID, nil, nil, env, 1, 0)
	expired, _ := s.ListPayments(ctx, payment.StatusExpired, "", &merchantID, nil, nil, env, 1, 0)
	breakdown := []DashboardStatusPoint{
		{Status: "paid", Count: paid.Total},
		{Status: "pending", Count: pend.Total},
		{Status: "failed", Count: failed.Total},
		{Status: "expired", Count: expired.Total},
	}
	return fillDailyCharts(raw, days, breakdown), nil
}

// ListMerchantNotifications builds operational alerts for a merchant (scoped env).
func (s *AdminService) ListMerchantNotifications(
	ctx context.Context,
	merchantID uuid.UUID,
	environment string,
	limit int,
) (*NotificationsResponse, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 50 {
		limit = 50
	}
	env := payment.NormalizeEnvironment(environment)

	items := make([]AdminNotification, 0, limit)

	// Failed callbacks for this merchant
	if s.callbackRepo != nil {
		cbs, err := s.callbackRepo.ListFiltered(ctx, merchant.CallbackStatusFailed, &merchantID, 15, 0)
		if err != nil {
			return nil, err
		}
		for _, row := range cbs {
			d := row.Delivery
			items = append(items, AdminNotification{
				ID:        "cb-" + d.ID.String(),
				Kind:      "webhook_error",
				Title:     "Callback failed",
				Body:      fmt.Sprintf("%s · attempt #%d · %s", row.Reference, d.AttemptNumber, d.EventType),
				CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
				Href:      "/dashboard/payments/" + d.PaymentID.String(),
				Attention: true,
			})
		}
	}

	// Recent expired / failed payments in this env
	for _, st := range []string{payment.StatusExpired, payment.StatusFailed} {
		rows, err := s.paymentRepo.ListAdmin(ctx, st, "", &merchantID, nil, nil, env, 8, 0)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			p := row.Payment
			kind := "payment_expired"
			title := "Payment expired"
			if st == payment.StatusFailed {
				kind = "payment_failed"
				title = "Payment failed"
			}
			items = append(items, AdminNotification{
				ID:        "pay-" + p.ID.String(),
				Kind:      kind,
				Title:     title,
				Body:      fmt.Sprintf("%s · %d %s", p.Reference, p.Amount, p.Currency),
				CreatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
				Href:      "/dashboard/payments/" + p.ID.String(),
				Attention: st == payment.StatusFailed,
			})
		}
	}

	// Sort by created_at desc (string RFC3339 sorts OK)
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})
	if len(items) > limit {
		items = items[:limit]
	}
	attention := 0
	for _, n := range items {
		if n.Attention {
			attention++
		}
	}
	return &NotificationsResponse{
		Items:          items,
		AttentionCount: attention,
		Total:          len(items),
	}, nil
}

func (s *AdminService) GetDashboardCharts(ctx context.Context, days int) (*DashboardCharts, error) {
	if days <= 0 {
		days = 14
	}
	if days > 90 {
		days = 90
	}

	raw, err := s.paymentRepo.DailyStats(ctx, days)
	if err != nil {
		return nil, err
	}

	summary, err := s.GetDashboardSummary(ctx)
	if err != nil {
		return nil, err
	}

	statusBreakdown := []DashboardStatusPoint{
		{Status: "paid", Count: summary.PaidPayments},
		{Status: "pending", Count: summary.PendingPayments},
		{Status: "failed", Count: summary.FailedPayments},
		{Status: "expired", Count: summary.ExpiredPayments},
	}

	return fillDailyCharts(raw, days, statusBreakdown), nil
}

func fillDailyCharts(
	raw []repository.DailyPaymentStat,
	days int,
	statusBreakdown []DashboardStatusPoint,
) *DashboardCharts {
	byDate := make(map[string]repository.DailyPaymentStat, len(raw))
	for _, row := range raw {
		key := row.Day.Format("2006-01-02")
		byDate[key] = row
	}

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(days - 1))

	daily := make([]DashboardDailyPoint, 0, days)
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		row, ok := byDate[key]
		point := DashboardDailyPoint{
			Date:  key,
			Label: d.Format("02 Jan"),
		}
		if ok {
			point.Total = row.Total
			point.Paid = row.Paid
			point.Pending = row.Pending
			point.Failed = row.Failed
			point.Expired = row.Expired
			point.Cancelled = row.Cancelled
			point.PaidAmount = row.PaidAmount
		}
		daily = append(daily, point)
	}

	return &DashboardCharts{
		Days:             days,
		Daily:            daily,
		StatusBreakdown:  statusBreakdown,
	}
}

func (s *AdminService) ListPayments(
	ctx context.Context,
	status, search string,
	merchantID *uuid.UUID,
	dateFrom, dateTo *time.Time,
	environment string,
	limit, offset int,
) (*PaginatedPayments, error) {
	status = strings.TrimSpace(strings.ToLower(status))
	search = strings.TrimSpace(search)
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.paymentRepo.ListAdmin(ctx, status, search, merchantID, dateFrom, dateTo, environment, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.paymentRepo.CountAdmin(ctx, status, search, merchantID, dateFrom, dateTo, environment)
	if err != nil {
		return nil, err
	}

	items := make([]AdminPaymentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAdminPaymentItem(row.Payment, row.MerchantName))
	}

	return &PaginatedPayments{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

func (s *AdminService) ExportPayments(
	ctx context.Context,
	status, search string,
	merchantID *uuid.UUID,
	dateFrom, dateTo *time.Time,
	environment string,
) ([]AdminPaymentItem, error) {
	status = strings.TrimSpace(strings.ToLower(status))
	search = strings.TrimSpace(search)
	rows, err := s.paymentRepo.ListAdmin(ctx, status, search, merchantID, dateFrom, dateTo, environment, 5000, 0)
	if err != nil {
		return nil, err
	}
	items := make([]AdminPaymentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAdminPaymentItem(row.Payment, row.MerchantName))
	}
	return items, nil
}

func (s *AdminService) GetPayment(ctx context.Context, id uuid.UUID) (*AdminPaymentDetail, error) {
	p, err := s.paymentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	merchantName := ""
	if m, err := s.merchantRepo.GetByID(ctx, p.MerchantID); err == nil && m != nil {
		if m.BusinessName != "" {
			merchantName = m.BusinessName
		} else {
			merchantName = m.Name
		}
	}

	item := toAdminPaymentItem(p, merchantName)
	return &AdminPaymentDetail{
		AdminPaymentItem: item,
		QRISData:         p.QRISData,
		CallbackURL:      p.CallbackURL,
	}, nil
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

func (s *AdminService) ListMerchants(
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

func (s *AdminService) CreateMerchant(ctx context.Context, req *merchant.CreateMerchantRequest) (*merchant.MerchantResponse, error) {
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

func (s *AdminService) ExportMerchants(ctx context.Context, search, status string) ([]*merchant.Merchant, error) {
	search = strings.TrimSpace(search)
	isActive := parseMerchantActiveFilter(status)
	// Cap export size for safety
	return s.merchantRepo.ListAdmin(ctx, search, isActive, 5000, 0)
}

func (s *AdminService) GetMerchant(ctx context.Context, id uuid.UUID) (*merchant.MerchantResponse, error) {
	m, err := s.merchantRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return merchant.ToMerchantResponse(m), nil
}

func (s *AdminService) UpdateMerchant(ctx context.Context, id uuid.UUID, req *merchant.UpdateMerchantRequest) (*merchant.MerchantResponse, error) {
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

func (s *AdminService) SetMerchantActive(ctx context.Context, id uuid.UUID, isActive bool) (*merchant.MerchantResponse, error) {
	if _, err := s.merchantRepo.GetByID(ctx, id); err != nil {
		return nil, err
	}
	if err := s.merchantRepo.SetActive(ctx, id, isActive); err != nil {
		return nil, err
	}
	m, err := s.merchantRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return merchant.ToMerchantResponse(m), nil
}

func (s *AdminService) ListMerchantPayments(ctx context.Context, merchantID uuid.UUID, limit, offset int) (*PaginatedPayments, error) {
	return s.ListPayments(ctx, "", "", &merchantID, nil, nil, "", limit, offset)
}

func (s *AdminService) ListProviders() []providerPkg.ProviderInfo {
	if s.providerRouter == nil {
		return []providerPkg.ProviderInfo{}
	}
	return s.providerRouter.ListProviders()
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

func (s *AdminService) GetProviderDetail(ctx context.Context, name string) (*ProviderDetail, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("provider name is required")
	}

	var found *providerPkg.ProviderInfo
	for _, p := range s.ListProviders() {
		if strings.EqualFold(p.Name, name) {
			cp := p
			found = &cp
			break
		}
	}
	if found == nil {
		return nil, errors.New("provider not found")
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
func (s *AdminService) UpdateProviderHealth(
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

	// Ensure provider is registered before updating health.
	if _, err := s.GetProviderDetail(ctx, name); err != nil {
		return nil, err
	}

	if s.providerRouter == nil {
		return nil, errors.New("provider router not available")
	}

	s.providerRouter.SetProviderHealth(name, req.Status, strings.TrimSpace(req.Reason))
	return s.GetProviderDetail(ctx, name)
}

func (s *AdminService) ListMerchantProviderConfigs(ctx context.Context, merchantID uuid.UUID) ([]*provider.MerchantProviderConfig, error) {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		return nil, err
	}
	if s.merchantProviderConfigRepo == nil {
		return []*provider.MerchantProviderConfig{}, nil
	}
	return s.merchantProviderConfigRepo.ListByMerchant(ctx, merchantID)
}

func (s *AdminService) UpsertMerchantProviderConfig(
	ctx context.Context,
	merchantID uuid.UUID,
	req *provider.UpsertMerchantProviderConfigRequest,
) error {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	if s.merchantProviderConfigRepo == nil {
		return errors.New("provider config repository not available")
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

func (s *AdminService) DeleteMerchantProviderConfig(
	ctx context.Context,
	merchantID uuid.UUID,
	paymentMethod, providerName string,
) error {
	if s.merchantProviderConfigRepo == nil {
		return errors.New("provider config repository not available")
	}
	return s.merchantProviderConfigRepo.Delete(ctx, merchantID, paymentMethod, providerName)
}

func (s *AdminService) ListRouting(ctx context.Context, limit, offset int) (*PaginatedRouting, error) {
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
func (s *AdminService) ListReconciliationCandidates(ctx context.Context, limit int) (*ReconciliationResponse, error) {
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

// AdminNotification is a compact operational alert for the admin bell drawer.
type AdminNotification struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // webhook_error | webhook_pending | payment_failed | payment_expired | info
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	Href      string `json:"href,omitempty"`
	Attention bool   `json:"attention"` // counts toward badge
}

type NotificationsResponse struct {
	Items          []AdminNotification `json:"items"`
	AttentionCount int                 `json:"attention_count"`
	Total          int                 `json:"total"`
}

func (s *AdminService) ListNotifications(ctx context.Context, limit int) (*NotificationsResponse, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 50 {
		limit = 50
	}

	// Fetch a bit more from each source, then merge/sort/cap.
	logs, err := s.webhookEventRepo.ListAdmin(ctx, 40, 0)
	if err != nil {
		return nil, err
	}

	failedRows, err := s.paymentRepo.ListAdmin(ctx, payment.StatusFailed, "", nil, nil, nil, "", 10, 0)
	if err != nil {
		return nil, err
	}
	expiredRows, err := s.paymentRepo.ListAdmin(ctx, payment.StatusExpired, "", nil, nil, nil, "", 10, 0)
	if err != nil {
		return nil, err
	}

	items := make([]AdminNotification, 0, 50)
	seen := make(map[string]struct{}, 50)

	add := func(n AdminNotification) {
		if _, ok := seen[n.ID]; ok {
			return
		}
		seen[n.ID] = struct{}{}
		items = append(items, n)
	}

	for _, e := range logs {
		if n, ok := notificationFromLog(e); ok {
			add(n)
		}
	}
	for _, row := range failedRows {
		add(notificationFromPayment(row.Payment, row.MerchantName, "payment_failed"))
	}
	for _, row := range expiredRows {
		add(notificationFromPayment(row.Payment, row.MerchantName, "payment_expired"))
	}

	// Newest first
	sortNotificationsByCreatedAtDesc(items)

	if len(items) > limit {
		items = items[:limit]
	}

	attention := 0
	for _, n := range items {
		if n.Attention {
			attention++
		}
	}

	return &NotificationsResponse{
		Items:          items,
		AttentionCount: attention,
		Total:          len(items),
	}, nil
}

func notificationFromLog(e *provider.WebhookEvent) (AdminNotification, bool) {
	createdAt := e.CreatedAt.UTC().Format(time.RFC3339)

	if e.ProcessingError != nil && strings.TrimSpace(*e.ProcessingError) != "" {
		return AdminNotification{
			ID:        "log-err-" + e.ID.String(),
			Kind:      "webhook_error",
			Title:     fallbackTitle(e.EventType, "Webhook processing failed"),
			Body:      strings.TrimSpace(*e.ProcessingError),
			CreatedAt: createdAt,
			Href:      "/admin/logs/" + e.ID.String(),
			Attention: true,
		}, true
	}

	if !e.IsProcessed {
		return AdminNotification{
			ID:        "log-pending-" + e.ID.String(),
			Kind:      "webhook_pending",
			Title:     fallbackTitle(e.EventType, "Unprocessed webhook"),
			Body:      "Provider " + e.ProviderName + " · ref " + emptyDash(e.ProviderReference),
			CreatedAt: createdAt,
			Href:      "/admin/logs/" + e.ID.String(),
			Attention: true,
		}, true
	}

	if e.Status == payment.StatusFailed || e.Status == payment.StatusExpired {
		kind := "payment_expired"
		if e.Status == payment.StatusFailed {
			kind = "payment_failed"
		}
		href := "/admin/logs/" + e.ID.String()
		if e.PaymentID != nil {
			href = "/admin/payments/" + e.PaymentID.String()
		}
		return AdminNotification{
			ID:        "log-info-" + e.ID.String(),
			Kind:      kind,
			Title:     fallbackTitle(e.EventType, "Payment "+e.Status),
			Body:      "Provider " + e.ProviderName + " · " + emptyDash(e.ProviderReference),
			CreatedAt: createdAt,
			Href:      href,
			Attention: e.Status == payment.StatusFailed,
		}, true
	}

	return AdminNotification{}, false
}

func notificationFromPayment(p *payment.Payment, merchantName, kind string) AdminNotification {
	title := "Payment expired"
	if kind == "payment_failed" {
		title = "Payment failed"
	}
	merchantLabel := merchantName
	if merchantLabel == "" {
		merchantLabel = "Merchant"
	}
	created := p.CreatedAt
	if p.UpdatedAt.After(p.CreatedAt) {
		created = p.UpdatedAt
	}
	return AdminNotification{
		ID:        "pay-" + p.ID.String(),
		Kind:      kind,
		Title:     title,
		Body:      p.Reference + " · " + merchantLabel + " · " + p.Description,
		CreatedAt: created.UTC().Format(time.RFC3339),
		Href:      "/admin/payments/" + p.ID.String(),
		Attention: kind == "payment_failed",
	}
}

func fallbackTitle(primary, fallback string) string {
	if strings.TrimSpace(primary) == "" {
		return fallback
	}
	return primary
}

func emptyDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

func sortNotificationsByCreatedAtDesc(items []AdminNotification) {
	sort.Slice(items, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, items[i].CreatedAt)
		tj, _ := time.Parse(time.RFC3339, items[j].CreatedAt)
		return tj.After(ti)
	})
}

func (s *AdminService) ListPaymentEvents(ctx context.Context, paymentID uuid.UUID) ([]AdminLogItem, error) {
	if _, err := s.paymentRepo.GetByID(ctx, paymentID); err != nil {
		return nil, err
	}
	events, err := s.webhookEventRepo.ListByPaymentID(ctx, paymentID, 100)
	if err != nil {
		return nil, err
	}
	items := make([]AdminLogItem, 0, len(events))
	for _, e := range events {
		items = append(items, toAdminLogItem(e))
	}
	return items, nil
}

type AdminLogDetail struct {
	AdminLogItem
	// Raw payload is included for admin debugging; strip secrets at handler if needed.
	RawPayload map[string]interface{} `json:"raw_payload,omitempty"`
}

func (s *AdminService) GetLog(ctx context.Context, id uuid.UUID) (*AdminLogDetail, error) {
	e, err := s.webhookEventRepo.GetByID(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrWebhookEventNotFound
		}
		return nil, err
	}

	// Redact common secret-looking keys from raw payload before returning.
	safePayload := redactPayload(e.RawPayload)

	item := toAdminLogItem(e)
	return &AdminLogDetail{
		AdminLogItem: item,
		RawPayload:   safePayload,
	}, nil
}

func redactPayload(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return map[string]interface{}{}
	}
	// Shallow copy + redact sensitive keys.
	out := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") ||
			strings.Contains(lk, "api_key") ||
			strings.Contains(lk, "apikey") ||
			strings.Contains(lk, "authorization") ||
			strings.Contains(lk, "signature") ||
			strings.Contains(lk, "token") {
			out[k] = "[redacted]"
			continue
		}
		out[k] = v
	}
	return out
}

func (s *AdminService) ListLogs(
	ctx context.Context,
	status, providerName, processed string,
	limit, offset int,
) (*PaginatedLogs, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	status = strings.TrimSpace(strings.ToLower(status))
	providerName = strings.TrimSpace(providerName)
	processed = strings.TrimSpace(strings.ToLower(processed))

	var isProcessed *bool
	switch processed {
	case "yes", "true", "1":
		v := true
		isProcessed = &v
	case "no", "false", "0":
		v := false
		isProcessed = &v
	}

	events, err := s.webhookEventRepo.ListAdminFiltered(ctx, status, providerName, isProcessed, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.webhookEventRepo.CountFiltered(ctx, status, providerName, isProcessed)
	if err != nil {
		return nil, err
	}

	items := make([]AdminLogItem, 0, len(events))
	for _, e := range events {
		items = append(items, toAdminLogItem(e))
	}

	return &PaginatedLogs{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

func (s *AdminService) ExportLogs(
	ctx context.Context,
	status, providerName, processed string,
) ([]AdminLogItem, error) {
	result, err := s.ListLogs(ctx, status, providerName, processed, 2000, 0)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func toAdminPaymentItem(p *payment.Payment, merchantName string) AdminPaymentItem {
	env := p.Environment
	if env == "" {
		env = payment.EnvironmentProduction
	}
	item := AdminPaymentItem{
		ID:                p.ID,
		Reference:         p.Reference,
		MerchantID:        p.MerchantID,
		MerchantName:      merchantName,
		Amount:            p.Amount,
		Currency:          p.Currency,
		PaymentMethod:     p.PaymentMethod,
		ProviderName:      p.ProviderName,
		ProviderReference: p.ProviderReference,
		Status:            p.Status,
		Description:       p.Description,
		CustomerName:      p.CustomerName,
		CustomerEmail:     p.CustomerEmail,
		Environment:       env,
		ExpiresAt:         p.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt:         p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.PaidAt != nil {
		v := p.PaidAt.UTC().Format(time.RFC3339)
		item.PaidAt = &v
	}
	return item
}

func toAdminLogItem(e *provider.WebhookEvent) AdminLogItem {
	item := AdminLogItem{
		ID:                e.ID,
		PaymentID:         e.PaymentID,
		ProviderName:      e.ProviderName,
		ProviderReference: e.ProviderReference,
		EventType:         e.EventType,
		Status:            e.Status,
		IsProcessed:       e.IsProcessed,
		ProcessingError:   e.ProcessingError,
		CreatedAt:         e.CreatedAt.UTC().Format(time.RFC3339),
	}
	if e.ProcessedAt != nil {
		v := e.ProcessedAt.UTC().Format(time.RFC3339)
		item.ProcessedAt = &v
	}
	return item
}

type AdminCallbackItem struct {
	ID             uuid.UUID              `json:"id"`
	PaymentID      uuid.UUID              `json:"payment_id"`
	PaymentRef     string                 `json:"payment_reference"`
	MerchantID     uuid.UUID              `json:"merchant_id"`
	MerchantName   string                 `json:"merchant_name"`
	EventType      string                 `json:"event_type"`
	TargetURL      string                 `json:"target_url"`
	RequestPayload map[string]interface{} `json:"request_payload,omitempty"`
	AttemptNumber  int                    `json:"attempt_number"`
	Status         string                 `json:"status"`
	HTTPStatus     *int                   `json:"http_status,omitempty"`
	ResponseBody   *string                `json:"response_body,omitempty"`
	ErrorMessage   *string                `json:"error_message,omitempty"`
	DeliveredAt    *string                `json:"delivered_at,omitempty"`
	NextRetryAt    *string                `json:"next_retry_at,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
}

type PaginatedCallbacks struct {
	Items  []AdminCallbackItem `json:"items"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

func (s *AdminService) ListCallbacks(
	ctx context.Context,
	status string,
	merchantID *uuid.UUID,
	limit, offset int,
) (*PaginatedCallbacks, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if s.callbackRepo == nil {
		return &PaginatedCallbacks{Items: []AdminCallbackItem{}, Limit: limit, Offset: offset}, nil
	}

	status = strings.TrimSpace(strings.ToLower(status))
	rows, err := s.callbackRepo.ListFiltered(ctx, status, merchantID, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.callbackRepo.CountFiltered(ctx, status, merchantID)
	if err != nil {
		return nil, err
	}

	items := make([]AdminCallbackItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAdminCallbackItem(row))
	}
	return &PaginatedCallbacks{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *AdminService) ListPaymentCallbacks(ctx context.Context, paymentID uuid.UUID) ([]AdminCallbackItem, error) {
	if s.callbackRepo == nil {
		return []AdminCallbackItem{}, nil
	}
	rows, err := s.callbackRepo.ListByPayment(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	items := make([]AdminCallbackItem, 0, len(rows))
	for _, d := range rows {
		item := AdminCallbackItem{
			ID:             d.ID,
			PaymentID:      d.PaymentID,
			MerchantID:     d.MerchantID,
			EventType:      d.EventType,
			TargetURL:      d.TargetURL,
			RequestPayload: d.RequestPayload,
			AttemptNumber:  d.AttemptNumber,
			Status:         d.Status,
			HTTPStatus:     d.HTTPStatus,
			ResponseBody:   d.ResponseBody,
			ErrorMessage:   d.ErrorMessage,
			CreatedAt:      d.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:      d.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if d.DeliveredAt != nil {
			v := d.DeliveredAt.UTC().Format(time.RFC3339)
			item.DeliveredAt = &v
		}
		if d.NextRetryAt != nil {
			v := d.NextRetryAt.UTC().Format(time.RFC3339)
			item.NextRetryAt = &v
		}
		items = append(items, item)
	}
	return items, nil
}

func toAdminCallbackItem(row repository.CallbackDeliveryRow) AdminCallbackItem {
	d := row.Delivery
	item := AdminCallbackItem{
		ID:             d.ID,
		PaymentID:      d.PaymentID,
		PaymentRef:     row.Reference,
		MerchantID:     d.MerchantID,
		MerchantName:   row.MerchantName,
		EventType:      d.EventType,
		TargetURL:      d.TargetURL,
		RequestPayload: d.RequestPayload,
		AttemptNumber:  d.AttemptNumber,
		Status:         d.Status,
		HTTPStatus:     d.HTTPStatus,
		ResponseBody:   d.ResponseBody,
		ErrorMessage:   d.ErrorMessage,
		CreatedAt:      d.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      d.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if d.DeliveredAt != nil {
		v := d.DeliveredAt.UTC().Format(time.RFC3339)
		item.DeliveredAt = &v
	}
	if d.NextRetryAt != nil {
		v := d.NextRetryAt.UTC().Format(time.RFC3339)
		item.NextRetryAt = &v
	}
	return item
}
