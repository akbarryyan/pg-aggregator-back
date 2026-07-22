package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

type contextKey string

const AdminIDContextKey contextKey = "admin_id"
const MerchantUserIDContextKey contextKey = "merchant_user_id"

type AuthMiddleware struct {
	authService *service.AuthService
}

func NewAuthMiddleware(authService *service.AuthService) *AuthMiddleware {
	return &AuthMiddleware{authService: authService}
}

// RequireMerchant validates merchant dashboard JWT and sets merchant_id + merchant_user_id.
func (m *AuthMiddleware) RequireMerchant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeJSONError(w, http.StatusUnauthorized, "Missing authorization header")
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			writeJSONError(w, http.StatusUnauthorized, "Invalid authorization header")
			return
		}
		claims, err := m.authService.ParseMerchantToken(parts[1])
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), MerchantIDContextKey, claims.MerchantID)
		ctx = context.WithValue(ctx, MerchantUserIDContextKey, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func MerchantUserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(MerchantUserIDContextKey).(uuid.UUID)
	return id, ok
}

func (m *AuthMiddleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeJSONError(w, http.StatusUnauthorized, "Missing authorization header")
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			writeJSONError(w, http.StatusUnauthorized, "Invalid authorization header")
			return
		}

		claims, err := m.authService.ParseAdminToken(parts[1])
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), AdminIDContextKey, claims.AdminID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func AdminIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(AdminIDContextKey).(uuid.UUID)
	return id, ok
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   http.StatusText(status),
		"message": message,
	})
}
