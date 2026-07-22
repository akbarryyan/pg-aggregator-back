package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/admin"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
)

type AuthHandler struct {
	authService *service.AuthService
}

func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{authService: authService}
}

func (h *AuthHandler) LoginAdmin(w http.ResponseWriter, r *http.Request) {
	var req admin.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode admin login request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.Email = strings.TrimSpace(req.Email)

	resp, err := h.authService.LoginAdmin(r.Context(), &req)
	if err != nil {
		switch err {
		case admin.ErrEmailRequired, admin.ErrPasswordRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		case admin.ErrInvalidCredentials:
			respondError(w, http.StatusUnauthorized, "Invalid email or password")
		case admin.ErrAdminInactive:
			respondError(w, http.StatusForbidden, "Admin account is inactive")
		default:
			logger.Errorf("Admin login failed: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to sign in")
		}
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (h *AuthHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateAdmin(w, r)
	if !ok {
		return
	}

	a, err := h.authService.GetAdminByID(r.Context(), claims.AdminID)
	if err != nil {
		if err == admin.ErrAdminNotFound {
			respondError(w, http.StatusUnauthorized, "Admin not found")
			return
		}
		logger.Errorf("Failed to get admin profile: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get profile")
		return
	}

	if !a.IsActive {
		respondError(w, http.StatusForbidden, "Admin account is inactive")
		return
	}

	respondJSON(w, http.StatusOK, admin.ToAdminResponse(a))
}

func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateAdmin(w, r)
	if !ok {
		return
	}

	var req admin.UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode update profile request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)

	updated, err := h.authService.UpdateProfile(r.Context(), claims.AdminID, &req)
	if err != nil {
		switch err {
		case admin.ErrNameRequired, admin.ErrEmailRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		case admin.ErrEmailAlreadyUsed:
			respondError(w, http.StatusConflict, "Email is already in use")
		case admin.ErrAdminInactive:
			respondError(w, http.StatusForbidden, "Admin account is inactive")
		case admin.ErrAdminNotFound:
			respondError(w, http.StatusUnauthorized, "Admin not found")
		default:
			logger.Errorf("Failed to update admin profile: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to update profile")
		}
		return
	}

	respondJSON(w, http.StatusOK, admin.ToAdminResponse(updated))
}

func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateAdmin(w, r)
	if !ok {
		return
	}

	var req admin.ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("Failed to decode change password request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.authService.ChangePassword(r.Context(), claims.AdminID, &req); err != nil {
		switch err {
		case admin.ErrCurrentPasswordRequired,
			admin.ErrNewPasswordRequired,
			admin.ErrNewPasswordTooShort,
			admin.ErrNewPasswordSameAsCurrent:
			respondError(w, http.StatusBadRequest, err.Error())
		case admin.ErrCurrentPasswordInvalid:
			respondError(w, http.StatusUnauthorized, "Current password is incorrect")
		case admin.ErrAdminInactive:
			respondError(w, http.StatusForbidden, "Admin account is inactive")
		case admin.ErrAdminNotFound:
			respondError(w, http.StatusUnauthorized, "Admin not found")
		default:
			logger.Errorf("Failed to change admin password: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to change password")
		}
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Password changed successfully",
	})
}

// --- Merchant dashboard auth (used by /login → /dashboard) ---

func (h *AuthHandler) LoginMerchant(w http.ResponseWriter, r *http.Request) {
	var req merchant.UserLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.Email = strings.TrimSpace(req.Email)

	resp, err := h.authService.LoginMerchant(r.Context(), &req)
	if err != nil {
		switch err {
		case merchant.ErrMerchantEmailRequired, merchant.ErrMerchantPasswordRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		case merchant.ErrMerchantInvalidCredentials:
			respondError(w, http.StatusUnauthorized, "Invalid email or password")
		case merchant.ErrMerchantUserInactive:
			respondError(w, http.StatusForbidden, "Merchant account is inactive")
		default:
			logger.Errorf("Merchant login failed: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to sign in")
		}
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

func (h *AuthHandler) GetMerchantMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateMerchant(w, r)
	if !ok {
		return
	}
	profile, err := h.authService.GetMerchantUserProfile(r.Context(), claims.UserID)
	if err != nil {
		if err == merchant.ErrMerchantUserNotFound {
			respondError(w, http.StatusUnauthorized, "User not found")
			return
		}
		logger.Errorf("Failed to get merchant profile: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to get profile")
		return
	}
	respondJSON(w, http.StatusOK, profile)
}

func (h *AuthHandler) UpdateMerchantProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateMerchant(w, r)
	if !ok {
		return
	}
	var req merchant.UserUpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	profile, err := h.authService.UpdateMerchantUserProfile(r.Context(), claims.UserID, &req)
	if err != nil {
		switch err {
		case merchant.ErrMerchantNameRequired, merchant.ErrMerchantEmailRequired:
			respondError(w, http.StatusBadRequest, err.Error())
		case merchant.ErrMerchantUserNotFound:
			respondError(w, http.StatusUnauthorized, "User not found")
		default:
			logger.Errorf("Failed to update merchant profile: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to update profile")
		}
		return
	}
	respondJSON(w, http.StatusOK, profile)
}

func (h *AuthHandler) ChangeMerchantPassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticateMerchant(w, r)
	if !ok {
		return
	}
	var req merchant.UserChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := h.authService.ChangeMerchantPassword(r.Context(), claims.UserID, &req); err != nil {
		switch err {
		case merchant.ErrMerchantCurrentPasswordRequired,
			merchant.ErrMerchantNewPasswordRequired,
			merchant.ErrMerchantNewPasswordTooShort,
			merchant.ErrMerchantNewPasswordSame:
			respondError(w, http.StatusBadRequest, err.Error())
		case merchant.ErrMerchantCurrentPasswordInvalid:
			respondError(w, http.StatusUnauthorized, "Current password is incorrect")
		default:
			logger.Errorf("Failed to change merchant password: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to change password")
		}
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "Password changed successfully"})
}

func (h *AuthHandler) authenticateMerchant(w http.ResponseWriter, r *http.Request) (*service.MerchantClaims, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		respondError(w, http.StatusUnauthorized, "Missing authorization header")
		return nil, false
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		respondError(w, http.StatusUnauthorized, "Invalid authorization header")
		return nil, false
	}
	claims, err := h.authService.ParseMerchantToken(parts[1])
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid or expired token")
		return nil, false
	}
	return claims, true
}

func (h *AuthHandler) authenticateAdmin(w http.ResponseWriter, r *http.Request) (*service.AdminClaims, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		respondError(w, http.StatusUnauthorized, "Missing authorization header")
		return nil, false
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		respondError(w, http.StatusUnauthorized, "Invalid authorization header")
		return nil, false
	}

	claims, err := h.authService.ParseAdminToken(parts[1])
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Invalid or expired token")
		return nil, false
	}

	return claims, true
}
