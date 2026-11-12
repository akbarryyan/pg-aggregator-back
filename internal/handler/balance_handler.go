package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// merchantBalanceAPI and merchantPayoutAPI are what the merchant balance
// endpoints need; *service.BalanceService / *service.PayoutService satisfy them.
type merchantBalanceAPI interface {
	Overview(ctx context.Context, merchantID uuid.UUID) (*service.BalanceOverview, error)
	Ledger(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Entry, int64, error)
	Daily(ctx context.Context, merchantID uuid.UUID, days int) (*service.BalanceDaily, error)
}

type merchantPayoutAPI interface {
	SetAccount(ctx context.Context, merchantID uuid.UUID, acc merchant.PayoutAccount) (*merchant.PayoutAccount, error)
	Request(ctx context.Context, merchantID uuid.UUID, amount int64, requestedBy *uuid.UUID) (*ledger.Payout, error)
	ListByMerchant(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]*ledger.Payout, int64, error)
}

// WithBalanceServices wires the balance and payout services for the merchant
// dashboard endpoints.
func (h *MerchantHandler) WithBalanceServices(balance merchantBalanceAPI, payouts merchantPayoutAPI) *MerchantHandler {
	h.balanceService = balance
	h.payoutService = payouts
	return h
}

type pagedResponse struct {
	Items  any   `json:"items"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// GetBalance returns the merchant's balance card. The merchant is always the
// authenticated one; nothing in the request selects another.
func (h *MerchantHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	ov, err := h.balanceService.Overview(r.Context(), merchantID)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "merchant balance %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to load balance")
		return
	}
	respondJSON(w, http.StatusOK, ov)
}

// GetBalanceDaily serves the credited/debited chart of the Balance page. The
// merchant is always the authenticated one; `days` (default 14) is clamped to
// 1..90 by the service and an unparsable value falls back to the default.
func (h *MerchantHandler) GetBalanceDaily(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	days, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("days")))
	res, err := h.balanceService.Daily(r.Context(), merchantID, days)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "merchant balance daily %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to load balance chart")
		return
	}
	respondJSON(w, http.StatusOK, res)
}

func (h *MerchantHandler) ListLedger(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	items, total, err := h.balanceService.Ledger(r.Context(), merchantID, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "merchant ledger %s: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to load ledger")
		return
	}
	respondJSON(w, http.StatusOK, pagedResponse{Items: toMerchantLedger(items), Total: total, Limit: limit, Offset: offset})
}

// UpdatePayoutAccount changes where payouts are sent. Like
// RegenerateWebhookSecret it re-authenticates with the account password; a
// wrong password is a plain 401 that the client must not treat as a session
// expiry.
func (h *MerchantHandler) UpdatePayoutAccount(w http.ResponseWriter, r *http.Request) {
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
		Password      string `json:"password"`
		BankName      string `json:"bank_name"`
		AccountNumber string `json:"account_number"`
		AccountHolder string `json:"account_holder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		respondError(w, http.StatusBadRequest, "Password is required to change payout account")
		return
	}
	// Payouts go to this account, so changing it re-authenticates like API-key rotation.
	if err := h.authService.VerifyMerchantPassword(r.Context(), userID, req.Password); err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid password")
		return
	}
	acc, err := h.payoutService.SetAccount(r.Context(), merchantID, merchant.PayoutAccount{
		BankName: req.BankName, AccountNumber: req.AccountNumber, AccountHolder: req.AccountHolder,
	})
	switch {
	case err == nil:
		logger.InfofCtx(r.Context(), "merchant %s (user %s) changed payout account", merchantID, userID)
		respondJSON(w, http.StatusOK, acc)
	case errors.Is(err, merchant.ErrPayoutBankInvalid),
		errors.Is(err, merchant.ErrPayoutAccountNumberInvalid),
		errors.Is(err, merchant.ErrPayoutHolderInvalid):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, merchant.ErrMerchantNotFound):
		respondError(w, http.StatusNotFound, "Merchant not found")
	default:
		logger.ErrorfCtx(r.Context(), "merchant %s set payout account: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to update payout account")
	}
}

