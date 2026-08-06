package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

var ErrWebhookEventNotFound = errors.New("webhook event not found")

// AdminLogService owns the admin view over raw provider webhook events:
// per-payment event history, single-event detail (with secrets redacted),
// and filtered/paginated listing. Split out of the former AdminService
// god-object — see project backlog item #9.
type AdminLogService struct {
	paymentRepo      *repository.PaymentRepository
	webhookEventRepo *repository.WebhookEventRepository
}

func NewAdminLogService(
	paymentRepo *repository.PaymentRepository,
	webhookEventRepo *repository.WebhookEventRepository,
) *AdminLogService {
	return &AdminLogService{
		paymentRepo:      paymentRepo,
		webhookEventRepo: webhookEventRepo,
	}
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

type AdminLogDetail struct {
	AdminLogItem
	// Raw payload is included for admin debugging; strip secrets at handler if needed.
	RawPayload map[string]interface{} `json:"raw_payload,omitempty"`
}

func (s *AdminLogService) ListPaymentEvents(ctx context.Context, paymentID uuid.UUID) ([]AdminLogItem, error) {
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

func (s *AdminLogService) GetLog(ctx context.Context, id uuid.UUID) (*AdminLogDetail, error) {
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

func (s *AdminLogService) ListLogs(
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

func (s *AdminLogService) ExportLogs(
	ctx context.Context,
	status, providerName, processed string,
) ([]AdminLogItem, error) {
	result, err := s.ListLogs(ctx, status, providerName, processed, 2000, 0)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
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
