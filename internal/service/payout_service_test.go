package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/ledger"
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/google/uuid"
)

type stubPayoutRepo struct {
	gotMin    int64
	gotAmount int64
	gotMerch  uuid.UUID
	open      bool
	err       error
}

func (r *stubPayoutRepo) Request(ctx context.Context, merchantID uuid.UUID, amount, minAmount int64, by *uuid.UUID) (*ledger.Payout, error) {
	r.gotMerch, r.gotAmount, r.gotMin = merchantID, amount, minAmount
	if r.err != nil {
		return nil, r.err
	}
	return &ledger.Payout{ID: uuid.New(), MerchantID: merchantID, Amount: amount}, nil
}
func (r *stubPayoutRepo) MarkPaid(ctx context.Context, id uuid.UUID, ref string, a *uuid.UUID) (*ledger.Payout, error) {
	return &ledger.Payout{ID: id}, r.err
}
func (r *stubPayoutRepo) Reject(ctx context.Context, id uuid.UUID, reason string, a *uuid.UUID) (*ledger.Payout, error) {
	return &ledger.Payout{ID: id}, r.err
}
func (r *stubPayoutRepo) ListByMerchant(ctx context.Context, m uuid.UUID, l, o int) ([]*ledger.Payout, int64, error) {
	return nil, 0, r.err
}
func (r *stubPayoutRepo) ListAdmin(ctx context.Context, s string, l, o int) ([]*ledger.Payout, int64, error) {
	return nil, 0, r.err
}
func (r *stubPayoutRepo) Stats(ctx context.Context, days int) (*repository.PayoutStats, error) {
	return &repository.PayoutStats{}, r.err
}
func (r *stubPayoutRepo) HasOpen(ctx context.Context, m uuid.UUID) (bool, error) {
	return r.open, r.err
}

type stubAccountRepo struct {
	acc *merchant.PayoutAccount
	set *merchant.PayoutAccount
	err error
}

func (r *stubAccountRepo) GetPayoutAccount(ctx context.Context, id uuid.UUID) (*merchant.PayoutAccount, error) {
	return r.acc, r.err
}
func (r *stubAccountRepo) SetPayoutAccount(ctx context.Context, id uuid.UUID, bank, number, holder string) error {
	if r.err != nil {
		return r.err
	}
	r.set = &merchant.PayoutAccount{BankName: bank, AccountNumber: number, AccountHolder: holder}
	return nil
}

func TestPayoutService_SetAccountValidatesAndTrims(t *testing.T) {
	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}
	tests := []struct {
		name string
		in   merchant.PayoutAccount
		want error
	}{
		{"ok", merchant.PayoutAccount{BankName: "  BCA ", AccountNumber: " 1234567890 ", AccountHolder: " Budi Santoso "}, nil},
		{"one-char bank", merchant.PayoutAccount{BankName: "B", AccountNumber: "12345", AccountHolder: "Budi"}, merchant.ErrPayoutBankInvalid},
		{"long bank", merchant.PayoutAccount{BankName: long(101), AccountNumber: "12345", AccountHolder: "Budi"}, merchant.ErrPayoutBankInvalid},
		{"short number", merchant.PayoutAccount{BankName: "BCA", AccountNumber: "1234", AccountHolder: "Budi"}, merchant.ErrPayoutAccountNumberInvalid},
		{"letters in number", merchant.PayoutAccount{BankName: "BCA", AccountNumber: "12a45", AccountHolder: "Budi"}, merchant.ErrPayoutAccountNumberInvalid},
		{"long number", merchant.PayoutAccount{BankName: "BCA", AccountNumber: "1234567890123456789012345678901", AccountHolder: "Budi"}, merchant.ErrPayoutAccountNumberInvalid},
		{"blank holder", merchant.PayoutAccount{BankName: "BCA", AccountNumber: "12345", AccountHolder: "   "}, merchant.ErrPayoutHolderInvalid},
		{"long holder", merchant.PayoutAccount{BankName: "BCA", AccountNumber: "12345", AccountHolder: long(151)}, merchant.ErrPayoutHolderInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc := &stubAccountRepo{}
			svc := NewPayoutService(&stubPayoutRepo{}, acc, 50000)
			got, err := svc.SetAccount(context.Background(), uuid.New(), tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				if acc.set != nil {
					t.Fatal("invalid account must not be stored")
				}
				return
			}
			want := merchant.PayoutAccount{BankName: "BCA", AccountNumber: "1234567890", AccountHolder: "Budi Santoso"}
			if *acc.set != want || *got != want {
				t.Fatalf("stored %+v returned %+v, want trimmed %+v", acc.set, got, want)
			}
		})
	}
}

func TestPayoutService_RequestUsesConfiguredMinimum(t *testing.T) {
	repo := &stubPayoutRepo{}
	svc := NewPayoutService(repo, &stubAccountRepo{}, 75000)
	m := uuid.New()
	if _, err := svc.Request(context.Background(), m, 80000, nil); err != nil {
		t.Fatal(err)
	}
	if repo.gotMin != 75000 || repo.gotAmount != 80000 || repo.gotMerch != m {
		t.Fatalf("repo got min=%d amount=%d merchant=%s", repo.gotMin, repo.gotAmount, repo.gotMerch)
	}
	if svc.MinAmount() != 75000 {
		t.Fatalf("MinAmount = %d", svc.MinAmount())
	}
}

