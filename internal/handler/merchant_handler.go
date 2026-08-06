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
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type MerchantHandler struct {
	adminPaymentService      *service.AdminPaymentService
	adminMerchantService     *service.AdminMerchantService
	adminDashboardService    *service.AdminDashboardService
	adminNotificationService *service.AdminNotificationService
	adminCallbackService     *service.AdminCallbackService
	paymentService           *service.PaymentService
	apiKeyService            *service.MerchantAPIKeyService
	authService              *service.AuthService
	frontendURL              string
}

func NewMerchantHandler(
	paymentService *service.PaymentService,
	apiKeyService *service.MerchantAPIKeyService,
	authService *service.AuthService,
	frontendURL string,
) *MerchantHandler {
	return &MerchantHandler{
		paymentService: paymentService,
		apiKeyService:  apiKeyService,
		authService:    authService,
		frontendURL:    strings.TrimRight(frontendURL, "/"),
	}
}

// WithMerchantAndPaymentServices wires the AdminMerchantService/
// AdminPaymentService split out of AdminService (project backlog #9).
func (h *MerchantHandler) WithMerchantAndPaymentServices(
	adminPaymentService *service.AdminPaymentService,
	adminMerchantService *service.AdminMerchantService,
) *MerchantHandler {
	h.adminPaymentService = adminPaymentService
	h.adminMerchantService = adminMerchantService
	return h
}

// WithReportingServices wires the dashboard/notification/callback services
// split out of AdminService — the last increment of project backlog item
// #9, after which AdminService itself no longer exists.
func (h *MerchantHandler) WithReportingServices(
	dashboard *service.AdminDashboardService,
	notification *service.AdminNotificationService,
	callback *service.AdminCallbackService,
) *MerchantHandler {
	h.adminDashboardService = dashboard
	h.adminNotificationService = notification
	h.adminCallbackService = callback
	return h
}

func merchantEnvFromQuery(r *http.Request) string {
	return payment.NormalizeEnvironment(strings.TrimSpace(r.URL.Query().Get("environment")))
}

func (h *MerchantHandler) GetDashboardCharts(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	env := merchantEnvFromQuery(r)
	days := 14
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			days = v
		}
	}
	if days > 90 {
		days = 90
	}

	raw, err := h.adminDashboardService.MerchantDailyStats(r.Context(), merchantID, env, days)
	if err != nil {
		logger.Errorf("merchant charts: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load charts")
		return
	}
	respondJSON(w, http.StatusOK, raw)
}

func (h *MerchantHandler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	env := merchantEnvFromQuery(r)
	result, err := h.adminNotificationService.ListMerchantNotifications(r.Context(), merchantID, env, 30)
	if err != nil {
		logger.Errorf("merchant notifications: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load notifications")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *MerchantHandler) ListCallbacks(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	result, err := h.adminCallbackService.ListCallbacks(r.Context(), status, &merchantID, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list callbacks")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *MerchantHandler) GetDashboardSummary(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	env := merchantEnvFromQuery(r)

	// One aggregate query instead of 6 ListPayments calls (12 SQL
	// round-trips) — also fixes a latent bug where paid_amount only summed
	// the 100 most recent paid payments instead of all of them.
	stats, err := h.adminPaymentService.StatusBreakdown(r.Context(), &merchantID, env)
	if err != nil {
		logger.Errorf("merchant dashboard summary: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load dashboard summary")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"environment":      env,
		"total_payments":   stats.Total,
		"paid_payments":    stats.Paid,
		"pending_payments": stats.Pending,
		"failed_payments":  stats.Failed,
		"expired_payments": stats.Expired,
		"paid_amount":      stats.PaidAmount,
	})
}

func (h *MerchantHandler) ListPayments(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	env := merchantEnvFromQuery(r)
	dateFrom, dateTo, ok := parseDateRangeQuery(w, r)
	if !ok {
		return
	}
	result, err := h.adminPaymentService.ListPayments(r.Context(), status, search, &merchantID, dateFrom, dateTo, env, limit, offset)
	if err != nil {
		logger.Errorf("merchant list payments: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payments")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *MerchantHandler) ExportPayments(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	env := merchantEnvFromQuery(r)
	dateFrom, dateTo, ok := parseDateRangeQuery(w, r)
	if !ok {
		return
	}

	rows, err := h.adminPaymentService.ExportPayments(r.Context(), status, search, &merchantID, dateFrom, dateTo, env)
	if err != nil {
		logger.Errorf("merchant export payments: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to export payments")
		return
	}

	filename := fmt.Sprintf("merchant-payments-%s-%s.csv", env, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"id", "reference", "amount", "currency", "payment_method",
		"provider_name", "provider_reference", "status", "description",
		"customer_name", "customer_email", "environment", "expires_at", "paid_at", "created_at",
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
			strconv.FormatInt(item.Amount, 10),
			item.Currency,
			item.PaymentMethod,
			item.ProviderName,
			providerRef,
			item.Status,
			item.Description,
			customerName,
			customerEmail,
			item.Environment,
			item.ExpiresAt,
			paidAt,
			item.CreatedAt,
		})
	}
	writer.Flush()
}

