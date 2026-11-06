package fee

import (
	"errors"
	"testing"
)

// ceilFee is written differently from the implementation on purpose, so a
// shared typo cannot hide.
func ceilFee(gross, bps int64) int64 {
	x := gross * bps
	q := x / 10000
	if x%10000 != 0 {
		q++
	}
	return q
}

func TestQuoteTotalBasisPoints_KnownValues(t *testing.T) {
	tests := []struct {
		nominal, bps, wantTotal, wantFee int64
	}{
		{2000, 70, 2015, 15}, // the real Rp 2.000 case: total 2.014 would net only 1.999
		{1000, 70, 1008, 8},
		{10000, 70, 10071, 71},
		{1, 70, 2, 1},
		{2000, 0, 2000, 0}, // zero rate -> no fee
		{5000, 100, 5051, 51},
		{MaxNominal, 70, 1007049345418, 7049345418},
		{MaxNominal, 9999, 10000000000000000, 9999000000000000},
		{MaxNominal, 5000, 2000000000000, 1000000000000},
	}
	for _, tc := range tests {
		total, fee, err := QuoteTotalBasisPoints(tc.nominal, tc.bps)
		if err != nil {
			t.Fatalf("(%d,%d): unexpected error %v", tc.nominal, tc.bps, err)
		}
		if total != tc.wantTotal || fee != tc.wantFee {
			t.Errorf("(%d,%d) = total %d fee %d, want total %d fee %d", tc.nominal, tc.bps, total, fee, tc.wantTotal, tc.wantFee)
		}
	}
}

// For every nominal the total must leave at least the nominal after the
// (rounded-up) provider fee, and be the smallest total that does.
func TestQuoteTotalBasisPoints_ExactMinimumProperty(t *testing.T) {
	for _, bps := range []int64{70, 100, 250, 1, 5000, 9999} {
		for nominal := int64(1); nominal <= 200000; nominal++ {
			total, fee, err := QuoteTotalBasisPoints(nominal, bps)
			if err != nil {
				t.Fatalf("(%d,%d): %v", nominal, bps, err)
			}
			if fee != total-nominal || fee < 0 {
				t.Fatalf("(%d,%d): fee %d inconsistent with total %d", nominal, bps, fee, total)
			}
			if total-ceilFee(total, bps) < nominal {
				t.Fatalf("(%d,%d): total %d leaves %d, less than nominal", nominal, bps, total, total-ceilFee(total, bps))
			}
			if total > nominal && (total-1)-ceilFee(total-1, bps) >= nominal {
				t.Fatalf("(%d,%d): total %d is not minimal", nominal, bps, total)
			}
		}
	}
}

func TestQuoteTotalBasisPoints_Invalid(t *testing.T) {
	for _, n := range []int64{0, -5, MaxNominal + 1} {
		if _, _, err := QuoteTotalBasisPoints(n, 70); !errors.Is(err, ErrInvalidNominal) {
			t.Errorf("nominal %d: want ErrInvalidNominal, got %v", n, err)
		}
	}
	for _, bps := range []int64{-1, 10000, 20000} {
		if _, _, err := QuoteTotalBasisPoints(2000, bps); !errors.Is(err, ErrInvalidRate) {
			t.Errorf("bps %d: want ErrInvalidRate, got %v", bps, err)
		}
	}
	total, fee, err := QuoteTotalBasisPoints(MaxNominal, 70)
	if err != nil || total != 1007049345418 || fee != 7049345418 {
		t.Errorf("MaxNominal: got total %d fee %d err %v", total, fee, err)
	}
}

func TestMerchantNetBasisPoints(t *testing.T) {
	cases := []struct {
		nominal, bps, net, fee int64
	}{
		{10000, 70, 9930, 70}, // contoh spec: 10.000 -> 9.930
		{2000, 70, 1986, 14},  // 2000*70/10000 = 14.0 -> 14 (pas, tanpa pembulatan)
		{100, 70, 99, 1},      // 0.7 dibulatkan ke atas -> 1
		{1, 70, 0, 1},         // sangat kecil: fee membulat ke atas
		{10000, 0, 10000, 0},  // bps 0: tanpa fee
	}
	for _, c := range cases {
		net, fee, err := MerchantNetBasisPoints(c.nominal, c.bps)
		if err != nil || net != c.net || fee != c.fee {
			t.Errorf("MerchantNetBasisPoints(%d,%d) = (%d,%d,%v), want (%d,%d,nil)", c.nominal, c.bps, net, fee, err, c.net, c.fee)
		}
	}
	if _, _, err := MerchantNetBasisPoints(0, 70); err == nil {
		t.Error("nominal 0 must be rejected")
	}
	if _, _, err := MerchantNetBasisPoints(MaxNominal+1, 70); err == nil {
		t.Error("nominal above MaxNominal must be rejected")
	}
	if _, _, err := MerchantNetBasisPoints(1000, 10000); err == nil {
		t.Error("bps 10000 must be rejected")
	}
}
