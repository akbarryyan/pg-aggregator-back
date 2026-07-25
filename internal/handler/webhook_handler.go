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
		logger.Errorf("Failed to read webhook payload: %v", err)
		respondError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	defer r.Body.Close()

	signature := r.Header.Get("x-gateway-signature")

	logger.Infof("Received webhook from provider %s, signature present: %v", providerName, signature != "")

	if err := h.paymentService.ProcessWebhook(r.Context(), providerName, rawPayload, signature); err != nil {
		logger.Errorf("Failed to process webhook: %v", err)
		if errors.Is(err, payment.ErrDuplicateWebhook) || errors.Is(err, payment.ErrPaymentAlreadyTerminal) {
			respondJSON(w, http.StatusOK, map[string]string{
				"status":  "ignored",
				"message": "Webhook already processed",
			})
			return
		}

		respondError(w, http.StatusBadRequest, "Webhook processing failed")
		return
	}

	logger.Info("Webhook processed successfully")
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Webhook processed successfully",
	})
}
