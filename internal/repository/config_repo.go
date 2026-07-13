package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/OmniSurg/omnisurg-currency-service/internal/db"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	pg "github.com/OmniSurg/omnisurg-go-common/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConfigRepository persists per-tenant FX config. Tenant scoped (RLS via
// WithTenant).
type ConfigRepository struct {
	pool *pgxpool.Pool
}

// NewConfigRepository builds a ConfigRepository.
func NewConfigRepository(pool *pgxpool.Pool) *ConfigRepository {
	return &ConfigRepository{pool: pool}
}

// Get returns the tenant config, mapping no rows to model.ErrTenantConfigEmpty.
func (r *ConfigRepository) Get(ctx context.Context, tenantID uuid.UUID) (model.TenantFXConfig, error) {
	var out model.TenantFXConfig
	err := pg.WithTenant(ctx, r.pool, tenantID.String(), func(ctx context.Context, conn pg.Conn) error {
		row, qerr := db.New(conn).GetTenantConfig(ctx, pgUUID(tenantID))
		if errors.Is(qerr, pgx.ErrNoRows) {
			return model.ErrTenantConfigEmpty
		}
		if qerr != nil {
			return fmt.Errorf("get tenant config: %w", qerr)
		}
		out = model.TenantFXConfig{
			TenantID:        fromPgUUID(row.TenantID),
			DisplayCurrency: row.DisplayCurrency,
			FXSource:        row.FxSource,
			UpdatedAt:       row.UpdatedAt.Time,
		}
		return nil
	})
	if err != nil {
		return model.TenantFXConfig{}, err
	}
	return out, nil
}

// Upsert writes the tenant config and returns the stored row.
func (r *ConfigRepository) Upsert(ctx context.Context, tenantID uuid.UUID, displayCurrency, fxSource string) (model.TenantFXConfig, error) {
	var out model.TenantFXConfig
	err := pg.WithTenant(ctx, r.pool, tenantID.String(), func(ctx context.Context, conn pg.Conn) error {
		row, qerr := db.New(conn).UpsertTenantConfig(ctx, db.UpsertTenantConfigParams{
			TenantID:        pgUUID(tenantID),
			DisplayCurrency: displayCurrency,
			FxSource:        fxSource,
		})
		if qerr != nil {
			return fmt.Errorf("upsert tenant config: %w", qerr)
		}
		out = model.TenantFXConfig{
			TenantID:        fromPgUUID(row.TenantID),
			DisplayCurrency: row.DisplayCurrency,
			FXSource:        row.FxSource,
			UpdatedAt:       row.UpdatedAt.Time,
		}
		return nil
	})
	if err != nil {
		return model.TenantFXConfig{}, err
	}
	return out, nil
}
