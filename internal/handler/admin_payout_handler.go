package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type adminBalanceAPI interface {
	Balances(ctx context.Context, negativeOnly bool, limit, offset int) ([]*ledger.MerchantBalance, int64, error)
	Ledger(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Entry, int64, error)
}

type adminPayoutAPI interface {
	ListAdmin(ctx context.Context, status string, limit, offset int) ([]*ledger.Payout, int64, error)
	MarkPaid(ctx context.Context, id uuid.UUID, reference string, adminID *uuid.UUID) (*ledger.Payout, error)
	Reject(ctx context.Context, id uuid.UUID, reason string, adminID *uuid.UUID) (*ledger.Payout, error)
	Stats(ctx context.Context) (*service.PayoutStats, error)
}

// WithPayoutServices wires the balance and payout services for the admin
// endpoints.
func (h *AdminHandler) WithPayoutServices(balance adminBalanceAPI, payouts adminPayoutAPI) *AdminHandler {
	h.balanceService = balance
	h.payoutService = payouts
	return h
}

// ListBalances: every merchant's balance, highest first; ?negative=true keeps
// only merchants below zero.
func (h *AdminHandler) ListBalances(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, 20, 0)
	negative := r.URL.Query().Get("negative") == "true"
	items, total, err := h.balanceService.Balances(r.Context(), negative, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "admin list balances: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list balances")
		return
	}
	respondJSON(w, http.StatusOK, pagedResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *AdminHandler) ListMerchantLedger(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	items, total, err := h.balanceService.Ledger(r.Context(), id, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "admin merchant %s ledger: %v", id, err)
		respondError(w, http.StatusInternalServerError, "Failed to load ledger")
		return
	}
	respondJSON(w, http.StatusOK, pagedResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *AdminHandler) ListPayouts(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !ledger.IsValidPayoutStatus(status) {
		respondError(w, http.StatusBadRequest, "Invalid status")
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	items, total, err := h.payoutService.ListAdmin(r.Context(), status, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "admin list payouts: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list payouts")
		return
	}
	respondJSON(w, http.StatusOK, pagedResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *AdminHandler) PayPayout(w http.ResponseWriter, r *http.Request) {
	h.processPayout(w, r, "reference", func(ctx context.Context, id uuid.UUID, text string, adminID *uuid.UUID) (*ledger.Payout, error) {
		return h.payoutService.MarkPaid(ctx, id, text, adminID)
	}, "marked paid")
}

func (h *AdminHandler) RejectPayout(w http.ResponseWriter, r *http.Request) {
	h.processPayout(w, r, "reason", func(ctx context.Context, id uuid.UUID, text string, adminID *uuid.UUID) (*ledger.Payout, error) {
		return h.payoutService.Reject(ctx, id, text, adminID)
	}, "rejected")
}

// processPayout is the shared body of pay / reject: both take one required
// text field ("reference" or "reason") and log the admin, payout and amount.
func (h *AdminHandler) processPayout(
	w http.ResponseWriter, r *http.Request, field string,
	do func(context.Context, uuid.UUID, string, *uuid.UUID) (*ledger.Payout, error), verb string,
) {
	adminID, ok := middleware.AdminIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payout ID")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	text, _ := body[field].(string)

	p, err := do(r.Context(), id, text, &adminID)
	switch {
	case err == nil:
		logger.InfofCtx(r.Context(), "admin %s %s payout %s merchant %s amount %d", adminID, verb, p.ID, p.MerchantID, p.Amount)
		activity.AddDetails(r.Context(), map[string]interface{}{"amount": p.Amount, field: text})
		respondJSON(w, http.StatusOK, p)
	case errors.Is(err, ledger.ErrReasonRequired):
		respondError(w, http.StatusBadRequest, "A "+field+" is required")
	case errors.Is(err, ledger.ErrTextTooLong):
		respondError(w, http.StatusBadRequest, "The "+field+" is too long")
	case errors.Is(err, ledger.ErrPayoutNotFound):
		respondError(w, http.StatusNotFound, "Payout not found")
	case errors.Is(err, ledger.ErrPayoutAlreadyProcessed):
		respondError(w, http.StatusConflict, "Payout has already been processed")
	default:
		logger.ErrorfCtx(r.Context(), "admin %s payout %s (%s): %v", adminID, id, verb, err)
		respondError(w, http.StatusInternalServerError, "Failed to process payout")
	}
}

// GetPayoutStats serves the summary cards and charts of the admin Payouts page.
func (h *AdminHandler) GetPayoutStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.payoutService.Stats(r.Context())
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to load payout stats: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load payout stats")
		return
	}
	respondJSON(w, http.StatusOK, stats)
}
