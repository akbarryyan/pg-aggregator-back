package service

import (
	"testing"

	"github.com/akbarryyan/pg-aggregator-back/internal/domain/payment"
)

func TestMerchantNetFor(t *testing.T) {
	cases := []struct {
		name        string
		amount, fee int64
		want        int64
		wantErr     bool
	}{
		{"fee on customer", 10000, 70, 10000, false},
		{"fee on merchant 10000", 10000, 0, 9930, false},
		{"fee on merchant 2000", 2000, 0, 1986, false},
		{"tiny nominal nets zero", 1, 0, 0, false},
		{"amount zero", 0, 0, 0, true},
		{"amount too large", 1 << 60, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := merchantNetFor(&payment.Payment{Amount: tc.amount, FeeAmount: tc.fee})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("net = %d, want %d", got, tc.want)
			}
		})
	}
}
