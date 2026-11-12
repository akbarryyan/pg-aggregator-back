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

// WebhookEventDailyPoint is one day of the webhook-events chart.
type WebhookEventDailyPoint struct {
	Date      string `json:"date"`
	Label     string `json:"label"`
	Processed int64  `json:"processed"`
	Failed    int64  `json:"failed"`
}

// WebhookEventStats is the summary payload for the admin Logs page.
// processed = is_processed; ignored = processing_error is exactly one of the
// benign sentinel messages (non-settlement webhook, duplicate webhook, payment
// already terminal, provider test event); failed = not ignored and
// (processing_error set or status rejected/failed). ignored is only in the
// totals.
type WebhookEventStats struct {
	Days      int                      `json:"days"`
	Total     int64                    `json:"total"`
	Processed int64                    `json:"processed"`
	Failed    int64                    `json:"failed"`
	Ignored   int64                    `json:"ignored"`
	Daily     []WebhookEventDailyPoint `json:"daily"`
	Breakdown []BreakdownPoint         `json:"breakdown"`
}

// Stats returns the webhook-event summary and the per-day outcome of the last
// pageStatsDays days (days without events are present with zeros).
func (s *AdminLogService) Stats(ctx context.Context) (*WebhookEventStats, error) {
	raw, err := s.webhookEventRepo.Stats(ctx, pageStatsDays, benignWebhookErrors())
	if err != nil {
		return nil, err
	}
	return buildWebhookEventStats(raw, pageStatsDays, time.Now()), nil
}

func buildWebhookEventStats(raw *repository.WebhookEventStats, days int, now time.Time) *WebhookEventStats {
	out := &WebhookEventStats{
		Days:      days,
		Total:     raw.Total,
		Processed: raw.Processed,
		Failed:    raw.Failed,
		Ignored:   raw.Ignored,
		Daily:     make([]WebhookEventDailyPoint, 0, days),
		Breakdown: toBreakdown(raw.Breakdown),
	}
	for _, d := range statsWindow(days, now) {
		a := raw.DailyByDay[d.Key]
		out.Daily = append(out.Daily, WebhookEventDailyPoint{Date: d.Key, Label: d.Label, Processed: a.Processed, Failed: a.Failed})
	}
	return out
}

// benignWebhookErrors lists the processing_error texts the webhook service
// stores for intentional non-failures (every failWebhookEvent call in
// payment_webhook_service.go passes one of these sentinels unwrapped).
func benignWebhookErrors() []string {
	return []string{
		payment.ErrWebhookIgnored.Error(),
		payment.ErrDuplicateWebhook.Error(),
		payment.ErrPaymentAlreadyTerminal.Error(),
		providerPkg.ErrTestWebhookEvent.Error(),
	}
}
