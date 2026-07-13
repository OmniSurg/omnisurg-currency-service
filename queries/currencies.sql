-- queries/currencies.sql
-- currencies is platform-global (no RLS), runs on the bare pool.

-- name: GetCurrency :one
SELECT code, name, symbol, is_base, decimals, active
FROM currencies
WHERE code = $1;

-- name: ListActiveCurrencies :many
SELECT code, name, symbol, is_base, decimals, active
FROM currencies
WHERE active
ORDER BY is_base DESC, code;
