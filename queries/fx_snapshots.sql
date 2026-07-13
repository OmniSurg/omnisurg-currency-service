-- queries/fx_snapshots.sql
-- fx_snapshots is platform-global (no RLS), append only. Runs on the bare pool.

-- name: InsertSnapshot :one
INSERT INTO fx_snapshots (base_currency, quote_currency, rate, source, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, base_currency, quote_currency, rate, source, captured_at, created_by;

-- name: GetLatestSnapshot :one
SELECT id, base_currency, quote_currency, rate, source, captured_at, created_by
FROM fx_snapshots
WHERE base_currency = $1 AND quote_currency = $2
ORDER BY captured_at DESC
LIMIT 1;

-- name: SeedSnapshot :exec
INSERT INTO fx_snapshots (base_currency, quote_currency, rate, source)
SELECT $1, $2, $3, 'seed'
WHERE NOT EXISTS (
    SELECT 1 FROM fx_snapshots
    WHERE base_currency = $1 AND quote_currency = $2 AND source = 'seed'
);
