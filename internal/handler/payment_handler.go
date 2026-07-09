package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"pg-aggregator/internal/domain/payment"
	"pg-aggregator/internal/service"
	"pg-aggregator/pkg/logger"
)

type PaymentHandler struct {
	paymentService *service.PaymentService
}

func NewPaymentHandler(paymentService *service.PaymentService) *PaymentHandler {
	return &PaymentHandler{
		paymentService: paymentService,
	}
}

func (h *PaymentHandler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	var req payment.CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	p, err := h.paymentService.CreatePayment(r.Context(), &req)
	if err != nil {
		logger.Errorf("Failed to create payment: %v", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response := payment.ToPaymentResponse(p, "http://localhost:3000")
	respondJSON(w, http.StatusCreated, response)
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
		logger.Errorf("Failed to get payment: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get payment")
		return
	}

	response := payment.ToPaymentResponse(p, "http://localhost:3000")
	respondJSON(w, http.StatusOK, response)
}

func (h *PaymentHandler) GetPaymentStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]

	id, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payment ID")
		return
	}

	status, err := h.paymentService.GetPaymentStatus(r.Context(), id)
	if err != nil {
		if err == payment.ErrPaymentNotFound {
			respondError(w, http.StatusNotFound, "Payment not found")
			return
		}
		logger.Errorf("Failed to get payment status: %v", err)
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
