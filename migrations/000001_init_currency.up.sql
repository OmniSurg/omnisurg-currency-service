-- currencies is the platform-global reference table of active currencies.
-- USD is the base/reporting currency (sheet 05_Multi_Currency: "Base/reporting
-- currency is USD; all KPIs roll up in USD"). It has NO RLS: the currency list
-- is the same across every tenant, configured by the platform.
CREATE TABLE currencies (
    code       TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    symbol     TEXT NOT NULL,
    is_base    BOOLEAN NOT NULL DEFAULT FALSE,
    decimals   SMALLINT NOT NULL DEFAULT 2,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Exactly one base currency may exist.
CREATE UNIQUE INDEX currencies_single_base ON currencies (is_base) WHERE is_base;

-- fx_snapshots is the append-only history of exchange rates. It is
-- PLATFORM-GLOBAL with NO app.tenant_id RLS, exactly like the tenant registry:
-- rates are sourced once (RBZ via ZimRate) and shared across every tenant. The
-- sheet rule "historical rates are never overwritten (audit)" is enforced by
-- never UPDATE-ing or DELETE-ing a row; each refresh inserts a new snapshot.
--
-- rate is "quote units per one base unit": for base=USD quote=ZWG a rate of
-- 26.7692 means 1 USD = 26.7692 ZWG. NUMERIC(20,10) keeps ten fractional digits
-- so reciprocal conversions stay exact to the cent after rounding.
CREATE TABLE fx_snapshots (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    base_currency  TEXT NOT NULL REFERENCES currencies (code),
    quote_currency TEXT NOT NULL REFERENCES currencies (code),
    rate           NUMERIC(20,10) NOT NULL CHECK (rate > 0),
    source         TEXT NOT NULL CHECK (source IN ('zimrate', 'manual', 'seed')),
    captured_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by     UUID
);
-- The latest-rate read filters by pair and orders by captured_at DESC; this
-- index serves it directly.
CREATE INDEX fx_snapshots_pair_captured_idx
    ON fx_snapshots (base_currency, quote_currency, captured_at DESC);

-- tenant_fx_config is the per-tenant display currency and FX source preference
-- (sheet 05: "currencies, the FX default and the FX source are configured per
-- tenant"). It IS tenant scoped, so it carries app.tenant_id RLS. base_currency
-- stays USD platform wide for reporting; display_currency is what the practice
-- shows to patients.
CREATE TABLE tenant_fx_config (
    tenant_id        UUID PRIMARY KEY,
    display_currency TEXT NOT NULL DEFAULT 'ZWG' REFERENCES currencies (code),
    fx_source        TEXT NOT NULL DEFAULT 'zimrate' CHECK (fx_source IN ('zimrate', 'manual')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE tenant_fx_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_fx_config FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_fx_config_tenant_scope ON tenant_fx_config
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- audit_log is the Phase 1 local stand-in for omnisurg-audit-service. The sheet
-- requires manual rate overrides be "logged with user & timestamp"; that write
-- lands here. Plan F swaps this for the audit-service gRPC client.
CREATE TABLE audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    actor_id    UUID,
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   UUID,
    detail      TEXT NOT NULL DEFAULT '',
    request_id  TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_lookup_idx ON audit_log (tenant_id, action, actor_id);
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_log_tenant_scope ON audit_log
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- Seed the three Phase 1 currencies. USD is base. Decimals per sheet 05.
INSERT INTO currencies (code, name, symbol, is_base, decimals) VALUES
    ('USD', 'US Dollar', '$', TRUE, 2),
    ('ZWG', 'Zimbabwe Gold', 'ZiG', FALSE, 2),
    ('ZAR', 'South African Rand', 'R', FALSE, 2)
ON CONFLICT (code) DO NOTHING;
