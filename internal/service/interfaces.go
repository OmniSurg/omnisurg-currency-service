package service

import (
	"context"

	"github.com/OmniSurg/omnisurg-currency-service/internal/client"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/google/uuid"
)

// SnapshotStore reads and appends fx snapshots. The repository satisfies it.
type SnapshotStore interface {
	Insert(ctx context.Context, in model.NewSnapshot) (model.FXSnapshot, error)
	GetLatest(ctx context.Context, base, quote string) (model.FXSnapshot, error)
}

// CurrencyStore reads the currency reference table.
type CurrencyStore interface {
	Get(ctx context.Context, code string) (model.Currency, error)
}

// ConfigStore reads and writes per-tenant fx config.
type ConfigStore interface {
	Get(ctx context.Context, tenantID uuid.UUID) (model.TenantFXConfig, error)
	Upsert(ctx context.Context, tenantID uuid.UUID, displayCurrency, fxSource string) (model.TenantFXConfig, error)
}

// RateCacher is the latest-rate cache. The cache.RateCache satisfies it. Every
// method degrades gracefully so a cache outage never fails a request.
type RateCacher interface {
	Get(ctx context.Context, base, quote string) (model.FXSnapshot, bool)
	Set(ctx context.Context, s model.FXSnapshot)
	Invalidate(ctx context.Context, base, quote string)
}

// RateFetcher pulls a rate from the upstream source (ZimRate or its stub).
type RateFetcher interface {
	FetchRate(ctx context.Context, base, quote string) (client.RateQuote, error)
}

// AuditEmitter records audit events.
type AuditEmitter interface {
	Emit(ctx context.Context, ev model.AuditEvent) error
}

// Caller is the authenticated identity passed from the handler into the service.
type Caller struct {
	UserID       uuid.UUID
	TenantID     uuid.UUID
	Role         string
	ProviderRole string
	RequestID    string
}

var providerRoles = map[string]struct{}{
	model.RoleProviderSuperAdmin: {},
	model.RoleProviderSupport:    {},
	model.RoleProviderBilling:    {},
}

// IsProvider reports whether the caller holds any platform provider role.
func (c Caller) IsProvider() bool {
	if _, ok := providerRoles[c.ProviderRole]; ok {
		return true
	}
	_, ok := providerRoles[c.Role]
	return ok
}
