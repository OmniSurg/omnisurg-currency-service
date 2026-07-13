package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BaseCurrency is the platform reporting currency. All KPIs roll up in USD per
// sheet 05_Multi_Currency.
const BaseCurrency = "USD"

// Currency is a platform-global active currency.
type Currency struct {
	Code     string
	Name     string
	Symbol   string
	IsBase   bool
	Decimals int32
	Active   bool
}

// FXSnapshot is one append-only exchange rate observation. Rate is "quote units
// per one base unit": for Base=USD Quote=ZWG a Rate of 26.7692 means
// 1 USD = 26.7692 ZWG. Snapshots are never overwritten (sheet 05 rule).
type FXSnapshot struct {
	ID         uuid.UUID
	Base       string
	Quote      string
	Rate       decimal.Decimal
	Source     string
	CapturedAt time.Time
	CreatedBy  *uuid.UUID
}

// NewSnapshot is the input to record a snapshot.
type NewSnapshot struct {
	Base      string
	Quote     string
	Rate      decimal.Decimal
	Source    string
	CreatedBy *uuid.UUID
}

// TenantFXConfig is a tenant's display currency and rate source preference.
type TenantFXConfig struct {
	TenantID        uuid.UUID
	DisplayCurrency string
	FXSource        string
	UpdatedAt       time.Time
}

// ConfigUpdate carries the mutable fields of a tenant fx config.
type ConfigUpdate struct {
	DisplayCurrency *string
	FXSource        *string
}

// Conversion is the result of converting an amount between two currencies.
type Conversion struct {
	FromCurrency string
	ToCurrency   string
	AmountMinor  int64
	// ConvertedMinor is the result in the smallest unit of ToCurrency.
	ConvertedMinor int64
	// Rate is the effective from->to rate applied (quote per one from unit).
	Rate decimal.Decimal
	// CapturedAt is the snapshot time of the rate(s) used; for a cross rate it
	// is the older of the two legs so callers know the staleness bound.
	CapturedAt time.Time
}

// AuditEvent is the local audit emission shape.
type AuditEvent struct {
	TenantID   uuid.UUID
	ActorID    *uuid.UUID
	Action     string
	TargetType string
	TargetID   *uuid.UUID
	Detail     string
	RequestID  string
}

// AuditRow is a stored audit row, returned by the debug query.
type AuditRow struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	ActorID    *uuid.UUID
	Action     string
	TargetType string
	TargetID   *uuid.UUID
	Detail     string
	RequestID  string
	OccurredAt time.Time
}
