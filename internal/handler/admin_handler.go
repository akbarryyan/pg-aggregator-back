package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type AdminHandler struct {
	adminPaymentService      *service.AdminPaymentService
	adminMerchantService     *service.AdminMerchantService
	adminProviderService     *service.AdminProviderService
	adminDashboardService    *service.AdminDashboardService
	adminNotificationService *service.AdminNotificationService
	adminLogService          *service.AdminLogService
	adminCallbackService     *service.AdminCallbackService
	paymentService           *service.PaymentService
	apiKeyService            *service.MerchantAPIKeyService
	authService              *service.AuthService
	frontendURL              string
}

func NewAdminHandler(
	paymentService *service.PaymentService,
	frontendURL string,
) *AdminHandler {
	return &AdminHandler{
		paymentService: paymentService,
		frontendURL:    strings.TrimRight(frontendURL, "/"),
	}
}

func (h *AdminHandler) WithAPIKeyService(svc *service.MerchantAPIKeyService) *AdminHandler {
	h.apiKeyService = svc
	return h
}

func (h *AdminHandler) WithAuthService(svc *service.AuthService) *AdminHandler {
	h.authService = svc
	return h
}

// WithMerchantAndPaymentServices wires the AdminMerchantService/
// AdminPaymentService split out of AdminService (project backlog #9).
func (h *AdminHandler) WithMerchantAndPaymentServices(
	adminPaymentService *service.AdminPaymentService,
	adminMerchantService *service.AdminMerchantService,
) *AdminHandler {
	h.adminPaymentService = adminPaymentService
	h.adminMerchantService = adminMerchantService
	return h
}

// WithProviderService wires the AdminProviderService split out of
// AdminService (project backlog #9).
func (h *AdminHandler) WithProviderService(adminProviderService *service.AdminProviderService) *AdminHandler {
	h.adminProviderService = adminProviderService
	return h
}

// WithReportingServices wires the dashboard/notification/log/callback
// services split out of AdminService — the last increment of project
// backlog item #9, after which AdminService itself no longer exists.
func (h *AdminHandler) WithReportingServices(
	dashboard *service.AdminDashboardService,
	notification *service.AdminNotificationService,
	log *service.AdminLogService,
	callback *service.AdminCallbackService,
) *AdminHandler {
	h.adminDashboardService = dashboard
	h.adminNotificationService = notification
	h.adminLogService = log
	h.adminCallbackService = callback
	return h
}

func (h *AdminHandler) GetDashboardSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.adminDashboardService.GetDashboardSummary(r.Context())
	if err != nil {
		logger.Errorf("Failed to get admin dashboard summary: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load dashboard summary")
		return
	}
	respondJSON(w, http.StatusOK, summary)
}

func (h *AdminHandler) GetDashboardCharts(w http.ResponseWriter, r *http.Request) {
	days := 14
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			days = v
		}
	}

	charts, err := h.adminDashboardService.GetDashboardCharts(r.Context(), days)
	if err != nil {
		logger.Errorf("Failed to get admin dashboard charts: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load dashboard charts")
		return
	}
	respondJSON(w, http.StatusOK, charts)
}

