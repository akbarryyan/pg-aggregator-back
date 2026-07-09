package handler

import (
	"encoding/json"
	"net/http"

	domainProvider "github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type ProviderRoutingHandler struct {
	service *service.ProviderRoutingService
}

func NewProviderRoutingHandler(service *service.ProviderRoutingService) *ProviderRoutingHandler {
	return &ProviderRoutingHandler{service: service}
}

func (h *ProviderRoutingHandler) ListMerchantProviderConfigs(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchantID(w, r)
	if !ok {
		return
	}

	configs, err := h.service.ListMerchantProviderConfigs(r.Context(), merchantID)
	if err != nil {
		logger.Errorf("Failed to list merchant provider configs: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list merchant provider configs")
		return
	}

	respondJSON(w, http.StatusOK, configs)
}

func (h *ProviderRoutingHandler) UpsertMerchantProviderConfig(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchantID(w, r)
	if !ok {
		return
	}

	var req domainProvider.UpsertMerchantProviderConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpsertMerchantProviderConfig(r.Context(), merchantID, &req); err != nil {
		logger.Errorf("Failed to upsert merchant provider config: %v", err)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *ProviderRoutingHandler) DeleteMerchantProviderConfig(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := parseMerchantID(w, r)
	if !ok {
		return
	}

	paymentMethod := r.URL.Query().Get("payment_method")
	providerName := r.URL.Query().Get("provider_name")
	if paymentMethod == "" || providerName == "" {
		respondError(w, http.StatusBadRequest, "payment_method and provider_name are required")
		return
	}

	if err := h.service.DeleteMerchantProviderConfig(r.Context(), merchantID, paymentMethod, providerName); err != nil {
		logger.Errorf("Failed to delete merchant provider config: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete merchant provider config")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *ProviderRoutingHandler) ListProviderHealths(w http.ResponseWriter, r *http.Request) {
	healths, err := h.service.ListProviderHealths(r.Context())
	if err != nil {
		logger.Errorf("Failed to list provider healths: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list provider healths")
		return
	}

	respondJSON(w, http.StatusOK, healths)
}

func (h *ProviderRoutingHandler) UpdateProviderHealth(w http.ResponseWriter, r *http.Request) {
	providerName := mux.Vars(r)["providerName"]
	if providerName == "" {
		respondError(w, http.StatusBadRequest, "Provider name is required")
		return
	}

	var req domainProvider.UpdateProviderHealthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpdateProviderHealth(r.Context(), providerName, &req); err != nil {
		logger.Errorf("Failed to update provider health: %v", err)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func parseMerchantID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	merchantIDStr := mux.Vars(r)["merchantID"]
	merchantID, err := uuid.Parse(merchantIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid merchant ID")
		return uuid.Nil, false
	}
	return merchantID, true
}
