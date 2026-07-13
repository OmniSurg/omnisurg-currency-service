package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/OmniSurg/omnisurg-currency-service/internal/db"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CurrencyRepository reads the platform-global currency reference table. No RLS,
// runs on the bare pool.
type CurrencyRepository struct {
	q *db.Queries
}

// NewCurrencyRepository builds a CurrencyRepository.
func NewCurrencyRepository(pool *pgxpool.Pool) *CurrencyRepository {
	return &CurrencyRepository{q: db.New(pool)}
}

// Get returns one currency, mapping no rows to model.ErrCurrencyUnknown.
func (r *CurrencyRepository) Get(ctx context.Context, code string) (model.Currency, error) {
	row, err := r.q.GetCurrency(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Currency{}, model.ErrCurrencyUnknown
	}
	if err != nil {
		return model.Currency{}, fmt.Errorf("get currency: %w", err)
	}
	return model.Currency{
		Code: row.Code, Name: row.Name, Symbol: row.Symbol,
		IsBase: row.IsBase, Decimals: int32(row.Decimals), Active: row.Active,
	}, nil
}

// ListActive returns the active currencies, base first.
func (r *CurrencyRepository) ListActive(ctx context.Context) ([]model.Currency, error) {
	rows, err := r.q.ListActiveCurrencies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active currencies: %w", err)
	}
	out := make([]model.Currency, 0, len(rows))
	for _, row := range rows {
		out = append(out, model.Currency{
			Code: row.Code, Name: row.Name, Symbol: row.Symbol,
			IsBase: row.IsBase, Decimals: int32(row.Decimals), Active: row.Active,
		})
	}
	return out, nil
}
