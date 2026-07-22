package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

type MerchantAPIKeyService struct {
	apiKeyRepo   *repository.MerchantAPIKeyRepository
	merchantRepo *repository.MerchantRepository
}

func NewMerchantAPIKeyService(
	apiKeyRepo *repository.MerchantAPIKeyRepository,
	merchantRepo *repository.MerchantRepository,
) *MerchantAPIKeyService {
	return &MerchantAPIKeyService{
		apiKeyRepo:   apiKeyRepo,
		merchantRepo: merchantRepo,
	}
}

// AuthenticatedMerchant is set on request context after API key validation.
type AuthenticatedMerchant struct {
	MerchantID  uuid.UUID
	APIKeyID    uuid.UUID
	KeyPrefix   string
	Environment string // sandbox | production (from key name)
}

func (s *MerchantAPIKeyService) List(ctx context.Context, merchantID uuid.UUID) ([]*merchant.APIKeyPublic, error) {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		return nil, err
	}
	keys, err := s.apiKeyRepo.ListByMerchant(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	out := make([]*merchant.APIKeyPublic, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.ToPublic())
	}
	return out, nil
}

// Upsert creates or rotates the single API key for sandbox|production.
// Callers must verify admin password before invoking this method.
func (s *MerchantAPIKeyService) Upsert(
	ctx context.Context,
	merchantID uuid.UUID,
	req *merchant.UpsertAPIKeyRequest,
) (*merchant.UpsertAPIKeyResponse, error) {
	if req == nil || strings.TrimSpace(req.Password) == "" {
		return nil, merchant.ErrAPIKeyPasswordRequired
	}

	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	if !m.IsActive {
		return nil, merchant.ErrAPIKeyMerchantInactive
	}

	env := normalizeAPIKeyEnvironment(req)
	deleted, err := s.apiKeyRepo.DeleteByMerchantAndName(ctx, merchantID, env)
	if err != nil {
		return nil, err
	}
	rotated := deleted > 0

	secret, err := generateAPIKeySecret(env)
	if err != nil {
		return nil, err
	}
	prefix := secret
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}

	key := &merchant.APIKey{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Name:       env,
		KeyPrefix:  prefix,
		KeyHash:    hashAPIKey(secret),
		IsActive:   true,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := s.apiKeyRepo.Create(ctx, key); err != nil {
		return nil, err
	}

	hint := "Copy this secret now. It will not be shown again."
	if rotated {
		hint = "Previous key for this environment was replaced. Copy the new secret now — it will not be shown again."
	}

	return &merchant.UpsertAPIKeyResponse{
		Key:         key.ToPublic(),
		Secret:      secret,
		Hint:        hint,
		Rotated:     rotated,
		Environment: env,
	}, nil
}

func (s *MerchantAPIKeyService) Delete(ctx context.Context, merchantID, keyID uuid.UUID) error {
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		return err
	}
	return s.apiKeyRepo.Delete(ctx, keyID, merchantID)
}

// Authenticate validates a raw API key secret and returns merchant context.
func (s *MerchantAPIKeyService) Authenticate(ctx context.Context, rawKey string) (*AuthenticatedMerchant, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return nil, merchant.ErrAPIKeyInvalid
	}

	key, err := s.apiKeyRepo.GetByHash(ctx, hashAPIKey(rawKey))
	if err != nil {
		if err == merchant.ErrAPIKeyNotFound {
			return nil, merchant.ErrAPIKeyInvalid
		}
		return nil, err
	}
	if !key.IsActive || key.RevokedAt != nil {
		return nil, merchant.ErrAPIKeyRevoked
	}

	m, err := s.merchantRepo.GetByID(ctx, key.MerchantID)
	if err != nil {
		return nil, err
	}
	if !m.IsActive {
		return nil, merchant.ErrAPIKeyMerchantInactive
	}

	_ = s.apiKeyRepo.TouchLastUsed(ctx, key.ID)

	env := strings.ToLower(strings.TrimSpace(key.Name))
	if env != "sandbox" {
		env = "production"
	}

	return &AuthenticatedMerchant{
		MerchantID:  key.MerchantID,
		APIKeyID:    key.ID,
		KeyPrefix:   key.KeyPrefix,
		Environment: env,
	}, nil
}

func normalizeAPIKeyEnvironment(req *merchant.UpsertAPIKeyRequest) string {
	raw := ""
	if req != nil {
		raw = strings.ToLower(strings.TrimSpace(req.Environment))
		if raw == "" {
			raw = strings.ToLower(strings.TrimSpace(req.Name))
		}
	}
	switch raw {
	case "production", "prod", "live":
		return "production"
	default:
		return "sandbox"
	}
}

func generateAPIKeySecret(env string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate api key: %w", err)
	}
	prefix := "pk_test_"
	if env == "production" {
		prefix = "pk_live_"
	}
	return prefix + hex.EncodeToString(buf), nil
}

func hashAPIKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
