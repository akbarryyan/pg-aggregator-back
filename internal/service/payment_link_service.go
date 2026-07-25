package service

import (
	cryptorand "crypto/rand"
	"fmt"
	"time"

	"context"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/paymentlink"
	"github.com/google/uuid"
)

// slugAlphabet excludes visually ambiguous characters (0/O, 1/l/I) since
// slugs are meant to be read, typed, and shared in a public URL.
const slugAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"
const slugLength = 10
const maxSlugGenerationAttempts = 5

type PaymentLinkService struct {
	linkRepo    paymentLinkRepository
	paymentRepo paymentRepository
	paymentSvc  *PaymentService
}

func NewPaymentLinkService(linkRepo paymentLinkRepository, paymentRepo paymentRepository, paymentSvc *PaymentService) *PaymentLinkService {
	return &PaymentLinkService{
		linkRepo:    linkRepo,
		paymentRepo: paymentRepo,
		paymentSvc:  paymentSvc,
	}
}

// CreateLink is a pure DB write — no provider/Cashi call happens here.
// QRIS is only valid ~10 minutes, so pre-generating one for a reusable link
// would be useless; the actual charge only happens at InitiateCheckout.
func (s *PaymentLinkService) CreateLink(ctx context.Context, req *paymentlink.CreatePaymentLinkRequest) (*paymentlink.PaymentLink, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	slug, err := s.generateUniqueSlug(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	l := &paymentlink.PaymentLink{
		ID:          uuid.New(),
		MerchantID:  req.MerchantID,
		Slug:        slug,
		Title:       req.Title,
		Description: req.Description,
		AmountType:  req.AmountType,
		Amount:      req.Amount,
		Currency:    req.Currency,
		MinAmount:   req.MinAmount,
		MaxAmount:   req.MaxAmount,
		IsActive:    true,
		ExpiresAt:   req.ExpiresAt,
		Environment: req.Environment,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.linkRepo.Create(ctx, l); err != nil {
		return nil, fmt.Errorf("failed to create payment link: %w", err)
	}

	return l, nil
}

// generateUniqueSlug generates a random slug and confirms via a lookup that
// it doesn't already exist (check-then-insert, same tolerance for a narrow
// TOCTOU race as the existing merchant-email duplicate check in
// admin_service.go — the DB's UNIQUE constraint is still the backstop).
func (s *PaymentLinkService) generateUniqueSlug(ctx context.Context) (string, error) {
	for i := 0; i < maxSlugGenerationAttempts; i++ {
		slug, err := randomSlug()
		if err != nil {
			return "", fmt.Errorf("failed to generate slug: %w", err)
		}
		if _, err := s.linkRepo.GetBySlug(ctx, slug); err == paymentlink.ErrPaymentLinkNotFound {
			return slug, nil
		} else if err != nil {
			return "", err
		}
		// slug already exists — try again
	}
	return "", fmt.Errorf("failed to generate a unique payment link slug after %d attempts", maxSlugGenerationAttempts)
}

func randomSlug() (string, error) {
	b := make([]byte, slugLength)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = slugAlphabet[int(b[i])%len(slugAlphabet)]
	}
	return string(b), nil
}

// GetLink fetches a link for its owning merchant. Cross-merchant access
// reports not-found rather than forbidden, matching how payment ownership
// checks behave elsewhere (avoids confirming a link id exists to a
// non-owner).
func (s *PaymentLinkService) GetLink(ctx context.Context, id, merchantID uuid.UUID) (*paymentlink.PaymentLink, error) {
	return s.getOwnedLink(ctx, id, merchantID)
}

func (s *PaymentLinkService) getOwnedLink(ctx context.Context, id, merchantID uuid.UUID) (*paymentlink.PaymentLink, error) {
	l, err := s.linkRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.MerchantID != merchantID {
		return nil, paymentlink.ErrPaymentLinkNotFound
	}
	return l, nil
}

// GetPublicLink resolves a link by its public slug — no ownership check,
// used by the unauthenticated pay-via-link page.
func (s *PaymentLinkService) GetPublicLink(ctx context.Context, slug string) (*paymentlink.PaymentLink, error) {
	return s.linkRepo.GetBySlug(ctx, slug)
}

type PaginatedPaymentLinks struct {
	Items  []*paymentlink.PaymentLink
	Total  int
	Limit  int
	Offset int
}

func (s *PaymentLinkService) ListLinks(
	ctx context.Context,
	merchantID uuid.UUID,
	environment string,
	isActive *bool,
	limit, offset int,
) (*PaginatedPaymentLinks, error) {
	if limit <= 0 {
		limit = 20
	}
	items, err := s.linkRepo.List(ctx, merchantID, environment, isActive, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.linkRepo.Count(ctx, merchantID, environment, isActive)
	if err != nil {
		return nil, err
	}
	return &PaginatedPaymentLinks{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *PaymentLinkService) UpdateLink(
	ctx context.Context,
	id, merchantID uuid.UUID,
	req *paymentlink.UpdatePaymentLinkRequest,
) (*paymentlink.PaymentLink, error) {
	l, err := s.getOwnedLink(ctx, id, merchantID)
	if err != nil {
		return nil, err
	}

	if req.Title != nil {
		if *req.Title == "" {
			return nil, paymentlink.ErrTitleRequired
		}
		l.Title = *req.Title
	}
	if req.Description != nil {
		l.Description = *req.Description
	}
	if req.IsActive != nil {
		l.IsActive = *req.IsActive
	}
	if req.ExpiresAt != nil {
		l.ExpiresAt = req.ExpiresAt
	}
	if req.MinAmount != nil {
		l.MinAmount = req.MinAmount
	}
	if req.MaxAmount != nil {
		l.MaxAmount = req.MaxAmount
	}
	if l.MinAmount != nil && l.MaxAmount != nil && *l.MinAmount > *l.MaxAmount {
		return nil, paymentlink.ErrInvalidAmountBounds
	}

	l.UpdatedAt = time.Now().UTC()
	if err := s.linkRepo.Update(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (s *PaymentLinkService) SetActive(ctx context.Context, id, merchantID uuid.UUID, isActive bool) error {
	if _, err := s.getOwnedLink(ctx, id, merchantID); err != nil {
		return err
	}
	return s.linkRepo.SetActive(ctx, id, isActive)
}

func (s *PaymentLinkService) ListLinkPayments(
	ctx context.Context,
	id, merchantID uuid.UUID,
	limit, offset int,
) ([]*payment.Payment, int, error) {
	if _, err := s.getOwnedLink(ctx, id, merchantID); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 20
	}
	items, err := s.paymentRepo.ListByPaymentLinkID(ctx, id, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.paymentRepo.CountByPaymentLinkID(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// InitiateCheckout is the lazy "customer clicks pay" path — the only place
// a Payment (and its QRIS) gets created for a link, and only at this
// moment, per the ~10-minute QRIS validity constraint.
func (s *PaymentLinkService) InitiateCheckout(
	ctx context.Context,
	slug string,
	req *paymentlink.InitiateCheckoutRequest,
) (*payment.Payment, error) {
	link, err := s.linkRepo.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}

	if ok, reason := link.IsAvailable(time.Now()); !ok {
		if reason == "expired" {
			return nil, paymentlink.ErrPaymentLinkExpired
		}
		return nil, paymentlink.ErrPaymentLinkInactive
	}

	var amount int64
	if link.AmountType == paymentlink.AmountTypeFixed {
		// Client-supplied amount, if any, is ignored — never trusted for a
		// fixed-price link.
		amount = *link.Amount
	} else {
		if req.Amount <= 0 {
			return nil, paymentlink.ErrCustomerAmountRequired
		}
		min, max := link.EffectiveBounds()
		if req.Amount < min || req.Amount > max {
			return nil, paymentlink.ErrCustomerAmountOutOfRange
		}
		amount = req.Amount
	}

	description := link.Description
	if description == "" {
		description = link.Title
	}

	createReq := &payment.CreatePaymentRequest{
		MerchantID:    link.MerchantID,
		Amount:        amount,
		Currency:      link.Currency,
		PaymentMethod: payment.PaymentMethodQRIS,
		Description:   description,
		CustomerName:  req.CustomerName,
		CustomerEmail: req.CustomerEmail,
		Environment:   link.Environment,
		PaymentLinkID: &link.ID,
	}

	return s.paymentSvc.CreatePayment(ctx, createReq)
}
