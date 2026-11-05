// Package fee computes the total a customer must pay so that the merchant
// still receives the nominal amount after the payment provider deducts its
// percentage fee. Integer arithmetic only: money never touches a float.
package fee

import "errors"

const (
	bpsDenominator = 10000

	// MaxNominal is the largest accepted nominal. It guarantees that
	// nominal*10000 and the resulting total stay inside int64 for every
	// allowed bps (the worst case, bps 9999, yields a total of 1e16).
	MaxNominal int64 = 1_000_000_000_000
)

var (
	ErrInvalidNominal = errors.New("fee: nominal must be between 1 and 1000000000000")
	ErrInvalidRate    = errors.New("fee: basis points must be in [0, 10000)")
)

// QuoteTotalBasisPoints returns the smallest total G such that
// G - ceil(G*bps/10000) >= nominal, and the fee (G - nominal). It assumes the
// provider rounds its fee UP, the conservative direction.
//
// Closed form: with D = 10000 and b = bps, G - ceil(G*b/D) >= n holds iff
// G - G*b/D >= n (n is an integer, so the ceiling can be absorbed), i.e.
// G*(D-b) >= n*D, i.e. G >= ceil(n*D/(D-b)). That bound is the exact minimum,
// so no search is needed (and none is done: evaluating G*b near MaxNominal
// could overflow). The only product is nominal*D <= 1e16.
//
// If the provider actually rounds its fee down, the merchant ends up at most
// 1 rupiah ahead.
func QuoteTotalBasisPoints(nominal, bps int64) (total, feeAmount int64, err error) {
	if nominal < 1 || nominal > MaxNominal {
		return 0, 0, ErrInvalidNominal
	}
	if bps < 0 || bps >= bpsDenominator {
		return 0, 0, ErrInvalidRate
	}
	if bps == 0 {
		return nominal, 0, nil
	}

	denom := bpsDenominator - bps
	g := (nominal*bpsDenominator + denom - 1) / denom
	return g, g - nominal, nil
}
