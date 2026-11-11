package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	providerPkg "github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type PaymentHandler struct {
	paymentService *service.PaymentService
	frontendURL    string
}

func NewPaymentHandler(paymentService *service.PaymentService, frontendURL string) *PaymentHandler {
	return &PaymentHandler{
		paymentService: paymentService,
		frontendURL:    strings.TrimRight(frontendURL, "/"),
	}
}

func (h *PaymentHandler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	var req payment.CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to decode request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Merchant API key auth: force merchant_id + environment from authenticated key.
	if merchantID, ok := middleware.MerchantIDFromContext(r.Context()); ok {
		req.MerchantID = merchantID
	}
	if env, ok := middleware.MerchantEnvironmentFromContext(r.Context()); ok {
		req.Environment = env
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
		if respondIdempotencyError(w, err) {
			return
		}
		logger.ErrorfCtx(r.Context(), "Failed to create payment: %v", err)
		respondCreatePaymentError(w, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	respondJSON(w, http.StatusCreated, payment.ToPaymentResponse(res.Payment, h.frontendURL))
}

func (h *PaymentHandler) GetPayment(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]

	id, err := uuid.Parse(idStr)
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
		logger.ErrorfCtx(r.Context(), "Failed to get payment: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get payment")
		return
	}

	if merchantID, ok := middleware.MerchantIDFromContext(r.Context()); ok {
		if p.MerchantID != merchantID {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
	}

	response := payment.ToPaymentResponse(p, h.frontendURL)
	respondJSON(w, http.StatusOK, response)
}

// GetPaymentByReference is a public checkout endpoint (limited fields for pay page).
func (h *PaymentHandler) GetPaymentByReference(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(mux.Vars(r)["reference"])
	if ref == "" {
		respondError(w, http.StatusBadRequest, "Reference is required")
		return
	}
	p, err := h.paymentService.GetPaymentByReference(r.Context(), ref)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to load payment")
		return
	}
	// Public-safe subset (still includes QR for checkout UX)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"id":             p.ID,
		"reference":      p.Reference,
		"amount":         p.Amount,
		"fee_amount":     p.FeeAmount,
		"total_amount":   p.TotalAmount(),
		"currency":       p.Currency,
		"status":         p.Status,
		"description":    p.Description,
		"payment_method": p.PaymentMethod,
		// Who the customer is paying (display only; "" when unknown).
		"merchant_name": h.paymentService.MerchantDisplayName(r.Context(), p.MerchantID),
		"environment":   payment.NormalizeEnvironment(p.Environment),
		"qris_data":     p.QRISData,
		"expires_at":    p.ExpiresAt.UTC().Format(time.RFC3339),
		"paid_at": func() interface{} {
			if p.PaidAt == nil {
				return nil
			}
			return p.PaidAt.UTC().Format(time.RFC3339)
		}(),
		"created_at": p.CreatedAt.UTC().Format(time.RFC3339),
	})
}

func (h *PaymentHandler) GetPaymentStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]

	id, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}

	// Ownership check before returning status
	if merchantID, ok := middleware.MerchantIDFromContext(r.Context()); ok {
		p, err := h.paymentService.GetPayment(r.Context(), id)
		if err != nil {
			if err == payment.ErrPaymentNotFound {
				respondError(w, http.StatusNotFound, "Payment not found")
				return
			}
			respondError(w, http.StatusInternalServerError, "Failed to get payment status")
			return
		}
		if p.MerchantID != merchantID {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
	}

	status, err := h.paymentService.GetPaymentStatus(r.Context(), id)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		logger.ErrorfCtx(r.Context(), "Failed to get payment status: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get payment status")
		return
	}

	respondJSON(w, http.StatusOK, status)
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func respondJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Errorf("Failed to encode response: %v", err)
	}
}

func respondError(w http.ResponseWriter, statusCode int, message string) {
	respondJSON(w, statusCode, ErrorResponse{
		Error:   http.StatusText(statusCode),
		Message: message,
	})
}

func respondCreatePaymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, merchant.ErrMerchantInactive):
		respondError(w, http.StatusForbidden, "Merchant account is inactive")
	case errors.Is(err, providerPkg.ErrProviderTimeout):
		// The provider did not answer in time. Checked before the generic
		// "not available" case; the adapter wraps exactly one sentinel.
		respondError(w, http.StatusGatewayTimeout, "Payment provider timed out")
	case errors.Is(err, providerPkg.ErrProviderNotAvailable):
		respondError(w, http.StatusServiceUnavailable, "Payment provider is temporarily unavailable")
	case errors.Is(err, providerPkg.ErrUnsupportedPaymentMethod):
		respondError(w, http.StatusBadRequest, "Unsupported payment method")
	case errors.Is(err, providerPkg.ErrFeeNotSupported):
		respondError(w, http.StatusServiceUnavailable, "Fee calculation is not available for this payment provider")
	case errors.Is(err, providerPkg.ErrProviderAPIError), errors.Is(err, payment.ErrProviderError):
		// The provider answered, but with an error or an invalid response. The
		// raw upstream text stays in the logs; clients get a fixed message.
		respondError(w, http.StatusBadGateway, "Payment provider error")
	default:
		respondError(w, http.StatusInternalServerError, "Failed to create payment")
	}
}