func (h *AdminHandler) ListPayments(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 20, 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	dateFrom, dateTo, ok := parseDateRangeQuery(w, r)
	if !ok {
		return
	}

	var merchantID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("merchant_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid merchant_id")
			return
		}
		merchantID = &id
	}

	environment := strings.TrimSpace(r.URL.Query().Get("environment"))
	result, err := h.adminPaymentService.ListPayments(r.Context(), status, search, merchantID, dateFrom, dateTo, environment, limit, offset)
	if err != nil {
		logger.Errorf("Failed to list admin payments: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payments")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	var req payment.CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode create payment request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	p, err := h.paymentService.CreatePayment(r.Context(), &req)
	if err != nil {
		switch err {
		case payment.ErrInvalidAmount,
			payment.ErrUnsupportedCurrency,
			payment.ErrPaymentMethodRequired,
			payment.ErrUnsupportedPaymentMethod,
			payment.ErrMerchantIDRequired,
			payment.ErrDescriptionRequired,
			payment.ErrInvalidExpiration:
			respondError(w, http.StatusBadRequest, err.Error())
		default:
			logger.Errorf("Failed to create payment from admin: %v", err)
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusCreated, payment.ToPaymentResponse(p, h.frontendURL))
}

func (h *AdminHandler) ExportPayments(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	dateFrom, dateTo, ok := parseDateRangeQuery(w, r)
	if !ok {
		return
	}

	var merchantID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("merchant_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid merchant_id")
			return
		}
		merchantID = &id
	}

	environment := strings.TrimSpace(r.URL.Query().Get("environment"))
	rows, err := h.adminPaymentService.ExportPayments(r.Context(), status, search, merchantID, dateFrom, dateTo, environment)
	if err != nil {
		logger.Errorf("Failed to export payments: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to export payments")
		return
	}

	filename := fmt.Sprintf("payments-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"id",
		"reference",
		"merchant_id",
		"merchant_name",
		"amount",
		"currency",
		"payment_method",
		"provider_name",
		"provider_reference",
		"status",
		"description",
		"customer_name",
		"customer_email",
		"expires_at",
		"paid_at",
		"created_at",
		"updated_at",
	})

	for _, item := range rows {
		providerRef := ""
		if item.ProviderReference != nil {
			providerRef = *item.ProviderReference
		}
		customerName := ""
		if item.CustomerName != nil {
			customerName = *item.CustomerName
		}
		customerEmail := ""
		if item.CustomerEmail != nil {
			customerEmail = *item.CustomerEmail
		}
		paidAt := ""
		if item.PaidAt != nil {
			paidAt = *item.PaidAt
		}

		_ = writer.Write([]string{
			item.ID.String(),
			item.Reference,
			item.MerchantID.String(),
			item.MerchantName,
			strconv.FormatInt(item.Amount, 10),
			item.Currency,
			item.PaymentMethod,
			item.ProviderName,
			providerRef,
			item.Status,
			item.Description,
			customerName,
			customerEmail,
			item.ExpiresAt,
			paidAt,
			item.CreatedAt,
			item.UpdatedAt,
		})
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		logger.Errorf("Failed to write payment CSV: %v", err)
	}
}

func (h *AdminHandler) GetPayment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}

	detail, err := h.adminPaymentService.GetPayment(r.Context(), id)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		logger.Errorf("Failed to get admin payment: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get payment")
		return
	}
	respondJSON(w, http.StatusOK, detail)
}

func (h *AdminHandler) ListMerchants(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 20, 0)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	result, err := h.adminMerchantService.ListMerchants(r.Context(), search, status, limit, offset)
	if err != nil {
		logger.Errorf("Failed to list admin merchants: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list merchants")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) CreateMerchant(w http.ResponseWriter, r *http.Request) {
	var req merchant.CreateMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode create merchant request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	created, err := h.adminMerchantService.CreateMerchant(r.Context(), &req)
	if err != nil {
		switch err {
		case merchant.ErrMerchantNameRequired,
			merchant.ErrMerchantEmailRequired,
			merchant.ErrBusinessNameRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		case merchant.ErrMerchantAlreadyExists:
			respondError(w, http.StatusConflict, "Merchant with this email already exists")
		default:
			logger.Errorf("Failed to create merchant: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to create merchant")
		}
		return
	}

	respondJSON(w, http.StatusCreated, created)
}

func (h *AdminHandler) ExportMerchants(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	rows, err := h.adminMerchantService.ExportMerchants(r.Context(), search, status)
	if err != nil {
		logger.Errorf("Failed to export merchants: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to export merchants")
		return
	}

	filename := fmt.Sprintf("merchants-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"id",
		"name",
		"email",
		"phone",
		"business_name",
		"webhook_url",
		"is_active",
		"created_at",
		"updated_at",
	})

	for _, m := range rows {
		webhookURL := ""
		if m.WebhookURL != nil {
			webhookURL = *m.WebhookURL
		}
		_ = writer.Write([]string{
			m.ID.String(),
			m.Name,
			m.Email,
			m.Phone,
			m.BusinessName,
			webhookURL,
			strconv.FormatBool(m.IsActive),
			m.CreatedAt.UTC().Format(time.RFC3339),
			m.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		logger.Errorf("Failed to write merchant CSV: %v", err)
	}
}

func (h *AdminHandler) GetMerchant(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}

	m, err := h.adminMerchantService.GetMerchant(r.Context(), id)
	if err != nil {
		if err == merchant.ErrMerchantNotFound {
			respondError(w, http.StatusNotFound, "Merchant not found")
			return
		}
		logger.Errorf("Failed to get admin merchant: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get merchant")
		return
	}
	respondJSON(w, http.StatusOK, m)
}

