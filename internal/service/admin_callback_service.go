package service

import (
	"context"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

// AdminCallbackService owns the admin/merchant view over outbound
// merchant-webhook callback deliveries (list + per-payment history). Split
// out of the former AdminService god-object — see project backlog item #9.
// Retrying a callback lives on PaymentService (RetryMerchantCallback), not
// here — this service is read-only reporting over what's already happened.
type AdminCallbackService struct {
	callbackRepo *repository.MerchantCallbackRepository
}

func NewAdminCallbackService(callbackRepo *repository.MerchantCallbackRepository) *AdminCallbackService {
	return &AdminCallbackService{callbackRepo: callbackRepo}
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

func (s *AdminCallbackService) ListCallbacks(
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

func (s *AdminCallbackService) ListPaymentCallbacks(ctx context.Context, paymentID uuid.UUID) ([]AdminCallbackItem, error) {
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
