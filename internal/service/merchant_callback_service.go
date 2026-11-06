package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// maxCallbackAttempts caps auto-retry: once a delivery has failed this many
// times, NextRetryAt is left unset so DeliverMerchantCallbacks stops
// requeueing it. Admins can still retry it manually from the dashboard.
const maxCallbackAttempts = 5

// EnsureMerchantWebhookSecret returns the merchant's HMAC signing secret for
// outbound payment webhooks, generating and persisting one on first use.
// Mirrors CASHI_SECRET_KEY's role for inbound Cashi webhooks, just for the
// direction we control (see docs/cashi-api.md's webhook verification example).
func (s *PaymentService) EnsureMerchantWebhookSecret(ctx context.Context, merchantID uuid.UUID) (string, error) {
	if s.merchantRepo == nil {
		return "", fmt.Errorf("merchant repository not available")
	}
	m, err := s.merchantRepo.GetByID(ctx, merchantID)
	if err != nil {
		return "", err
	}
	if m.WebhookSecret != nil && *m.WebhookSecret != "" {
		return *m.WebhookSecret, nil
	}
	return s.RegenerateMerchantWebhookSecret(ctx, merchantID)
}

// RegenerateMerchantWebhookSecret issues a new secret, invalidating the old
// one immediately — any endpoint still verifying against the previous
// secret will start rejecting signatures until updated.
func (s *PaymentService) RegenerateMerchantWebhookSecret(ctx context.Context, merchantID uuid.UUID) (string, error) {
	if s.merchantRepo == nil {
		return "", fmt.Errorf("merchant repository not available")
	}
	secret, err := generateWebhookSecret()
	if err != nil {
		return "", fmt.Errorf("failed to generate webhook secret: %w", err)
	}
	if err := s.merchantRepo.SetWebhookSecret(ctx, merchantID, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func signWebhookPayload(secret string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// NotifyMerchantPaymentEvent queues an outbound callback when payment status
// changes: it only records a 'pending' delivery row, and DeliverMerchantCallbacks
// sends it. Keeping HTTP out of here means a slow or hostile merchant endpoint
// can't stall webhook, reconcile, or expire processing.
// latePayment marks a settlement that arrived after the payment had expired;
// merchants may already have cancelled the order.
// Prefer payment.callback_url, else merchant.webhook_url. Skips if neither is set.
func (s *PaymentService) NotifyMerchantPaymentEvent(ctx context.Context, p *payment.Payment, eventType string, latePayment bool) {
	if s.callbackRepo == nil || s.merchantRepo == nil || p == nil {
		return
	}

	targetURL, err := s.resolveCallbackTarget(ctx, p)
	if err != nil {
		logger.WarnfCtx(ctx, "Merchant callback skipped: merchant not found for payment %s: %v", p.Reference, err)
		return
	}

	if targetURL == "" {
		logger.InfofCtx(ctx, "Merchant callback skipped for payment %s: no webhook URL", p.Reference)
		return
	}

	payload := map[string]interface{}{
		"event":        eventType,
		"payment_id":   p.ID.String(),
		"reference":    p.Reference,
		"status":       p.Status,
		"amount":       p.Amount, // always the merchant nominal
		"fee_amount":   p.FeeAmount,
		"total_amount": p.TotalAmount(),
		"currency":     p.Currency,
		"merchant_id":  p.MerchantID.String(),
		"provider":     p.ProviderName,
		"occurred_at":  time.Now().UTC().Format(time.RFC3339),
		"late_payment": latePayment,
	}
	if p.ProviderReference != nil {
		payload["provider_reference"] = *p.ProviderReference
	}
	if p.PaidAt != nil {
		payload["paid_at"] = p.PaidAt.UTC().Format(time.RFC3339)
	}

	delivery := &merchant.CallbackDelivery{
		ID:             uuid.New(),
		PaymentID:      p.ID,
		MerchantID:     p.MerchantID,
		EventType:      eventType,
		TargetURL:      targetURL,
		RequestPayload: payload,
		AttemptNumber:  1,
		Status:         merchant.CallbackStatusPending,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	if err := s.callbackRepo.Create(ctx, delivery); err != nil {
		logger.ErrorfCtx(ctx, "Failed to persist merchant callback for %s: %v", p.Reference, err)
		return
	}
}

// resolveCallbackTarget picks where a payment's callbacks go: its own
// callback_url if non-blank, else the merchant's webhook_url, else "". The
// merchant is only looked up when the payment has no URL of its own.
func (s *PaymentService) resolveCallbackTarget(ctx context.Context, p *payment.Payment) (string, error) {
	if p.CallbackURL != nil && strings.TrimSpace(*p.CallbackURL) != "" {
		return strings.TrimSpace(*p.CallbackURL), nil
	}
	m, err := s.merchantRepo.GetByID(ctx, p.MerchantID)
	if err != nil {
		return "", err
	}
	if m.WebhookURL != nil {
		return strings.TrimSpace(*m.WebhookURL), nil
	}
	return "", nil
}

func (s *PaymentService) RetryMerchantCallback(ctx context.Context, id uuid.UUID) (*merchant.CallbackDelivery, error) {
	if s.callbackRepo == nil {
		return nil, fmt.Errorf("callback repository not available")
	}
	existing, err := s.callbackRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.retryMerchantCallbackData(ctx, existing, nil)
}

// retryMerchantCallbackData is RetryMerchantCallback's body operating on an
// already-fetched delivery: it records a new attempt row and sends it
// synchronously (an admin pressing "retry" wants the outcome now). Any source
// status is accepted on purpose, including re-sending one that succeeded.
// secretCache is passed through to executeCallbackDelivery; nil for a single
// ad hoc retry.
//
// It must not race the delivery worker into duplicate sends:
//   - the source's schedule is cleared BEFORE sending, with a targeted update,
//     so RequeueDueFailed can't spawn a second attempt for the same failure;
//   - the source row is never written back from our snapshot (that could
//     revert a status the worker set meanwhile and trigger another send);
//   - the new attempt is inserted already 'sending', so ClaimDue leaves it
//     alone (it only reclaims 'sending' rows after callbackSendingStaleAfter,
//     well past the send timeout plus the detached budget).
func (s *PaymentService) retryMerchantCallbackData(
	ctx context.Context,
	existing *merchant.CallbackDelivery,
	secretCache map[uuid.UUID]string,
) (*merchant.CallbackDelivery, error) {
	// Detached: once started, a disconnected admin client must not leave the
	// schedule cleared but the new attempt never created.
	ctx, cancel := detached(ctx)
	defer cancel()

	// Look up the current URL BEFORE touching anything, so a transient failure
	// here leaves the source's schedule intact and the admin can just retry.
	targetURL, err := s.currentCallbackTarget(ctx, existing)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if _, err := s.callbackRepo.ClearRetrySchedule(ctx, existing.ID, now); err != nil {
		return nil, err
	}

	// Create a new attempt row (keeps history) based on previous delivery.
	retry := &merchant.CallbackDelivery{
		ID:             uuid.New(),
		PaymentID:      existing.PaymentID,
		MerchantID:     existing.MerchantID,
		EventType:      existing.EventType,
		TargetURL:      targetURL,
		RequestPayload: existing.RequestPayload,
		AttemptNumber:  existing.AttemptNumber + 1,
		Status:         merchant.CallbackStatusSending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.callbackRepo.Create(ctx, retry); err != nil {
		return nil, err
	}
	s.executeCallbackDelivery(ctx, retry, secretCache)
	return retry, nil
}

// currentCallbackTarget is the URL a manual retry should use: where the
// payment's callbacks go NOW, so a merchant who fixed a bad URL can get the
// failed callback delivered. It falls back to the URL stored on the delivery
// when the payment is gone or nothing is configured any more. The result is
// not trusted: executeCallbackDelivery still validates it. Any other lookup
// error is returned (likely transient). The automatic requeue deliberately
// keeps copying the stored URL.
func (s *PaymentService) currentCallbackTarget(ctx context.Context, existing *merchant.CallbackDelivery) (string, error) {
	p, err := s.paymentRepo.GetByID(ctx, existing.PaymentID)
	if errors.Is(err, payment.ErrPaymentNotFound) {
		return existing.TargetURL, nil
	}
	if err != nil {
		return "", err
	}
	target, err := s.resolveCallbackTarget(ctx, p)
	if err != nil && !errors.Is(err, merchant.ErrMerchantNotFound) {
		return "", err
	}
	if err != nil || target == "" {
		return existing.TargetURL, nil
	}
	return target, nil
}

// resolveWebhookSecret is EnsureMerchantWebhookSecret with an optional
// per-batch cache so sending several callbacks for the same merchant
// doesn't hit merchantRepo.GetByID once per delivery. Pass a nil cache to
// skip caching (single-item callers).
func (s *PaymentService) resolveWebhookSecret(ctx context.Context, merchantID uuid.UUID, secretCache map[uuid.UUID]string) (string, error) {
	if secretCache != nil {
		if secret, ok := secretCache[merchantID]; ok {
			return secret, nil
		}
	}
	secret, err := s.EnsureMerchantWebhookSecret(ctx, merchantID)
	if err != nil {
		return "", err
	}
	if secretCache != nil {
		secretCache[merchantID] = secret
	}
	return secret, nil
}

func (s *PaymentService) executeCallbackDelivery(ctx context.Context, d *merchant.CallbackDelivery, secretCache map[uuid.UUID]string) {
	// Detached: once a delivery is claimed (or a manual retry started) its
	// outcome must be recorded even if the caller's context is cancelled
	// (shutdown, a disconnected admin client).
	ctx, cancel := detached(ctx)
	defer cancel()

	allowHTTP, err := s.callbackAllowsHTTP(ctx, d)
	if err != nil {
		// A DB hiccup says nothing about the URL. Don't send (we don't know
		// which rules apply) and don't fail permanently (a sandbox http
		// callback would be lost for good): retry on the normal schedule.
		d.Status = merchant.CallbackStatusFailed
		d.NextRetryAt = nextRetryAt(d.AttemptNumber)
		// At the attempt cap there is no next retry; don't promise one.
		msg := callbackTemporaryError
		if d.NextRetryAt == nil {
			msg = callbackTemporaryErrorFinal
		}
		d.ErrorMessage = &msg
		s.recordDeliveryResult(ctx, d)
		logger.WarnfCtx(ctx, "Merchant callback for %s deferred, payment lookup failed: %v", d.PaymentID, err)
		return
	}
	if err := s.callbackValidate(d.TargetURL, allowHTTP); err != nil {
		// Typically a URL stored before the SSRF rules existed. Retrying
		// can't fix it, so fail permanently with the reason.
		s.failCallbackPermanently(ctx, d, err)
		return
	}

	body, err := json.Marshal(d.RequestPayload)
	if err != nil {
		s.failCallbackPermanently(ctx, d, err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TargetURL, bytes.NewReader(body))
	if err != nil {
		// Not err itself: url.Parse errors quote the URL, which may carry
		// tokens and would end up in error_message and the logs.
		s.failCallbackPermanently(ctx, d, errors.New(netsafe.DescribeError(err)))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pg-aggregator-callback/1.0")
	req.Header.Set("X-PG-Event", d.EventType)

	if secret, secretErr := s.resolveWebhookSecret(ctx, d.MerchantID, secretCache); secretErr != nil {
		// Signing is best-effort: a transient secret-provisioning failure
		// shouldn't block the merchant from learning their payment status
		// changed. The delivery just goes out unsigned this one time.
		logger.ErrorfCtx(ctx, "Failed to get webhook secret for merchant %s, sending unsigned: %v", d.MerchantID, secretErr)
	} else {
		req.Header.Set("X-PG-Signature", signWebhookPayload(secret, body))
	}

	resp, err := s.callbackClient().Do(req)
	if err != nil {
		// DescribeError drops the request URL (may carry tokens) and any
		// resolved internal IP before the text is stored.
		msg := netsafe.DescribeError(err)
		d.Status = merchant.CallbackStatusFailed
		d.ErrorMessage = &msg
		if errors.Is(err, netsafe.ErrBlockedAddress) {
			d.NextRetryAt = nil // retrying an internal destination can't succeed
		} else {
			d.NextRetryAt = nextRetryAt(d.AttemptNumber)
		}
		s.recordDeliveryResult(ctx, d)
		logger.WarnfCtx(ctx, "Merchant callback failed for %s: %s", d.PaymentID, msg)
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxStoredResponseBody))
	// Merchant-controlled bytes: may be binary, contain NUL, or end mid-rune
	// at the read cap, any of which PostgreSQL TEXT rejects.
	respStr := storableText(string(respBytes), maxStoredResponseBody)
	statusCode := resp.StatusCode
	d.HTTPStatus = &statusCode
	if respStr != "" {
		d.ResponseBody = &respStr
	}

	now := time.Now().UTC()
	if statusCode >= 200 && statusCode < 300 {
		d.Status = merchant.CallbackStatusSuccess
		d.DeliveredAt = &now
		d.ErrorMessage = nil
		d.NextRetryAt = nil
		logger.InfofCtx(ctx, "Merchant callback success (%d) for payment %s", statusCode, d.PaymentID)
	} else {
		d.Status = merchant.CallbackStatusFailed
		msg := fmt.Sprintf("unexpected HTTP status %d", statusCode)
		d.ErrorMessage = &msg
		d.NextRetryAt = nextRetryAt(d.AttemptNumber)
		logger.WarnfCtx(ctx, "Merchant callback HTTP %d for payment %s", statusCode, d.PaymentID)
	}
	s.recordDeliveryResult(ctx, d)
}

// callbackAllowsHTTP reports whether this delivery may use plain http: only
// for sandbox payments (IP rules apply regardless). A payment that doesn't
// exist gets the strict rules; any other lookup error is returned, since it
// is likely transient and says nothing about the payment's environment.
func (s *PaymentService) callbackAllowsHTTP(ctx context.Context, d *merchant.CallbackDelivery) (bool, error) {
	p, err := s.paymentRepo.GetByID(ctx, d.PaymentID)
	if errors.Is(err, payment.ErrPaymentNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.Environment == payment.EnvironmentSandbox, nil
}

// recordDeliveryResult persists a delivery's outcome. A failure here is not
// harmless: the row stays 'sending' and is re-sent every
// callbackSendingStaleAfter. If the failure is specific to this row's detail
// (text PostgreSQL won't store), retrying the same write would fail forever,
// so it retries once with the outcome only: status, HTTP status, delivery
// time and retry schedule kept, response body and message dropped.
func (s *PaymentService) recordDeliveryResult(ctx context.Context, d *merchant.CallbackDelivery) {
	err := s.callbackRepo.UpdateResult(ctx, d)
	if err == nil {
		return
	}
	logger.ErrorfCtx(ctx, "Failed to record merchant callback %s (payment %s, status %s), retrying without detail: %v",
		d.ID, d.PaymentID, d.Status, err)
	msg := callbackResultDetailLost
	d.ResponseBody = nil
	d.ErrorMessage = &msg
	if err := s.callbackRepo.UpdateResult(ctx, d); err != nil {
		logger.ErrorfCtx(ctx, "Failed to record merchant callback %s (payment %s) even without detail; it stays 'sending' and may be re-sent: %v",
			d.ID, d.PaymentID, err)
	}
}

// storableText makes merchant- or error-supplied text safe for a PostgreSQL
// TEXT column (which rejects invalid UTF-8 and NUL bytes): both are dropped,
// then the text is cut to at most maxBytes on a character boundary.
func storableText(s string, maxBytes int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// failCallbackPermanently records a failure no retry can fix (invalid or
// disallowed URL, unencodable payload). cause's text must be safe to store:
// netsafe's validation errors never include the URL.
func (s *PaymentService) failCallbackPermanently(ctx context.Context, d *merchant.CallbackDelivery, cause error) {
	msg := storableText(cause.Error(), maxStoredErrorMessage)
	d.Status = merchant.CallbackStatusFailed
	d.ErrorMessage = &msg
	d.NextRetryAt = nil
	s.recordDeliveryResult(ctx, d)
	logger.WarnfCtx(ctx, "Merchant callback for %s not sent: %v", d.PaymentID, cause)
}

// nextRetryAt returns when the next auto-retry should happen, or nil once
// attemptNumber has reached maxCallbackAttempts (stops RequeueDueFailed from
// picking the delivery back up).
func nextRetryAt(attemptNumber int) *time.Time {
	if attemptNumber >= maxCallbackAttempts {
		return nil
	}
	next := time.Now().UTC().Add(5 * time.Minute)
	return &next
}

const (
	// CallbackDeliveryInterval is how often the worker polls for deliveries
	// to send; callbacks reach merchants within about this long of a change.
	CallbackDeliveryInterval = 5 * time.Second

	callbackBatchSize         = 50
	callbackSendingStaleAfter = 2 * time.Minute

	// defaultCallbackBatchBudget: ClaimDue stamps every claimed row at claim
	// time, but rows are sent one by one (up to the 10s client timeout each).
	// A run must stop well before the first unsent row looks stale to another
	// worker (a rolling deploy) and gets sent twice; the margin leaves room
	// for the send in flight. It also caps how long one slow merchant can
	// hold up everyone else's callbacks.
	defaultCallbackBatchBudget = callbackSendingStaleAfter - 2*10*time.Second
)

// callbackPanicMessage is stored on a delivery whose send panicked. It is
// deliberately generic: the panic value may contain request details.
const callbackPanicMessage = "internal error while sending callback"

// callbackResultDetailLost replaces the stored detail (also on success rows,
// hence the neutral wording) when the full result
// could not be written for a row (see recordDeliveryResult).
const callbackResultDetailLost = "response detail not stored"

// callbackTemporaryError is stored when the delivery was deferred because a
// lookup it depends on failed; the normal retry schedule applies.
const (
	callbackTemporaryError      = "temporary error, will retry"
	callbackTemporaryErrorFinal = "temporary error, giving up after the last attempt"
)

const (
	maxStoredResponseBody = 4096 // bytes of the merchant's response kept
	maxStoredErrorMessage = 300  // same cap as netsafe.DescribeError
)

// DeliverMerchantCallbacks is the scheduler job that sends queued merchant
// callbacks. It first turns failed deliveries whose retry time has come into
// fresh pending attempts, then claims and sends pending ones (plus 'sending'
// rows abandoned by a dead worker). Sending is sequential and a single
// delivery failing never stops the batch.
func (s *PaymentService) DeliverMerchantCallbacks(ctx context.Context) error {
	if s.callbackRepo == nil {
		return nil
	}
	now := time.Now().UTC()
	if _, err := s.callbackRepo.RequeueDueFailed(ctx, now, callbackBatchSize); err != nil {
		return fmt.Errorf("failed to requeue failed callbacks: %w", err)
	}
	due, err := s.callbackRepo.ClaimDue(ctx, now, callbackBatchSize, callbackSendingStaleAfter)
	if err != nil {
		return fmt.Errorf("failed to claim callbacks: %w", err)
	}

	start := time.Now()
	secretCache := make(map[uuid.UUID]string)
	for i := range due {
		if ctx.Err() != nil {
			// Shutting down: hand the unsent rows back right away.
			s.releaseUnsent(ctx, due[i:], "shutting down")
			break
		}
		if time.Since(start) > s.callbackBatchBudget {
			// One slow merchant must not hold everyone else's callbacks for
			// the whole stale window: release the rest for the next tick.
			logger.WarnfCtx(ctx, "Merchant callback batch over its %s budget; %d claimed deliveries released for the next run",
				s.callbackBatchBudget, len(due)-i)
			s.releaseUnsent(ctx, due[i:], "batch budget exhausted")
			break
		}
		s.deliverClaimedCallback(ctx, &due[i], secretCache)
	}
	return nil
}

// releaseUnsent puts claimed but never-sent rows back to 'pending' so the
// next tick (or another worker) sends them without waiting out the stale
// window. Detached: it runs on shutdown, when ctx is already cancelled. If it
// fails, the stale window still recovers the rows.
func (s *PaymentService) releaseUnsent(ctx context.Context, unsent []merchant.CallbackDelivery, why string) {
	if len(unsent) == 0 {
		return
	}
	ctx, cancel := detached(ctx)
	defer cancel()
	ids := make([]uuid.UUID, len(unsent))
	for i := range unsent {
		ids[i] = unsent[i].ID
	}
	if _, err := s.callbackRepo.ReleaseClaimed(ctx, ids, time.Now().UTC()); err != nil {
		logger.ErrorfCtx(ctx, "Failed to release %d unsent merchant callbacks (%s); they are reclaimed after %s: %v",
			len(ids), why, callbackSendingStaleAfter, err)
	}
}

// deliverClaimedCallback sends one claimed delivery and contains a panic to
// it. Without this, a row that panics the worker would stay 'sending', be
// re-claimed after callbackSendingStaleAfter and panic again forever, and
// would also abort every delivery after it in the batch.
func (s *PaymentService) deliverClaimedCallback(ctx context.Context, d *merchant.CallbackDelivery, secretCache map[uuid.UUID]string) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		logger.ErrorfCtx(ctx, "Merchant callback %s for payment %s panicked, marking failed without retry: %v\n%s",
			d.ID, d.PaymentID, r, debug.Stack())
		ctx, cancel := detached(ctx)
		defer cancel()
		msg := callbackPanicMessage
		d.Status = merchant.CallbackStatusFailed
		d.ErrorMessage = &msg
		d.NextRetryAt = nil
		s.recordDeliveryResult(ctx, d)
	}()
	s.executeCallbackDelivery(ctx, d, secretCache)
}

func eventTypeForStatus(status string) string {
	switch status {
	case payment.StatusPaid:
		return "payment.paid"
	case payment.StatusExpired:
		return "payment.expired"
	case payment.StatusFailed:
		return "payment.failed"
	case payment.StatusCancelled:
		return "payment.cancelled"
	case payment.StatusRefunded:
		return "payment.refunded"
	default:
		return "payment." + status
	}
}
