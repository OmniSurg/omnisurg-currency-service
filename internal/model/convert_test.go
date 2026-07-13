package model_test

import (
	"testing"

	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestConvertMinor(t *testing.T) {
	tests := []struct {
		name        string
		amountMinor int64
		fromDec     int32
		toDec       int32
		rate        string // quote (to) per one from unit
		wantMinor   int64
	}{
		{
			// Sheet 05 worked example: 2000 ZAR at 0.06128 USD per ZAR = 122.56 USD.
			name: "ZAR to USD worked example", amountMinor: 200000, fromDec: 2, toDec: 2,
			rate: "0.06128", wantMinor: 12256,
		},
		{
			// 1500 ZWG at 0.0373560 USD per ZWG = 56.034 -> rounds to 56.03 USD.
			name: "ZWG to USD rounds half away", amountMinor: 150000, fromDec: 2, toDec: 2,
			rate: "0.0373560", wantMinor: 5603,
		},
		{
			// 100.00 USD at 26.7692 ZWG per USD = 2676.92 ZWG.
			name: "USD to ZWG", amountMinor: 10000, fromDec: 2, toDec: 2,
			rate: "26.7692", wantMinor: 267692,
		},
		{
			// Identity rate is exactly 1.
			name: "same currency identity", amountMinor: 4299, fromDec: 2, toDec: 2,
			rate: "1", wantMinor: 4299,
		},
		{
			// Banker-style half-up: 0.005 USD at rate 1 -> but test true half:
			// 1 minor (0.01) * 0.5 = 0.005 -> rounds to 0.01 (half away from zero).
			name: "half rounds away from zero up", amountMinor: 1, fromDec: 2, toDec: 2,
			rate: "0.5", wantMinor: 1,
		},
		{
			// 3 minor (0.03) * 0.5 = 0.015 -> 0.02.
			name: "another half away", amountMinor: 3, fromDec: 2, toDec: 2,
			rate: "0.5", wantMinor: 2,
		},
		{
			name: "zero amount", amountMinor: 0, fromDec: 2, toDec: 2,
			rate: "26.7692", wantMinor: 0,
		},
		{
			// Float-trap: 0.1 + 0.2 style. 70 minor (0.70) * 1.1 = 0.77 exactly.
			name: "no float drift", amountMinor: 70, fromDec: 2, toDec: 2,
			rate: "1.1", wantMinor: 77,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := model.ConvertMinor(tc.amountMinor, tc.fromDec, tc.toDec, dec(tc.rate))
			assert.Equal(t, tc.wantMinor, got)
		})
	}
}

func TestCrossRate(t *testing.T) {
	// from->USD then USD->to. ZWG->ZAR via USD.
	// 1 ZWG = 0.037356 USD; 1 USD = 18 ZAR (seed). So 1 ZWG = 0.672408 ZAR.
	usdPerFrom := dec("0.037356") // USD per ZWG
	toPerUSD := dec("18")         // ZAR per USD
	got := model.CrossRate(usdPerFrom, toPerUSD)
	assert.True(t, got.Equal(dec("0.672408")), "got %s", got.String())
}

func TestCrossRateRejectsZero(t *testing.T) {
	_, err := model.SafeCrossRate(decimal.Zero, dec("18"))
	require.Error(t, err)
}
