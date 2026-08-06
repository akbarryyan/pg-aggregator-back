package service

import (
	"context"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// AdminPaymentService is the admin-facing view over payments: list/export
// for the admin payments table, and single-payment detail. Split out of the
// former AdminService god-object (see docs/payment-state-machine.md for the
// underlying payment lifecycle these views read from) — see project backlog
// item #9. AdminMerchantService composes this for merchant-scoped payment
// views instead of duplicating the pagination/formatting logic.
type AdminPaymentService struct {
	paymentRepo  *repository.PaymentRepository
	merchantRepo *repository.MerchantRepository
}

func NewAdminPaymentService(
	paymentRepo *repository.PaymentRepository,
	merchantRepo *repository.MerchantRepository,
) *AdminPaymentService {
	return &AdminPaymentService{
		paymentRepo:  paymentRepo,
		merchantRepo: merchantRepo,
	}
}

// StatusBreakdown returns payment counts per status (+ total paid amount) in
// a single query, optionally scoped by merchant/environment. Use this
// instead of calling ListPayments once per status — each ListPayments call
// is itself two queries (list + count), so building a status summary that
// way costs 2N SQL round-trips for N statuses checked.
func (s *AdminPaymentService) StatusBreakdown(ctx context.Context, merchantID *uuid.UUID, environment string) (*repository.StatusBreakdown, error) {
	return s.paymentRepo.StatusBreakdownFiltered(ctx, merchantID, environment)
}

type AdminPaymentItem struct {
	ID                uuid.UUID `json:"id"`
	Reference         string    `json:"reference"`
	MerchantID        uuid.UUID `json:"merchant_id"`
	MerchantName      string    `json:"merchant_name"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	PaymentMethod     string    `json:"payment_method"`
	ProviderName      string    `json:"provider_name"`
	ProviderReference *string   `json:"provider_reference,omitempty"`
	Status            string    `json:"status"`
	Description       string    `json:"description"`
	CustomerName      *string   `json:"customer_name,omitempty"`
	CustomerEmail     *string   `json:"customer_email,omitempty"`
	Environment       string    `json:"environment"`
	ExpiresAt         string    `json:"expires_at"`
	PaidAt            *string   `json:"paid_at,omitempty"`
	CreatedAt         string    `json:"created_at"`
	UpdatedAt         string    `json:"updated_at"`
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

func (s *AdminPaymentService) ListPayments(
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

func (s *AdminPaymentService) ExportPayments(
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

func (s *AdminPaymentService) GetPayment(ctx context.Context, id uuid.UUID) (*AdminPaymentDetail, error) {
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
