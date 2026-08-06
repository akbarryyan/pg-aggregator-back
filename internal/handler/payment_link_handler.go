package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// PaymentLinkHandler covers both merchant-dashboard CRUD (JWT) and the
// public resolve/pay endpoints for Payment Links — mirrors how
// PaymentHandler already mixes API-key-authed CreatePayment with the public
// GetPaymentByReference in one file, rather than splitting this across
// MerchantHandler/PaymentHandler.
type PaymentLinkHandler struct {
	linkService *service.PaymentLinkService
	frontendURL string
}

func NewPaymentLinkHandler(linkService *service.PaymentLinkService, frontendURL string) *PaymentLinkHandler {
	return &PaymentLinkHandler{
		linkService: linkService,
		frontendURL: strings.TrimRight(frontendURL, "/"),
	}
}

// ---- Merchant dashboard (JWT) --------------------------------------------

func (h *PaymentLinkHandler) ListPaymentLinks(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	limit, offset := parseLimitOffset(r, 20, 0)
	environment := strings.TrimSpace(r.URL.Query().Get("environment"))
	isActive := parseIsActiveQuery(r)

	result, err := h.linkService.ListLinks(r.Context(), merchantID, environment, isActive, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to list payment links: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payment links")
		return
	}

	items := make([]*paymentlink.PaymentLinkResponse, 0, len(result.Items))
	for _, l := range result.Items {
		items = append(items, paymentlink.ToPaymentLinkResponse(l, h.frontendURL))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items":  items,
		"total":  result.Total,
		"limit":  result.Limit,
		"offset": result.Offset,
	})
}

func (h *PaymentLinkHandler) CreatePaymentLink(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req paymentlink.CreatePaymentLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.MerchantID = merchantID

	link, err := h.linkService.CreateLink(r.Context(), &req)
	if err != nil {
		respondPaymentLinkValidationError(w, r, err)
		return
	}

	respondJSON(w, http.StatusCreated, paymentlink.ToPaymentLinkResponse(link, h.frontendURL))
}

func (h *PaymentLinkHandler) GetPaymentLink(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment link ID")
		return
	}

	link, err := h.linkService.GetLink(r.Context(), id, merchantID)
	if err != nil {
		if err == paymentlink.ErrPaymentLinkNotFound {
			respondError(w, http.StatusNotFound, "Payment link not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to get payment link")
		return
	}

	respondJSON(w, http.StatusOK, paymentlink.ToPaymentLinkResponse(link, h.frontendURL))
}

func (h *PaymentLinkHandler) UpdatePaymentLink(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment link ID")
		return
	}

	var req paymentlink.UpdatePaymentLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	link, err := h.linkService.UpdateLink(r.Context(), id, merchantID, &req)
	if err != nil {
		if err == paymentlink.ErrPaymentLinkNotFound {
			respondError(w, http.StatusNotFound, "Payment link not found")
			return
		}
		respondPaymentLinkValidationError(w, r, err)
		return
	}

	respondJSON(w, http.StatusOK, paymentlink.ToPaymentLinkResponse(link, h.frontendURL))
}

func (h *PaymentLinkHandler) SetPaymentLinkActive(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment link ID")
		return
	}

	var req struct {
		IsActive bool `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.linkService.SetActive(r.Context(), id, merchantID, req.IsActive); err != nil {
		if err == paymentlink.ErrPaymentLinkNotFound {
			respondError(w, http.StatusNotFound, "Payment link not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to update payment link status")
		return
	}

	respondJSON(w, http.StatusOK, map[string]bool{"is_active": req.IsActive})
}

func (h *PaymentLinkHandler) ListPaymentLinkPayments(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment link ID")
		return
	}

	limit, offset := parseLimitOffset(r, 20, 0)
	items, total, err := h.linkService.ListLinkPayments(r.Context(), id, merchantID, limit, offset)
	if err != nil {
		if err == paymentlink.ErrPaymentLinkNotFound {
			respondError(w, http.StatusNotFound, "Payment link not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to list payments for this link")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// ---- Public (no auth, rate-limited like GetPaymentByReference) ----------

func (h *PaymentLinkHandler) GetPublicPaymentLink(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(mux.Vars(r)["slug"])
	if slug == "" {
		respondError(w, http.StatusBadRequest, "Slug is required")
		return
	}

	link, err := h.linkService.GetPublicLink(r.Context(), slug)
	if err != nil {
		if err == paymentlink.ErrPaymentLinkNotFound {
			respondError(w, http.StatusNotFound, "Payment link not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to load payment link")
		return
	}

	respondJSON(w, http.StatusOK, paymentlink.ToPublicPaymentLinkResponse(link, time.Now()))
}

func (h *PaymentLinkHandler) InitiatePaymentLinkCheckout(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(mux.Vars(r)["slug"])
	if slug == "" {
		respondError(w, http.StatusBadRequest, "Slug is required")
		return
	}

	var req paymentlink.InitiateCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	p, err := h.linkService.InitiateCheckout(r.Context(), slug, &req)
	if err != nil {
		switch {
		case errors.Is(err, paymentlink.ErrPaymentLinkNotFound):
			respondError(w, http.StatusNotFound, "Payment link not found")
		case errors.Is(err, paymentlink.ErrPaymentLinkInactive), errors.Is(err, paymentlink.ErrPaymentLinkExpired):
			respondError(w, http.StatusGone, err.Error())
		case errors.Is(err, paymentlink.ErrCustomerAmountRequired), errors.Is(err, paymentlink.ErrCustomerAmountOutOfRange):
			respondError(w, http.StatusBadRequest, err.Error())
		default:
			logger.ErrorfCtx(r.Context(), "Failed to initiate payment link checkout: %v", err)
			respondCreatePaymentError(w, err)
		}
		return
	}

	respondJSON(w, http.StatusCreated, payment.ToPaymentResponse(p, h.frontendURL))
}

// parseIsActiveQuery reads ?is_active=true|false. Absent or unrecognized
// means "no filter" (nil) — matches how other list endpoints in this
// package treat optional boolean filters.
func parseIsActiveQuery(r *http.Request) *bool {
	raw := strings.TrimSpace(r.URL.Query().Get("is_active"))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &v
}

// respondPaymentLinkValidationError maps paymentlink domain validation
// errors to 400 Bad Request; anything else is treated as an internal error.
func respondPaymentLinkValidationError(w http.ResponseWriter, r *http.Request, err error) {
	switch err {
	case paymentlink.ErrTitleRequired,
		paymentlink.ErrInvalidAmountType,
		paymentlink.ErrFixedAmountRequired,
		paymentlink.ErrAmountNotAllowedForOpenLink,
		paymentlink.ErrInvalidAmountBounds,
		paymentlink.ErrAmountOutOfPlatformBounds,
		paymentlink.ErrMerchantIDRequired,
		payment.ErrUnsupportedCurrency:
		respondError(w, http.StatusBadRequest, err.Error())
	default:
		logger.ErrorfCtx(r.Context(), "Payment link operation failed: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to save payment link")
	}
}
