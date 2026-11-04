package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/gorilla/mux"
)

type WebhookHandler struct {
	paymentService *service.PaymentService
}

func NewWebhookHandler(paymentService *service.PaymentService) *WebhookHandler {
	return &WebhookHandler{
		paymentService: paymentService,
	}
}

func (h *WebhookHandler) HandleProviderWebhook(w http.ResponseWriter, r *http.Request) {
	providerName := mux.Vars(r)["providerName"]
	if providerName == "" {
		respondError(w, http.StatusBadRequest, "Provider name is required")
		return
	}

	rawPayload, err := io.ReadAll(r.Body)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to read webhook payload: %v", err)
		respondError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	defer r.Body.Close()

	signature := r.Header.Get("x-gateway-signature")

	logger.InfofCtx(r.Context(), "Received webhook from provider %s, signature present: %v", providerName, signature != "")

	if err := h.paymentService.ProcessWebhook(r.Context(), providerName, rawPayload, signature); err != nil {
		switch {
		case errors.Is(err, payment.ErrDuplicateWebhook),
			errors.Is(err, payment.ErrPaymentAlreadyTerminal),
			errors.Is(err, payment.ErrWebhookIgnored),
			errors.Is(err, payment.ErrAmountMismatch),
			errors.Is(err, payment.ErrWebhookAmountMismatch):
			// Acknowledge so the provider doesn't keep retrying: these are
			// duplicates, non-settlements, or an amount mismatch (provider-side
			// ones are flagged for a human; webhook-only ones are left to
			// reconcile).
			logger.InfofCtx(r.Context(), "Webhook acknowledged without status change: %v", err)
			respondJSON(w, http.StatusOK, map[string]string{
				"status":  "ignored",
				"message": "Webhook acknowledged without a status change",
			})
		case errors.Is(err, payment.ErrProviderVerificationFailed):
			// The provider couldn't confirm the settlement yet: ask it to
			// retry. The reconcile job is the fallback if it never does.
			logger.WarnfCtx(r.Context(), "Webhook not confirmed by provider: %v", err)
			respondError(w, http.StatusBadGateway, "Payment not yet confirmed by provider; retry later")
		default:
			logger.ErrorfCtx(r.Context(), "Failed to process webhook: %v", err)
			respondError(w, http.StatusBadRequest, "Webhook processing failed")
		}
		return
	}

	logger.InfoCtx(r.Context(), "Webhook processed successfully")
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Webhook processed successfully",
	})
}
