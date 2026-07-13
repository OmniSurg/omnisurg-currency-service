-- BWP (Botswana Pula) is an active reference currency a Zimbabwe practice may
-- list, but no RBZ rate feed covers it yet, so no fx_snapshot is seeded for it.
-- This gives a known, active currency with no rate path: any rate lookup or
-- conversion involving BWP returns the documented CURRENCY_RATE_NOT_FOUND (404)
-- rather than CURRENCY_UNKNOWN (422), which the Contract Smoke Test exercises.
-- Idempotent so it is safe on databases that already applied 000001.
INSERT INTO currencies (code, name, symbol, is_base, decimals) VALUES
    ('BWP', 'Botswana Pula', 'P', FALSE, 2)
ON CONFLICT (code) DO NOTHING;
