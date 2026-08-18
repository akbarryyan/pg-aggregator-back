package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/provider"
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

	// Mirror Cashi-like unique amount suffix (1–99) without network
	unique := time.Now().UnixNano()%99 + 1
	finalAmount := req.Amount + unique

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
	a.mu.RUnlock()
	if !ok {
		// Unknown ref (e.g. after API restart): treat as pending
		return &provider.NormalizedPaymentStatus{
			Status:            "pending",
			ProviderReference: providerReference,
		}, nil
	}

	status := st.Status
	if status == "pending" && time.Now().After(st.ExpiresAt) {
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
