package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/rs/zerolog/log"
)

// validSources are the allowed fx_source values (sheet 05: ZimRate feed or
// manual admin override).
var validSources = map[string]struct{}{"zimrate": {}, "manual": {}}

// ConfigService owns per-tenant FX configuration.
type ConfigService struct {
	store      ConfigStore
	currencies CurrencyStore
	audit      AuditEmitter
}

// NewConfigService builds a ConfigService.
func NewConfigService(store ConfigStore, currencies CurrencyStore, audit AuditEmitter) *ConfigService {
	return &ConfigService{store: store, currencies: currencies, audit: audit}
}

// Get returns the caller tenant's config, synthesising the platform default
// (display ZWG, source zimrate) when none is stored yet so the read never 404s
// for a real tenant. The default mirrors the sheet 05 System FX Source default.
func (s *ConfigService) Get(ctx context.Context, caller Caller) (model.TenantFXConfig, error) {
	cfg, err := s.store.Get(ctx, caller.TenantID)
	if errors.Is(err, model.ErrTenantConfigEmpty) {
		return model.TenantFXConfig{TenantID: caller.TenantID, DisplayCurrency: "ZWG", FXSource: "zimrate"}, nil
	}
	if err != nil {
		return model.TenantFXConfig{}, err
	}
	return cfg, nil
}

// Update validates and upserts the caller tenant's config, then audits.
func (s *ConfigService) Update(ctx context.Context, caller Caller, upd model.ConfigUpdate) (model.TenantFXConfig, error) {
	current, err := s.Get(ctx, caller)
	if err != nil {
		return model.TenantFXConfig{}, err
	}
	display := current.DisplayCurrency
	source := current.FXSource
	if upd.DisplayCurrency != nil {
		display = strings.ToUpper(strings.TrimSpace(*upd.DisplayCurrency))
	}
	if upd.FXSource != nil {
		source = strings.ToLower(strings.TrimSpace(*upd.FXSource))
	}
	if _, cerr := s.currencies.Get(ctx, display); cerr != nil {
		return model.TenantFXConfig{}, cerr
	}
	if _, ok := validSources[source]; !ok {
		return model.TenantFXConfig{}, model.ErrValidation.WithDetails([]map[string]string{{"field": "fx_source", "issue": "must be zimrate or manual"}})
	}
	out, err := s.store.Upsert(ctx, caller.TenantID, display, source)
	if err != nil {
		return model.TenantFXConfig{}, err
	}
	if s.audit != nil {
		actor := caller.UserID
		if aerr := s.audit.Emit(ctx, model.AuditEvent{
			TenantID:   caller.TenantID,
			ActorID:    &actor,
			Action:     "currency.config.update",
			TargetType: "tenant_fx_config",
			Detail:     fmt.Sprintf("display=%s source=%s", display, source),
			RequestID:  caller.RequestID,
		}); aerr != nil {
			log.Error().Err(aerr).Str("action", "currency.config.update").Msg("audit emit failed")
		}
	}
	return out, nil
}
