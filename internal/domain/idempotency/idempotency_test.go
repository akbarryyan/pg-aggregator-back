package idempotency

import (
	"strings"
	"testing"
)

func TestValidateKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{"typical uuid", "3f1c2d9e-8a7b-4c6d-9e0f-1a2b3c4d5e6f", true},
		{"one char", "a", true},
		{"max length", strings.Repeat("a", 128), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 129), false},
		{"space", "has space", false},
		{"control char", "a\tb", false},
		{"non-ascii", "kunci-é", false},
		{"del char", "a\x7fb", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKey(tc.key)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateKey(%q) err=%v, want ok=%v", tc.key, err, tc.ok)
			}
		})
	}
}

func TestRequestHash_StableAndSensitive(t *testing.T) {
	name := "Budi"
	base := RequestFields{Amount: 1000, Currency: "IDR", PaymentMethod: "qris", Description: "x", CustomerName: &name, ExpiresInMinutes: 30}
	if RequestHash(base) != RequestHash(base) {
		t.Fatal("hash must be deterministic")
	}
	changed := base
	changed.Amount = 1001
	if RequestHash(base) == RequestHash(changed) {
		t.Error("amount must change the hash")
	}
	other := "Siti"
	changed = base
	changed.CustomerName = &other
	if RequestHash(base) == RequestHash(changed) {
		t.Error("customer name must change the hash")
	}
	if len(RequestHash(base)) != 64 {
		t.Errorf("want 64 hex chars, got %d", len(RequestHash(base)))
	}
}
