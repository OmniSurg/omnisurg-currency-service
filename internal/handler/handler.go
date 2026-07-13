package handler

import (
	"context"
	"net/http"
	"strings"

	api "github.com/OmniSurg/omnisurg-currency-service/internal/generated/api"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	mw "github.com/OmniSurg/omnisurg-go-common/middleware"
	"github.com/OmniSurg/omnisurg-go-common/tenant"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// AuditQuerier reads audit rows for the non production debug endpoint.
type AuditQuerier interface {
	Query(ctx context.Context, tenantID uuid.UUID, action string, actorID *uuid.UUID) ([]model.AuditRow, error)
}

// Handler implements api.ServerInterface.
type Handler struct {
	rates  *service.CurrencyService
	config *service.ConfigService
	audit  AuditQuerier
	ping   func(context.Context) error
}

var _ api.ServerInterface = (*Handler)(nil)

type convertRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	From        string `json:"from"`
	To          string `json:"to"`
}
type refreshRequest struct {
	Base  string `json:"base"`
	Quote string `json:"quote"`
}
type manualRateRequest struct {
	Base  string `json:"base"`
	Quote string `json:"quote"`
	Rate  string `json:"rate"`
}
type updateConfigRequest struct {
	DisplayCurrency *string `json:"display_currency"`
	FXSource        *string `json:"fx_source"`
}

// GetHealth pings the database and returns the service status.
func (h *Handler) GetHealth(c *gin.Context) {
	if h.ping != nil {
		if err := h.ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false,
				"error": gin.H{"code": "DEPENDENCY_DOWN", "message": "database is not reachable"}})
			return
		}
	}
	respondSuccess(c, http.StatusOK, gin.H{"status": "ok", "service": "currency-service"})
}

// GetLatestRate returns the most recent rate for the base/quote pair.
func (h *Handler) GetLatestRate(c *gin.Context, params api.GetLatestRateParams) {
	base := normCode(params.Base)
	quote := normCode(params.Quote)
	snap, err := h.rates.GetLatestRate(c.Request.Context(), base, quote)
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusOK, presentRate(snap))
}

// ConvertAmount converts a minor unit amount between two currencies.
func (h *Handler) ConvertAmount(c *gin.Context) {
	var req convertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, model.ErrValidation.WithDetails([]map[string]string{{"field": "body", "issue": "malformed json"}}))
		return
	}
	out, err := h.rates.Convert(c.Request.Context(), req.AmountMinor, normCode(req.From), normCode(req.To))
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusOK, presentConversion(out))
}

// RefreshRates pulls the latest rate from the upstream source (provider only).
func (h *Handler) RefreshRates(c *gin.Context) {
	caller, ok := callerFrom(c)
	if !ok {
		respondError(c, model.ErrTenantMissing)
		return
	}
	var req refreshRequest
	// Body is optional; ignore bind errors and fall back to defaults.
	_ = c.ShouldBindJSON(&req)
	snap, err := h.rates.Refresh(c.Request.Context(), caller, normCode(req.Base), normCode(req.Quote))
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusCreated, presentRate(snap))
}

// SetManualRate records a manual override rate (practice_admin; router gated).
func (h *Handler) SetManualRate(c *gin.Context) {
	caller, ok := callerFrom(c)
	if !ok {
		respondError(c, model.ErrTenantMissing)
		return
	}
	var req manualRateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, model.ErrValidation.WithDetails([]map[string]string{{"field": "body", "issue": "malformed json"}}))
		return
	}
	rate, derr := decimal.NewFromString(strings.TrimSpace(req.Rate))
	if derr != nil {
		respondError(c, model.ErrValidation.WithDetails([]map[string]string{{"field": "rate", "issue": "must be a decimal string"}}))
		return
	}
	snap, err := h.rates.SetManualRate(c.Request.Context(), caller, normCode(req.Base), normCode(req.Quote), rate)
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusCreated, presentRate(snap))
}

// GetTenantConfig returns the caller tenant's FX configuration.
func (h *Handler) GetTenantConfig(c *gin.Context) {
	caller, ok := callerFrom(c)
	if !ok {
		respondError(c, model.ErrTenantMissing)
		return
	}
	cfg, err := h.config.Get(c.Request.Context(), caller)
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusOK, presentConfig(cfg))
}

// UpdateTenantConfig updates the caller tenant's FX configuration (practice_admin).
func (h *Handler) UpdateTenantConfig(c *gin.Context) {
	caller, ok := callerFrom(c)
	if !ok {
		respondError(c, model.ErrTenantMissing)
		return
	}
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, model.ErrValidation.WithDetails([]map[string]string{{"field": "body", "issue": "malformed json"}}))
		return
	}
	cfg, err := h.config.Update(c.Request.Context(), caller, model.ConfigUpdate{
		DisplayCurrency: req.DisplayCurrency,
		FXSource:        req.FXSource,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusOK, presentConfig(cfg))
}

func normCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// callerFrom builds a service.Caller from the JWT identity. TenantID is optional
// (provider callers have none).
func callerFrom(c *gin.Context) (service.Caller, bool) {
	id, ok := tenant.Get(c)
	if !ok {
		return service.Caller{}, false
	}
	uid, err := uuid.Parse(id.UserID)
	if err != nil {
		return service.Caller{}, false
	}
	var tid uuid.UUID
	if id.TenantID != "" {
		if parsed, perr := uuid.Parse(id.TenantID); perr == nil {
			tid = parsed
		}
	}
	return service.Caller{
		UserID: uid, TenantID: tid, Role: id.Role, ProviderRole: id.ProviderRole,
		RequestID: c.GetString(mw.RequestIDKey),
	}, true
}