func (h *MerchantHandler) GetPayment(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}
	p, err := h.paymentService.GetPayment(r.Context(), id)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to get payment")
		return
	}
	if p.MerchantID != merchantID {
		respondError(w, http.StatusNotFound, "Payment not found")
		return
	}
	respondJSON(w, http.StatusOK, payment.ToPaymentResponse(p, h.frontendURL))
}

func (h *MerchantHandler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	var req payment.CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.MerchantID = merchantID
	// Prefer body environment, else query ?environment=
	if strings.TrimSpace(req.Environment) == "" {
		req.Environment = strings.TrimSpace(r.URL.Query().Get("environment"))
	}
	req.Environment = payment.NormalizeEnvironment(req.Environment)
	if err := req.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := h.paymentService.CreatePayment(r.Context(), &req)
	if err != nil {
		logger.Errorf("merchant create payment: %v", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, payment.ToPaymentResponse(p, h.frontendURL))
}

func (h *MerchantHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	items, err := h.apiKeyService.List(r.Context(), merchantID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list API keys")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *MerchantHandler) UpsertAPIKey(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	userID, ok := middleware.MerchantUserIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	var req merchant.UpsertAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "Password is required to update API key")
		return
	}
	if err := h.authService.VerifyMerchantPassword(r.Context(), userID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid password")
		return
	}
	result, err := h.apiKeyService.Upsert(r.Context(), merchantID, &req)
	if err != nil {
		if err == merchant.ErrAPIKeyMerchantInactive {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		logger.Errorf("merchant upsert api key: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to update API key")
		return
	}
	status := http.StatusCreated
	if result.Rotated {
		status = http.StatusOK
	}
	respondJSON(w, status, result)
}

func (h *MerchantHandler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	userID, ok := middleware.MerchantUserIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	keyID, err := uuid.Parse(mux.Vars(r)["keyId"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid API key ID")
		return
	}
	var req merchant.DeleteAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Password is required")
		return
	}
	if err := h.authService.VerifyMerchantPassword(r.Context(), userID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid password")
		return
	}
	if err := h.apiKeyService.Delete(r.Context(), merchantID, keyID); err != nil {
		if err == merchant.ErrAPIKeyNotFound {
			respondError(w, http.StatusNotFound, "API key not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to delete API key")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "API key deleted"})
}

func (h *MerchantHandler) GetBusiness(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	m, err := h.adminMerchantService.GetMerchant(r.Context(), merchantID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load business profile")
		return
	}
	respondJSON(w, http.StatusOK, m)
}

func (h *MerchantHandler) UpdateBusiness(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	var req merchant.UpdateMerchantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, err := h.adminMerchantService.UpdateMerchant(r.Context(), merchantID, &req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, m)
}

// GetWebhookSecret returns the merchant's HMAC signing secret for outbound
// payment webhooks, generating one on first access.
func (h *MerchantHandler) GetWebhookSecret(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	secret, err := h.paymentService.EnsureMerchantWebhookSecret(r.Context(), merchantID)
	if err != nil {
		logger.Errorf("Failed to get webhook secret for merchant %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to load webhook secret")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret})
}

// RegenerateWebhookSecret issues a new signing secret, invalidating the
// previous one immediately.
func (h *MerchantHandler) RegenerateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	secret, err := h.paymentService.RegenerateMerchantWebhookSecret(r.Context(), merchantID)
	if err != nil {
		logger.Errorf("Failed to regenerate webhook secret for merchant %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to regenerate webhook secret")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret})
}
