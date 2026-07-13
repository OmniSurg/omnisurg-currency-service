// Package model holds the currency service domain types and error sentinels.
package model

import (
	"net/http"

	apperr "github.com/OmniSurg/omnisurg-go-common/errors"
)

var (
	ErrCurrencyUnknown   = apperr.New("CURRENCY_UNKNOWN", "currency code is not active on this platform", http.StatusUnprocessableEntity)
	ErrRateNotFound      = apperr.New("CURRENCY_RATE_NOT_FOUND", "no exchange rate snapshot exists for this currency pair", http.StatusNotFound)
	ErrTenantConfigEmpty = apperr.New("CURRENCY_CONFIG_NOT_FOUND", "no fx configuration exists for this tenant", http.StatusNotFound)
	ErrTenantMissing     = apperr.New("AUTH_TENANT_MISSING", "tenant context is required", http.StatusUnauthorized)
	ErrValidation        = apperr.New("VALIDATION_FAILED", "the request body is invalid", http.StatusUnprocessableEntity)
	ErrRateSourceDown    = apperr.New("CURRENCY_SOURCE_UNAVAILABLE", "the upstream rate source could not be reached", http.StatusBadGateway)
)
