package handler

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
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
	balanceService           merchantBalanceAPI
	payoutService            merchantPayoutAPI
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
		logger.ErrorfCtx(r.Context(), "merchant charts: %v", err)
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
		logger.ErrorfCtx(r.Context(), "merchant notifications: %v", err)
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
		logger.ErrorfCtx(r.Context(), "merchant dashboard summary: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load dashboard summary")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"environment":       env,
		"total_payments":    stats.Total,
		"paid_payments":     stats.Paid,
		"pending_payments":  stats.Pending,
		"failed_payments":   stats.Failed,
		"expired_payments":  stats.Expired,
		"refunded_payments": stats.Refunded,
		"paid_amount":       stats.PaidAmount,
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
		logger.ErrorfCtx(r.Context(), "merchant list payments: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payments")
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (h *MerchantHandler) ExportPayments(w http.ResponseWriter, r *http.Request) {
	extendWriteDeadline(w, exportWriteTimeout)
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
		logger.ErrorfCtx(r.Context(), "merchant export payments: %v", err)
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
		"fee_amount", "total_amount",
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
			strconv.FormatInt(item.FeeAmount, 10),
			strconv.FormatInt(item.TotalAmount, 10),
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
	detail := payment.ToPaymentResponse(p, h.frontendURL)
	detail.QRImageURL = h.paymentService.QRImageURL(p)
	respondJSON(w, http.StatusOK, merchantPaymentDetail{
		PaymentResponse:   detail,
		MerchantNetAmount: service.MerchantReceives(p),
	})
}

// merchantPaymentDetail is the dashboard's payment detail: the regular payment
// response plus what the merchant receives (the stored net once paid, the
// expected net before that). Dashboard-only, so the API-key payment response
// and the public checkout stay unchanged.
type merchantPaymentDetail struct {
	*payment.PaymentResponse
	MerchantNetAmount int64 `json:"merchant_net_amount"`
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
	if err := req.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	idemKey, err := idempotencyKeyFromRequest(r)
	if err != nil {
		respondIdempotencyError(w, err)
		return
	}
	res, err := h.paymentService.CreatePaymentIdempotent(r.Context(), &req, idemKey)
	if err != nil {
		if respondInvalidCallbackURL(w, err) || respondIdempotencyError(w, err) {
			return
		}
		logger.ErrorfCtx(r.Context(), "merchant create payment: %v", err)
		respondCreatePaymentError(w, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	resp := payment.ToPaymentResponse(res.Payment, h.frontendURL)
	resp.QRImageURL = h.paymentService.QRImageURL(res.Payment)
	respondJSON(w, http.StatusCreated, resp)
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
		logger.ErrorfCtx(r.Context(), "merchant upsert api key: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to update API key")
		return
	}
	status := http.StatusCreated
	if result.Rotated {
		status = http.StatusOK
	}
	activity.AddDetails(r.Context(), apiKeyDetails(result))
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
	activity.AddDetails(r.Context(), map[string]interface{}{"key_id": keyID.String()})
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
		if respondInvalidCallbackURL(w, err) {
			return
		}
		if errors.Is(err, merchant.ErrMerchantNotFound) {
			respondError(w, http.StatusNotFound, "Merchant not found")
			return
		}
		if !errors.Is(err, merchant.ErrMerchantNameRequired) && !errors.Is(err, merchant.ErrBusinessNameRequired) {
			logger.ErrorfCtx(r.Context(), "merchant update business: %v", err)
		}
		respondValidationOrInternal(w, err,
			merchant.ErrMerchantNameRequired,
			merchant.ErrBusinessNameRequired,
		)
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
		logger.ErrorfCtx(r.Context(), "Failed to get webhook secret for merchant %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to load webhook secret")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret})
}

// RegenerateWebhookSecret issues a new signing secret, invalidating the
// previous one immediately. It requires the account password, like API-key
// rotation, because it breaks every callback verifier on the merchant's side.
func (h *MerchantHandler) RegenerateWebhookSecret(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "Password is required to regenerate webhook secret")
		return
	}
	// Regenerating invalidates the old secret at once, so every callback we send
	// stops verifying on the merchant's side: re-authenticate like API-key rotation.
	if err := h.authService.VerifyMerchantPassword(r.Context(), userID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid password")
		return
	}
	secret, err := h.paymentService.RegenerateMerchantWebhookSecret(r.Context(), merchantID)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to regenerate webhook secret for merchant %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to regenerate webhook secret")
		return
	}
	activity.AddDetails(r.Context(), map[string]interface{}{"regenerated": true})
	respondJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret})
}
