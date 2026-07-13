-- queries/tenant_config.sql
-- tenant_fx_config is tenant scoped (RLS via WithTenant).

-- name: GetTenantConfig :one
SELECT tenant_id, display_currency, fx_source, updated_at
FROM tenant_fx_config
WHERE tenant_id = $1;

-- name: UpsertTenantConfig :one
INSERT INTO tenant_fx_config (tenant_id, display_currency, fx_source)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id) DO UPDATE
SET display_currency = EXCLUDED.display_currency,
    fx_source        = EXCLUDED.fx_source,
    updated_at       = now()
RETURNING tenant_id, display_currency, fx_source, updated_at;
