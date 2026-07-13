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

// SnapshotRepository persists and reads fx_snapshots. The table is
// platform-global with no RLS (rates are shared across tenants), so it runs on
// the bare pool.
type SnapshotRepository struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// NewSnapshotRepository builds a SnapshotRepository.
func NewSnapshotRepository(pool *pgxpool.Pool) *SnapshotRepository {
	return &SnapshotRepository{pool: pool, q: db.New(pool)}
}

// Insert appends a new immutable snapshot and returns it.
func (r *SnapshotRepository) Insert(ctx context.Context, in model.NewSnapshot) (model.FXSnapshot, error) {
	row, err := r.q.InsertSnapshot(ctx, db.InsertSnapshotParams{
		BaseCurrency:  in.Base,
		QuoteCurrency: in.Quote,
		Rate:          in.Rate,
		Source:        in.Source,
		CreatedBy:     pgUUIDPtr(in.CreatedBy),
	})
	if err != nil {
		return model.FXSnapshot{}, fmt.Errorf("insert snapshot: %w", err)
	}
	return snapshotToDomain(row), nil
}

// GetLatest returns the most recent snapshot for base->quote, mapping no rows
// to model.ErrRateNotFound.
func (r *SnapshotRepository) GetLatest(ctx context.Context, base, quote string) (model.FXSnapshot, error) {
	row, err := r.q.GetLatestSnapshot(ctx, db.GetLatestSnapshotParams{
		BaseCurrency:  base,
		QuoteCurrency: quote,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return model.FXSnapshot{}, model.ErrRateNotFound
	}
	if err != nil {
		return model.FXSnapshot{}, fmt.Errorf("get latest snapshot: %w", err)
	}
	return snapshotToDomain(row), nil
}

func snapshotToDomain(row db.FxSnapshot) model.FXSnapshot {
	return model.FXSnapshot{
		ID:         fromPgUUID(row.ID),
		Base:       row.BaseCurrency,
		Quote:      row.QuoteCurrency,
		Rate:       row.Rate,
		Source:     row.Source,
		CapturedAt: row.CapturedAt.Time,
		CreatedBy:  fromPgUUIDPtr(row.CreatedBy),
	}
}
