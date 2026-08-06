package service

import (
	"context"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// AdminDashboardService owns global and merchant-scoped dashboard
// summaries/charts. Split out of the former AdminService god-object — see
// project backlog item #9 (this is the last increment; AdminService itself
// is retired after this split). Composes AdminPaymentService for
// merchant-scoped status breakdowns rather than duplicating that logic.
type AdminDashboardService struct {
	merchantRepo     *repository.MerchantRepository
	paymentRepo      *repository.PaymentRepository
	webhookEventRepo *repository.WebhookEventRepository
	paymentService   *AdminPaymentService
}

func NewAdminDashboardService(
	merchantRepo *repository.MerchantRepository,
	paymentRepo *repository.PaymentRepository,
	webhookEventRepo *repository.WebhookEventRepository,
	paymentService *AdminPaymentService,
) *AdminDashboardService {
	return &AdminDashboardService{
		merchantRepo:     merchantRepo,
		paymentRepo:      paymentRepo,
		webhookEventRepo: webhookEventRepo,
		paymentService:   paymentService,
	}
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
	Days            int                    `json:"days"`
	Daily           []DashboardDailyPoint  `json:"daily"`
	StatusBreakdown []DashboardStatusPoint `json:"status_breakdown"`
}

func (s *AdminDashboardService) GetDashboardSummary(ctx context.Context) (*DashboardSummary, error) {
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
func (s *AdminDashboardService) MerchantDailyStats(
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
	// Status breakdown for this merchant+env in one query (project backlog #6).
	stats, err := s.paymentService.StatusBreakdown(ctx, &merchantID, env)
	if err != nil {
		return nil, err
	}
	breakdown := []DashboardStatusPoint{
		{Status: "paid", Count: stats.Paid},
		{Status: "pending", Count: stats.Pending},
		{Status: "failed", Count: stats.Failed},
		{Status: "expired", Count: stats.Expired},
	}
	return fillDailyCharts(raw, days, breakdown), nil
}

func (s *AdminDashboardService) GetDashboardCharts(ctx context.Context, days int) (*DashboardCharts, error) {
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
		Days:            days,
		Daily:           daily,
		StatusBreakdown: statusBreakdown,
	}
}
