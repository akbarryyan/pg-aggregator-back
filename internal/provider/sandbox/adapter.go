package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/fee"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
)

const ProviderName = "sandbox"

// Adapter is an in-process mock payment provider for merchant sandbox environment.
// It never calls external networks (no Cashi HTTP).
type Adapter struct {
	mu     sync.RWMutex
	orders map[string]*orderState
}

type orderState struct {
	Status    string
	Amount    int64
	ExpiresAt time.Time
	CreatedAt time.Time
}

// feeBasisPoints mirrors Cashi so merchants can test with identical numbers.
const feeBasisPoints = 70

// QuoteTotal tells the total a customer must pay so the merchant still nets
// the nominal after the (simulated) provider fee.
func (a *Adapter) QuoteTotal(nominal int64) (total, feeAmount int64, err error) {
	return fee.QuoteTotalBasisPoints(nominal, feeBasisPoints)
}

func NewAdapter() *Adapter {
	return &Adapter{
		orders: make(map[string]*orderState),
	}
}

func (a *Adapter) GetName() string {
	return ProviderName
}

func (a *Adapter) CreatePayment(ctx context.Context, req *provider.ProviderPaymentRequest) (*provider.ProviderPaymentResponse, error) {
	_ = ctx
	if req.Amount < 2000 {
		return nil, fmt.Errorf("sandbox: amount must be at least 2000")
	}

	// Real Cashi payments carry no unique amount suffix (a Rp 2.000 payment is
	// charged exactly Rp 2.000), so the sandbox must not add one either.
	finalAmount := req.Amount

	ref := fmt.Sprintf("SBX-%s", strings.ReplaceAll(uuid.New().String(), "-", "")[:16])
	expiresAt := req.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(10 * time.Minute)
	}

	// Encodes a placeholder payload as an actual scannable QR image (not a
	// real QRIS EMV string — for UI/demo only) so the checkout page renders
	// it the same way it would a real provider's QR, instead of dumping text.
	qrPayload := fmt.Sprintf("sandbox:order=%s;amount=%d;ref=%s", req.InternalReference, finalAmount, ref)
	png, err := qrcode.Encode(qrPayload, qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to generate QR image: %w", err)
	}
	qr := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	checkout := fmt.Sprintf("https://sandbox.local/pay/%s", ref)

	a.mu.Lock()
	a.orders[ref] = &orderState{
		Status:    "pending",
		Amount:    finalAmount,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
	a.mu.Unlock()

	return &provider.ProviderPaymentResponse{
		ProviderReference: ref,
		ProviderName:      ProviderName,
		Status:            "pending",
		Amount:            finalAmount,
		QRISData:          &qr,
		PaymentURL:        &checkout,
		ExpiresAt:         expiresAt,
		RawResponse: map[string]interface{}{
			"sandbox":  true,
			"order_id": ref,
			"amount":   finalAmount,
			"message":  "Sandbox payment — no real money and no Cashi call",
		},
	}, nil
}

func (a *Adapter) GetPaymentStatus(ctx context.Context, providerReference string) (*provider.NormalizedPaymentStatus, error) {
	_ = ctx
	a.mu.RLock()
	st, ok := a.orders[providerReference]
	// Copy every field we read while the lock is held: *st is mutated in
	// place by MarkPaid and by the expiry write below, so reading it after
	// RUnlock would race.
	var (
		amount    int64
		status    string
		expiresAt time.Time
	)
	if ok {
		amount, status, expiresAt = st.Amount, st.Status, st.ExpiresAt
	}
	a.mu.RUnlock()
	if !ok {
		// Unknown ref (e.g. after API restart): treat as pending
		return &provider.NormalizedPaymentStatus{
			Status:            "pending",
			ProviderReference: providerReference,
		}, nil
	}

	if status == "pending" && time.Now().After(expiresAt) {
		status = "expired"
		a.mu.Lock()
		if cur, exists := a.orders[providerReference]; exists && cur.Status == "pending" {
			cur.Status = "expired"
		}
		a.mu.Unlock()
	}

	// Dev helper: mark paid if reference contains "PAID" (optional manual testing)
	if strings.Contains(strings.ToUpper(providerReference), "FORCEPAID") {
		status = "paid"
	}

	return &provider.NormalizedPaymentStatus{
		Status:            status,
		ProviderReference: providerReference,
		Amount:            &amount,
	}, nil
}

// MarkPaid allows tests/admin tools to simulate settlement (in-memory only).
func (a *Adapter) MarkPaid(providerReference string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.orders[providerReference]
	if !ok {
		return false
	}
	st.Status = "paid"
	return true
}

func (a *Adapter) ValidateWebhook(rawPayload []byte, signature string) error {
	_ = rawPayload
	_ = signature
	// Sandbox webhooks are not used from Cashi
	return nil
}

func (a *Adapter) ParseWebhook(rawPayload []byte) (*provider.ProviderWebhookPayload, error) {
	_ = rawPayload
	return nil, fmt.Errorf("sandbox provider does not accept external webhooks")
}

func (a *Adapter) NormalizeStatus(providerStatus string) string {
	switch strings.ToLower(providerStatus) {
	case "paid", "settled", "success":
		return "paid"
	case "expired":
		return "expired"
	case "failed":
		return "failed"
	default:
		return "pending"
	}
}
