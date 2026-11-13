package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type adminRefundAPI interface {
	Refund(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*service.RefundResult, error)
	ListRefunds(ctx context.Context, paymentID uuid.UUID) (*ledger.RefundSummary, error)
	Adjust(ctx context.Context, merchantID uuid.UUID, amount int64, reason string, adminID *uuid.UUID) (*ledger.Entry, error)
}

// WithRefundService wires admin refunds and balance adjustments.
func (h *AdminHandler) WithRefundService(s adminRefundAPI) *AdminHandler {
	h.refundService = s
	return h
}

type amountReasonRequest struct {
	Amount int64  `json:"amount"`
	Reason string `json:"reason"`
}

type refundResponse struct {
	Refund              *ledger.Refund `json:"refund"`
	PaymentStatus       string         `json:"payment_status"`
	RemainingRefundable int64          `json:"remaining_refundable"`
}

// RefundPayment records a partial or full refund of a paid production payment.
func (h *AdminHandler) RefundPayment(w http.ResponseWriter, r *http.Request) {
	adminID, ok := middleware.AdminIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}
	var req amountReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	res, err := h.refundService.Refund(r.Context(), id, req.Amount, req.Reason, &adminID)
	switch {
	case err == nil:
		logger.InfofCtx(r.Context(), "admin %s refunded payment %s merchant %s amount %d full=%t",
			adminID, id, res.Refund.MerchantID, res.Refund.Amount, res.Full)
		activity.AddDetails(r.Context(), map[string]interface{}{"amount": res.Refund.Amount, "reason": req.Reason})
		respondJSON(w, http.StatusCreated, refundResponse{
			Refund: res.Refund, PaymentStatus: res.PaymentStatus, RemainingRefundable: res.RemainingRefundable,
		})
	case errors.Is(err, ledger.ErrInvalidAmount):
		respondError(w, http.StatusBadRequest, "Amount must be greater than 0")
	case errors.Is(err, ledger.ErrReasonRequired):
		respondError(w, http.StatusBadRequest, "A reason is required")
	case errors.Is(err, ledger.ErrTextTooLong):
		respondError(w, http.StatusBadRequest, "The reason is too long")
	case errors.Is(err, payment.ErrPaymentNotFound):
		respondError(w, http.StatusNotFound, "Payment not found")
	case errors.Is(err, ledger.ErrPaymentNotRefundable):
		respondError(w, http.StatusConflict, "Payment cannot be refunded")
	case errors.Is(err, ledger.ErrRefundExceedsNet):
		respondError(w, http.StatusUnprocessableEntity, "Refund exceeds the refundable amount")
	default:
		logger.ErrorfCtx(r.Context(), "admin %s refund payment %s: %v", adminID, id, err)
		respondError(w, http.StatusInternalServerError, "Failed to record refund")
	}
}

// AdjustMerchantBalance records a manual credit (positive) or debit
// (negative) on a merchant's ledger.
func (h *AdminHandler) AdjustMerchantBalance(w http.ResponseWriter, r *http.Request) {
	adminID, ok := middleware.AdminIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	var req amountReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	entry, err := h.refundService.Adjust(r.Context(), id, req.Amount, req.Reason, &adminID)
	switch {
	case err == nil:
		logger.InfofCtx(r.Context(), "admin %s adjusted merchant %s balance by %d", adminID, id, entry.Amount)
		activity.AddDetails(r.Context(), map[string]interface{}{"amount": entry.Amount, "reason": req.Reason})
		respondJSON(w, http.StatusCreated, entry)
	case errors.Is(err, ledger.ErrInvalidAdjustment):
		respondError(w, http.StatusBadRequest, "Amount must be non-zero and within the allowed range")
	case errors.Is(err, ledger.ErrReasonRequired):
		respondError(w, http.StatusBadRequest, "A reason is required")
	case errors.Is(err, ledger.ErrTextTooLong):
		respondError(w, http.StatusBadRequest, "The reason is too long")
	case errors.Is(err, merchant.ErrMerchantNotFound):
		respondError(w, http.StatusNotFound, "Merchant not found")
	default:
		logger.ErrorfCtx(r.Context(), "admin %s adjust merchant %s: %v", adminID, id, err)
		respondError(w, http.StatusInternalServerError, "Failed to record adjustment")
	}
}

// ListPaymentRefunds returns a payment's refunds and refundable figures.
func (h *AdminHandler) ListPaymentRefunds(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}
	res, err := h.refundService.ListRefunds(r.Context(), id)
	switch {
	case err == nil:
		respondJSON(w, http.StatusOK, res)
	case errors.Is(err, payment.ErrPaymentNotFound):
		respondError(w, http.StatusNotFound, "Payment not found")
	default:
		logger.ErrorfCtx(r.Context(), "admin list refunds payment %s: %v", id, err)
		respondError(w, http.StatusInternalServerError, "Failed to load refunds")
	}
}
