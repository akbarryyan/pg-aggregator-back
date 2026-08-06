package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// AdminNotificationService builds the operational alert feed (global, for
// the admin bell drawer, and merchant-scoped, for the merchant dashboard)
// from failed callbacks, failed/expired payments, and unprocessed/errored
// webhook events. Split out of the former AdminService god-object — see
// project backlog item #9.
type AdminNotificationService struct {
	paymentRepo      *repository.PaymentRepository
	webhookEventRepo *repository.WebhookEventRepository
	callbackRepo     *repository.MerchantCallbackRepository
}

func NewAdminNotificationService(
	paymentRepo *repository.PaymentRepository,
	webhookEventRepo *repository.WebhookEventRepository,
) *AdminNotificationService {
	return &AdminNotificationService{
		paymentRepo:      paymentRepo,
		webhookEventRepo: webhookEventRepo,
	}
}

func (s *AdminNotificationService) WithCallbackRepo(repo *repository.MerchantCallbackRepository) *AdminNotificationService {
	s.callbackRepo = repo
	return s
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

func (s *AdminNotificationService) ListNotifications(ctx context.Context, limit int) (*NotificationsResponse, error) {
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

// ListMerchantNotifications builds operational alerts for a merchant (scoped env).
func (s *AdminNotificationService) ListMerchantNotifications(
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
