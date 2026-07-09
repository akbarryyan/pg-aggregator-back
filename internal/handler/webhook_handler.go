package handler

import (
	"io"
	"net/http"

	"pg-aggregator/internal/service"
	"pg-aggregator/pkg/logger"
)

type WebhookHandler struct {
	paymentService *service.PaymentService
}

func NewWebhookHandler(paymentService *service.PaymentService) *WebhookHandler {
	return &WebhookHandler{
		paymentService: paymentService,
	}
}

func (h *WebhookHandler) HandleKlikQrisWebhook(w http.ResponseWriter, r *http.Request) {
	rawPayload, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Errorf("Failed to read webhook payload: %v", err)
		respondError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	defer r.Body.Close()

	signature := r.Header.Get("X-Signature")
	if signature == "" {
		signature = r.Header.Get("X-KlikQris-Signature")
	}

	logger.Infof("Received KlikQris webhook, signature present: %v", signature != "")

	if err := h.paymentService.ProcessWebhook(r.Context(), "klikqris", rawPayload, signature); err != nil {
		logger.Errorf("Failed to process webhook: %v", err)
		
		respondError(w, http.StatusBadRequest, "Webhook processing failed")
		return
	}

	logger.Info("Webhook processed successfully")
	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Webhook processed successfully",
	})
}