func (h *AdminHandler) UpdateMerchant(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}

	var req merchant.UpdateMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	m, err := h.adminMerchantService.UpdateMerchant(r.Context(), id, &req)
	if err != nil {
		switch err {
		case merchant.ErrMerchantNotFound:
			respondError(w, http.StatusNotFound, "Merchant not found")
		case merchant.ErrMerchantNameRequired, merchant.ErrBusinessNameRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		default:
			logger.Errorf("Failed to update merchant: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to update merchant")
		}
		return
	}
	respondJSON(w, http.StatusOK, m)
}

func (h *AdminHandler) SetMerchantActive(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}

	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	m, err := h.adminMerchantService.SetMerchantActive(r.Context(), id, body.IsActive)
	if err != nil {
		if err == merchant.ErrMerchantNotFound {
			respondError(w, http.StatusNotFound, "Merchant not found")
			return
		}
		logger.Errorf("Failed to set merchant active: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to update merchant status")
		return
	}
	respondJSON(w, http.StatusOK, m)
}

func (h *AdminHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": h.adminProviderService.ListProviders(),
	})
}

func (h *AdminHandler) GetProvider(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(mux.Vars(r)["name"])
	detail, err := h.adminProviderService.GetProviderDetail(r.Context(), name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			respondError(w, http.StatusNotFound, "Provider not found")
			return
		}
		logger.Errorf("Failed to get provider detail: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get provider")
		return
	}
	respondJSON(w, http.StatusOK, detail)
}

func (h *AdminHandler) UpdateProviderHealth(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(mux.Vars(r)["name"])
	var req domainProvider.UpdateProviderHealthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	detail, err := h.adminProviderService.UpdateProviderHealth(r.Context(), name, &req)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			respondError(w, http.StatusNotFound, "Provider not found")
			return
		}
		if err == domainProvider.ErrInvalidProviderHealthStatus {
			respondError(w, http.StatusBadRequest, "Invalid health status (use healthy, degraded, or unhealthy)")
			return
		}
		logger.Errorf("Failed to update provider health: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to update provider health")
		return
	}
	respondJSON(w, http.StatusOK, detail)
}

func (h *AdminHandler) ListMerchantProviderConfigs(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	items, err := h.adminMerchantService.ListMerchantProviderConfigs(r.Context(), id)
	if err != nil {
		if err == merchant.ErrMerchantNotFound {
			respondError(w, http.StatusNotFound, "Merchant not found")
			return
		}
		logger.Errorf("Failed to list merchant provider configs: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list provider configs")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *AdminHandler) UpsertMerchantProviderConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}

	var req domainProvider.UpsertMerchantProviderConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	items, err := h.adminMerchantService.UpsertMerchantProviderConfig(r.Context(), id, &req)
	if err != nil {
		switch err {
		case merchant.ErrMerchantNotFound:
			respondError(w, http.StatusNotFound, "Merchant not found")
		case domainProvider.ErrProviderConfigProviderRequired,
			domainProvider.ErrProviderConfigPaymentMethodRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		default:
			logger.Errorf("Failed to upsert merchant provider config: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to save provider config")
		}
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *AdminHandler) DeleteMerchantProviderConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	providerName := strings.TrimSpace(r.URL.Query().Get("provider_name"))
	paymentMethod := strings.TrimSpace(r.URL.Query().Get("payment_method"))
	if providerName == "" || paymentMethod == "" {
		respondError(w, http.StatusBadRequest, "provider_name and payment_method are required")
		return
	}

	if err := h.adminMerchantService.DeleteMerchantProviderConfig(r.Context(), id, paymentMethod, providerName); err != nil {
		logger.Errorf("Failed to delete merchant provider config: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete provider config")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "Provider config deleted"})
}