func TestBalanceService_CanRequest(t *testing.T) {
	acc := &merchant.PayoutAccount{BankName: "BCA", AccountNumber: "12345", AccountHolder: "Budi"}
	tests := []struct {
		name    string
		balance int64
		acc     *merchant.PayoutAccount
		open    bool
		want    bool
	}{
		{"all good", 50000, acc, false, true},
		{"below minimum", 49999, acc, false, false},
		{"no account", 90000, nil, false, false},
		{"open payout", 90000, acc, true, false},
		{"negative", -10, acc, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewBalanceService(stubLedgerRepo{sum: &ledger.Summary{Balance: tc.balance}}, &stubAccountRepo{acc: tc.acc}, &stubPayoutRepo{open: tc.open}, 50000)
			ov, err := svc.Overview(context.Background(), uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			if ov.CanRequest != tc.want || ov.MinPayoutAmount != 50000 || ov.Balance != tc.balance {
				t.Fatalf("overview = %+v, want can_request=%v", ov, tc.want)
			}
		})
	}
}

type stubLedgerRepo struct{ sum *ledger.Summary }

func (r stubLedgerRepo) Summary(ctx context.Context, m uuid.UUID) (*ledger.Summary, error) {
	return r.sum, nil
}
func (r stubLedgerRepo) List(ctx context.Context, m uuid.UUID, l, o int) ([]*ledger.Entry, int64, error) {
	return nil, 0, nil
}
func (r stubLedgerRepo) ListBalances(ctx context.Context, n bool, l, o int) ([]*ledger.MerchantBalance, int64, error) {
	return nil, 0, nil
}

type stubPayoutQueue struct {
	n      int64
	latest *time.Time
	err    error
}

func (q stubPayoutQueue) PendingSummary(ctx context.Context) (int64, *time.Time, error) {
	return q.n, q.latest, q.err
}

func TestAdminNotificationService_PayoutRequestedItem(t *testing.T) {
	latest := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	find := func(res *NotificationsResponse) *AdminNotification {
		for i := range res.Items {
			if res.Items[i].Kind == "payout_requested" {
				return &res.Items[i]
			}
		}
		return nil
	}
	list := func(t *testing.T, q payoutQueue) *NotificationsResponse {
		svc, mock := newAdminNotificationServiceWithMock(t)
		svc.WithPayoutQueue(q)
		expectEmptyAdminFeed(mock)
		res, err := svc.ListNotifications(context.Background(), 30)
		if err != nil {
			t.Fatalf("ListNotifications: %v", err)
		}
		return res
	}

	res := list(t, stubPayoutQueue{n: 3, latest: &latest})
	n := find(res)
	if n == nil {
		t.Fatalf("no payout_requested item in %+v", res.Items)
	}
	if n.Href != "/admin/payouts" || !n.Attention || n.Body != "3 payout requests are waiting to be processed" || n.CreatedAt != "2026-11-05T10:00:00Z" {
		t.Fatalf("unexpected item %+v", n)
	}
	if res.AttentionCount != 1 {
		t.Fatalf("attention = %d", res.AttentionCount)
	}
	if one := find(list(t, stubPayoutQueue{n: 1, latest: &latest})); one == nil || one.Body != "1 payout request is waiting to be processed" {
		t.Fatalf("singular body = %+v", one)
	}
	if find(list(t, stubPayoutQueue{})) != nil {
		t.Fatal("no open payouts must add no item")
	}
}

func TestCapFeed_PinnedPayoutSurvivesTruncation(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var items []AdminNotification
	for i := 0; i < 60; i++ {
		items = append(items, AdminNotification{ID: fmt.Sprintf("n%d", i), Kind: "payment_failed",
			CreatedAt: time.Date(2026, 11, 1, 0, i, 0, 0, time.UTC).Format(time.RFC3339)})
	}
	pn := payoutRequestedNotification(2, old)
	got := capFeed(items, &pn, 50)
	if len(got) != 50 {
		t.Fatalf("len = %d, want 50", len(got))
	}
	if got[len(got)-1].Kind != "payout_requested" {
		t.Fatalf("pinned item missing/misplaced: last=%+v", got[len(got)-1])
	}
	if got[0].ID != "n59" {
		t.Fatalf("feed must be newest first, got first=%s", got[0].ID)
	}
	attention := 0
	for _, n := range got {
		if n.Attention {
			attention++
		}
	}
	if attention != 1 {
		t.Fatalf("attention = %d", attention)
	}
	// Without the pin the old behaviour (plain cap) holds.
	if got := capFeed(items, nil, 50); len(got) != 50 || got[len(got)-1].Kind == "payout_requested" {
		t.Fatalf("plain cap: len=%d", len(got))
	}
	// Small feed: nothing is dropped.
	if got := capFeed(items[:3], &pn, 50); len(got) != 4 {
		t.Fatalf("small feed len = %d", len(got))
	}
}
