package model

import (
	"errors"

	"github.com/shopspring/decimal"
)

// ConvertMinor converts amountMinor (an integer in the smallest unit of the
// from currency, e.g. cents) into the smallest unit of the to currency, given
// rate expressed as "to-currency units per one from-currency unit".
//
// The math is exact decimal throughout: amountMinor is shifted to its major
// value by fromDec places, multiplied by the rate, then rounded to toDec
// places (half away from zero, the convention for cash rounding) and shifted
// back to minor units. No float64 is involved, so there is no rounding drift.
func ConvertMinor(amountMinor int64, fromDec, toDec int32, rate decimal.Decimal) int64 {
	amount := decimal.New(amountMinor, -fromDec) // amountMinor * 10^-fromDec
	converted := amount.Mul(rate)
	rounded := converted.Round(toDec) // half away from zero
	minor := rounded.Shift(toDec)     // back to smallest unit
	return minor.IntPart()
}

// CrossRate returns the from->to rate when only the legs through the base
// currency are known: usdPerFrom is "USD per one from unit" and toPerUSD is
// "to units per one USD". The product is "to units per one from unit".
func CrossRate(usdPerFrom, toPerUSD decimal.Decimal) decimal.Decimal {
	return usdPerFrom.Mul(toPerUSD)
}

// SafeCrossRate is CrossRate with a guard against a zero or negative leg, which
// would otherwise silently produce a zero rate.
func SafeCrossRate(usdPerFrom, toPerUSD decimal.Decimal) (decimal.Decimal, error) {
	if usdPerFrom.LessThanOrEqual(decimal.Zero) || toPerUSD.LessThanOrEqual(decimal.Zero) {
		return decimal.Decimal{}, errors.New("cross rate legs must be positive")
	}
	return CrossRate(usdPerFrom, toPerUSD), nil
}

// Reciprocal returns 1/rate, used to invert a stored base->quote rate into the
// quote->base direction. Panics-free: a non-positive rate returns zero so the
// caller's guards surface a clean error rather than a divide-by-zero.
func Reciprocal(rate decimal.Decimal) decimal.Decimal {
	if rate.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	// 28 digit precision is ample for currency reciprocals.
	return decimal.NewFromInt(1).DivRound(rate, 28)
}