func (h *AdminHandler) ListRouting(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 50, 0)
	result, err := h.adminProviderService.ListRouting(r.Context(), limit, offset)
	if err != nil {
		logger.Errorf("Failed to list routing: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list routing")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) ListReconciliation(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	result, err := h.adminProviderService.ListReconciliationCandidates(r.Context(), limit)
	if err != nil {
		logger.Errorf("Failed to list reconciliation candidates: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list reconciliation candidates")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) CheckReconciliationPayment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}
	if h.paymentService == nil {
		respondError(w, http.StatusInternalServerError, "Payment service unavailable")
		return
	}
	result, err := h.paymentService.ReconcilePayment(r.Context(), id)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		logger.Errorf("Failed to reconcile payment: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to reconcile payment")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) CheckReconciliationBatch(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if h.paymentService == nil {
		respondError(w, http.StatusInternalServerError, "Payment service unavailable")
		return
	}
	results, err := h.paymentService.ReconcilePendingPayments(r.Context(), limit)
	if err != nil {
		logger.Errorf("Failed to batch reconcile: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to run batch reconciliation")
		return
	}

	updated, unchanged, expired, skipped, errored := 0, 0, 0, 0, 0
	for _, item := range results {
		switch item.Action {
		case "updated":
			updated++
		case "expired_local":
			expired++
		case "skipped":
			skipped++
		case "error":
			errored++
		default:
			unchanged++
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": results,
		"summary": map[string]int{
			"total":     len(results),
			"updated":   updated,
			"expired":   expired,
			"unchanged": unchanged,
			"skipped":   skipped,
			"errors":    errored,
		},
	})
}

func (h *AdminHandler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}

	result, err := h.adminNotificationService.ListNotifications(r.Context(), limit)
	if err != nil {
		logger.Errorf("Failed to list admin notifications: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load notifications")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) ListPaymentEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}

	events, err := h.adminLogService.ListPaymentEvents(r.Context(), id)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		logger.Errorf("Failed to list payment events: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payment events")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"items": events})
}

func (h *AdminHandler) GetLog(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid log ID")
		return
	}

	detail, err := h.adminLogService.GetLog(r.Context(), id)
	if err != nil {
		if err == service.ErrWebhookEventNotFound {
			respondError(w, http.StatusNotFound, "Log event not found")
			return
		}
		logger.Errorf("Failed to get log: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get log")
		return
	}
	respondJSON(w, http.StatusOK, detail)
}

func (h *AdminHandler) ListMerchantAPIKeys(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	if h.apiKeyService == nil {
		respondError(w, http.StatusInternalServerError, "API key service unavailable")
		return
	}
	items, err := h.apiKeyService.List(r.Context(), id)
	if err != nil {
		if err == merchant.ErrMerchantNotFound {
			respondError(w, http.StatusNotFound, "Merchant not found")
			return
		}
		logger.Errorf("Failed to list API keys: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list API keys")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *AdminHandler) UpsertMerchantAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	if h.apiKeyService == nil || h.authService == nil {
		respondError(w, http.StatusInternalServerError, "API key service unavailable")
		return
	}

	var req merchant.UpsertAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "Admin password is required to update API key")
		return
	}

	adminID, ok := middleware.AdminIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if err := h.authService.VerifyAdminPassword(r.Context(), adminID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid admin password")
		return
	}

	result, err := h.apiKeyService.Upsert(r.Context(), id, &req)
	if err != nil {
		switch err {
		case merchant.ErrMerchantNotFound:
			respondError(w, http.StatusNotFound, "Merchant not found")
		case merchant.ErrAPIKeyMerchantInactive:
			respondError(w, http.StatusBadRequest, "Cannot update API key for inactive merchant")
		case merchant.ErrAPIKeyPasswordRequired:
			respondError(w, http.StatusBadRequest, "Admin password is required")
		default:
			logger.Errorf("Failed to update API key: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to update API key")
		}
		return
	}

	status := http.StatusCreated
	if result.Rotated {
		status = http.StatusOK
	}
	respondJSON(w, status, result)
}

func (h *AdminHandler) DeleteMerchantAPIKey(w http.ResponseWriter, r *http.Request) {
	merchantID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	keyID, err := uuid.Parse(mux.Vars(r)["keyId"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid API key ID")
		return
	}
	if h.apiKeyService == nil || h.authService == nil {
		respondError(w, http.StatusInternalServerError, "API key service unavailable")
		return
	}

	var req merchant.DeleteAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body. Password is required.")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "Admin password is required to delete API key")
		return
	}

	adminID, ok := middleware.AdminIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if err := h.authService.VerifyAdminPassword(r.Context(), adminID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid admin password")
		return
	}

	if err := h.apiKeyService.Delete(r.Context(), merchantID, keyID); err != nil {
		switch err {
		case merchant.ErrMerchantNotFound:
			respondError(w, http.StatusNotFound, "Merchant not found")
		case merchant.ErrAPIKeyNotFound:
			respondError(w, http.StatusNotFound, "API key not found")
		default:
			logger.Errorf("Failed to delete API key: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to delete API key")
		}
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "API key deleted"})
}

