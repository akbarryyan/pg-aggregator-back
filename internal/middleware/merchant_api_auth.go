package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

const MerchantIDContextKey contextKey = "merchant_id"
const MerchantAPIKeyIDContextKey contextKey = "merchant_api_key_id"
const MerchantEnvironmentContextKey contextKey = "merchant_environment"

type MerchantAPIAuthMiddleware struct {
	apiKeyService *service.MerchantAPIKeyService
}

func NewMerchantAPIAuthMiddleware(apiKeyService *service.MerchantAPIKeyService) *MerchantAPIAuthMiddleware {
	return &MerchantAPIAuthMiddleware{apiKeyService: apiKeyService}
}

// RequireMerchantAPIKey accepts Authorization: Bearer <key> or X-API-Key: <key>.
func (m *MerchantAPIAuthMiddleware) RequireMerchantAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := extractAPIKey(r)
		if raw == "" {
			writeJSONError(w, http.StatusUnauthorized, "Missing API key. Use Authorization: Bearer <key> or X-API-Key header.")
			return
		}

		auth, err := m.apiKeyService.Authenticate(r.Context(), raw)
		if err != nil {
			switch err {
			case merchant.ErrAPIKeyInvalid:
				writeJSONError(w, http.StatusUnauthorized, "Invalid API key")
			case merchant.ErrAPIKeyRevoked:
				writeJSONError(w, http.StatusUnauthorized, "API key has been revoked")
			case merchant.ErrAPIKeyMerchantInactive:
				writeJSONError(w, http.StatusForbidden, "Merchant is inactive")
			default:
				writeJSONError(w, http.StatusUnauthorized, "Authentication failed")
			}
			return
		}

		ctx := context.WithValue(r.Context(), MerchantIDContextKey, auth.MerchantID)
		ctx = context.WithValue(ctx, MerchantAPIKeyIDContextKey, auth.APIKeyID)
		ctx = context.WithValue(ctx, MerchantEnvironmentContextKey, auth.Environment)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractAPIKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-API-Key")); v != "" {
		return v
	}
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func MerchantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(MerchantIDContextKey).(uuid.UUID)
	return id, ok
}

func MerchantAPIKeyIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(MerchantAPIKeyIDContextKey).(uuid.UUID)
	return id, ok
}

// MerchantEnvironmentFromContext returns sandbox|production when set (API key auth).
func MerchantEnvironmentFromContext(ctx context.Context) (string, bool) {
	env, ok := ctx.Value(MerchantEnvironmentContextKey).(string)
	return env, ok && env != ""
}
