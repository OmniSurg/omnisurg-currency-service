package handler

import (
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
)

type rateJSON struct {
	ID         string    `json:"id,omitempty"`
	Base       string    `json:"base"`
	Quote      string    `json:"quote"`
	Rate       string    `json:"rate"`
	Source     string    `json:"source"`
	CapturedAt time.Time `json:"captured_at"`
}

func presentRate(s model.FXSnapshot) rateJSON {
	id := ""
	if s.ID.String() != "00000000-0000-0000-0000-000000000000" {
		id = s.ID.String()
	}
	return rateJSON{
		ID: id, Base: s.Base, Quote: s.Quote, Rate: s.Rate.String(),
		Source: s.Source, CapturedAt: s.CapturedAt,
	}
}

type conversionJSON struct {
	From           string    `json:"from"`
	To             string    `json:"to"`
	AmountMinor    int64     `json:"amount_minor"`
	ConvertedMinor int64     `json:"converted_minor"`
	Rate           string    `json:"rate"`
	CapturedAt     time.Time `json:"captured_at"`
}

func presentConversion(c model.Conversion) conversionJSON {
	return conversionJSON{
		From: c.FromCurrency, To: c.ToCurrency, AmountMinor: c.AmountMinor,
		ConvertedMinor: c.ConvertedMinor, Rate: c.Rate.String(), CapturedAt: c.CapturedAt,
	}
}

type configJSON struct {
	TenantID        string    `json:"tenant_id"`
	BaseCurrency    string    `json:"base_currency"`
	DisplayCurrency string    `json:"display_currency"`
	FXSource        string    `json:"fx_source"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func presentConfig(c model.TenantFXConfig) configJSON {
	return configJSON{
		TenantID: c.TenantID.String(), BaseCurrency: model.BaseCurrency,
		DisplayCurrency: c.DisplayCurrency, FXSource: c.FXSource, UpdatedAt: c.UpdatedAt,
	}
}