// RequestPayout creates a payout for the authenticated merchant. There is no
// Idempotency-Key: a double click is refused by the "one open payout per
// merchant" rule (409), so no extra idempotency table is needed.
func (h *MerchantHandler) RequestPayout(w http.ResponseWriter, r *http.Request) {
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
		Amount int64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	p, err := h.payoutService.Request(r.Context(), merchantID, req.Amount, &userID)
	switch {
	case err == nil:
		logger.InfofCtx(r.Context(), "merchant %s (user %s) requested payout %s amount %d", merchantID, userID, p.ID, p.Amount)
		respondJSON(w, http.StatusCreated, toMerchantPayout(p))
	case errors.Is(err, merchant.ErrMerchantNotFound):
		respondError(w, http.StatusNotFound, "Merchant not found")
	case errors.Is(err, ledger.ErrBelowMinimumPayout):
		respondError(w, http.StatusBadRequest, "Amount is below the minimum payout")
	case errors.Is(err, ledger.ErrOpenPayoutExists):
		respondError(w, http.StatusConflict, "A payout request is already waiting to be processed")
	case errors.Is(err, ledger.ErrInsufficientBalance):
		respondError(w, http.StatusUnprocessableEntity, "Insufficient balance")
	case errors.Is(err, ledger.ErrPayoutAccountMissing):
		respondError(w, http.StatusUnprocessableEntity, "Set a payout account first")
	default:
		logger.ErrorfCtx(r.Context(), "merchant %s request payout: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to request payout")
	}
}

func (h *MerchantHandler) ListPayouts(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := middleware.MerchantIDFromContext(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	items, total, err := h.payoutService.ListByMerchant(r.Context(), merchantID, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "merchant %s list payouts: %v", merchantID, err)
		respondError(w, http.StatusInternalServerError, "Failed to list payouts")
		return
	}
	respondJSON(w, http.StatusOK, pagedResponse{Items: toMerchantPayouts(items), Total: total, Limit: limit, Offset: offset})
}

// Merchant-facing representations. They deliberately leave out every admin
// or user identifier (created_by, requested_by, processed_by): merchants must
// not learn admin UUIDs. Admin endpoints keep returning the domain structs.

type merchantLedgerEntry struct {
	ID        uuid.UUID  `json:"id"`
	EntryType string     `json:"entry_type"`
	Amount    int64      `json:"amount"`
	PaymentID *uuid.UUID `json:"payment_id,omitempty"`
	PayoutID  *uuid.UUID `json:"payout_id,omitempty"`
	RefundID  *uuid.UUID `json:"refund_id,omitempty"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
}

func toMerchantLedger(in []*ledger.Entry) []merchantLedgerEntry {
	out := make([]merchantLedgerEntry, 0, len(in))
	for _, e := range in {
		out = append(out, merchantLedgerEntry{ID: e.ID, EntryType: e.EntryType, Amount: e.Amount,
			PaymentID: e.PaymentID, PayoutID: e.PayoutID, RefundID: e.RefundID, Note: e.Note, CreatedAt: e.CreatedAt})
	}
	return out
}

type merchantPayoutView struct {
	ID            uuid.UUID  `json:"id"`
	Amount        int64      `json:"amount"`
	Status        string     `json:"status"`
	BankName      string     `json:"bank_name"`
	AccountNumber string     `json:"account_number"`
	AccountHolder string     `json:"account_holder"`
	RequestedAt   time.Time  `json:"requested_at"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty"`
	Reference     *string    `json:"reference,omitempty"`
	RejectReason  *string    `json:"reject_reason,omitempty"`
}

func toMerchantPayout(p *ledger.Payout) merchantPayoutView {
	return merchantPayoutView{ID: p.ID, Amount: p.Amount, Status: p.Status, BankName: p.BankName,
		AccountNumber: p.AccountNumber, AccountHolder: p.AccountHolder, RequestedAt: p.RequestedAt,
		ProcessedAt: p.ProcessedAt, Reference: p.Reference, RejectReason: p.RejectReason}
}

func toMerchantPayouts(in []*ledger.Payout) []merchantPayoutView {
	out := make([]merchantPayoutView, 0, len(in))
	for _, p := range in {
		out = append(out, toMerchantPayout(p))
	}
	return out
}