func (h *AdminHandler) ListMerchantPayments(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}

	limit, offset := parseLimitOffset(r, 20, 0)
	result, err := h.adminMerchantService.ListMerchantPayments(r.Context(), id, limit, offset)
	if err != nil {
		logger.Errorf("Failed to list merchant payments: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list merchant payments")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 50, 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	providerName := strings.TrimSpace(r.URL.Query().Get("provider"))
	processed := strings.TrimSpace(r.URL.Query().Get("processed"))
	result, err := h.adminLogService.ListLogs(r.Context(), status, providerName, processed, limit, offset)
	if err != nil {
		logger.Errorf("Failed to list admin logs: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list logs")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) ListCallbacks(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 50, 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	var merchantID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("merchant_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid merchant_id")
			return
		}
		merchantID = &id
	}

	result, err := h.adminCallbackService.ListCallbacks(r.Context(), status, merchantID, limit, offset)
	if err != nil {
		logger.Errorf("Failed to list callbacks: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list callbacks")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) ListPaymentCallbacks(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}
	items, err := h.adminCallbackService.ListPaymentCallbacks(r.Context(), id)
	if err != nil {
		logger.Errorf("Failed to list payment callbacks: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payment callbacks")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *AdminHandler) RetryCallback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid callback ID")
		return
	}
	if h.paymentService == nil {
		respondError(w, http.StatusInternalServerError, "Payment service unavailable")
		return
	}
	delivery, err := h.paymentService.RetryMerchantCallback(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			respondError(w, http.StatusNotFound, "Callback delivery not found")
			return
		}
		logger.Errorf("Failed to retry callback: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to retry callback")
		return
	}
	respondJSON(w, http.StatusOK, delivery)
}

func (h *AdminHandler) ExportLogs(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	providerName := strings.TrimSpace(r.URL.Query().Get("provider"))
	processed := strings.TrimSpace(r.URL.Query().Get("processed"))

	rows, err := h.adminLogService.ExportLogs(r.Context(), status, providerName, processed)
	if err != nil {
		logger.Errorf("Failed to export logs: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to export logs")
		return
	}

	filename := fmt.Sprintf("logs-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"id", "payment_id", "provider_name", "provider_reference", "event_type",
		"status", "is_processed", "processing_error", "processed_at", "created_at",
	})
	for _, item := range rows {
		paymentID := ""
		if item.PaymentID != nil {
			paymentID = item.PaymentID.String()
		}
		errMsg := ""
		if item.ProcessingError != nil {
			errMsg = *item.ProcessingError
		}
		processedAt := ""
		if item.ProcessedAt != nil {
			processedAt = *item.ProcessedAt
		}
		_ = writer.Write([]string{
			item.ID.String(),
			paymentID,
			item.ProviderName,
			item.ProviderReference,
			item.EventType,
			item.Status,
			strconv.FormatBool(item.IsProcessed),
			errMsg,
			processedAt,
			item.CreatedAt,
		})
	}
	writer.Flush()
}

func parseLimitOffset(r *http.Request, defaultLimit, defaultOffset int) (int, int) {
	limit := defaultLimit
	offset := defaultOffset

	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}

	if raw := r.URL.Query().Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}

	return limit, offset
}

// parseDateRangeQuery reads date_from / date_to as YYYY-MM-DD (UTC day bounds).
// date_to is inclusive on the calendar day (stored as exclusive next-day midnight).
func parseDateRangeQuery(w http.ResponseWriter, r *http.Request) (from, to *time.Time, ok bool) {
	rawFrom := strings.TrimSpace(r.URL.Query().Get("date_from"))
	rawTo := strings.TrimSpace(r.URL.Query().Get("date_to"))

	if rawFrom != "" {
		t, err := time.ParseInLocation("2006-01-02", rawFrom, time.UTC)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid date_from (use YYYY-MM-DD)")
			return nil, nil, false
		}
		from = &t
	}
	if rawTo != "" {
		t, err := time.ParseInLocation("2006-01-02", rawTo, time.UTC)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid date_to (use YYYY-MM-DD)")
			return nil, nil, false
		}
		// Inclusive end-of-day → exclusive next day
		next := t.Add(24 * time.Hour)
		to = &next
	}
	if from != nil && to != nil && !from.Before(*to) {
		respondError(w, http.StatusBadRequest, "date_from must be before or equal to date_to")
		return nil, nil, false
	}
	return from, to, true
}
