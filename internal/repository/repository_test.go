package repository_test

import (
	"context"
	"testing"

	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/OmniSurg/omnisurg-currency-service/internal/repository"
	"github.com/OmniSurg/omnisurg-currency-service/test/harness"
	pg "github.com/OmniSurg/omnisurg-go-common/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	tenantA = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tenantB = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, stop := harness.StartPostgres(t)
	t.Cleanup(stop)
	pool, err := pg.OpenPool(context.Background(), pg.Options{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestCurrencyReferenceSeeded(t *testing.T) {
	pool := newPool(t)
	repo := repository.NewCurrencyRepository(pool)
	ctx := context.Background()

	usd, err := repo.Get(ctx, "USD")
	require.NoError(t, err)
	assert.True(t, usd.IsBase)
	assert.EqualValues(t, 2, usd.Decimals)

	_, err = repo.Get(ctx, "JPY")
	assert.ErrorIs(t, err, model.ErrCurrencyUnknown)

	// USD, ZWG, ZAR carry rate coverage; BWP is the active reference currency
	// with no seeded rate (migration 000002) that drives the convert-404 path.
	all, err := repo.ListActive(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 4)
	assert.True(t, all[0].IsBase, "base currency sorts first")
}

func TestSnapshotInsertGetLatestAppendOnly(t *testing.T) {
	pool := newPool(t)
	repo := repository.NewSnapshotRepository(pool)
	ctx := context.Background()

	_, err := repo.GetLatest(ctx, "USD", "ZWG")
	assert.ErrorIs(t, err, model.ErrRateNotFound)

	actor := uuid.New()
	first, err := repo.Insert(ctx, model.NewSnapshot{Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CreatedBy: &actor})
	require.NoError(t, err)
	assert.True(t, first.Rate.Equal(dec("26.7692")))

	// A newer snapshot supersedes on read but does not overwrite history.
	second, err := repo.Insert(ctx, model.NewSnapshot{Base: "USD", Quote: "ZWG", Rate: dec("30.0"), Source: "manual", CreatedBy: &actor})
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)

	latest, err := repo.GetLatest(ctx, "USD", "ZWG")
	require.NoError(t, err)
	assert.True(t, latest.Rate.Equal(dec("30.0")))
	assert.Equal(t, "manual", latest.Source)
}

func TestSnapshotDecimalPrecisionRoundTrip(t *testing.T) {
	pool := newPool(t)
	repo := repository.NewSnapshotRepository(pool)
	ctx := context.Background()
	// Ten fractional digits survive the NUMERIC(20,10) round trip exactly.
	in := dec("0.0373560000")
	_, err := repo.Insert(ctx, model.NewSnapshot{Base: "ZWG", Quote: "USD", Rate: in, Source: "seed"})
	require.NoError(t, err)
	got, err := repo.GetLatest(ctx, "ZWG", "USD")
	require.NoError(t, err)
	assert.True(t, got.Rate.Equal(dec("0.037356")), "got %s", got.Rate)
}

func TestTenantConfigUpsertAndGet(t *testing.T) {
	pool := newPool(t)
	repo := repository.NewConfigRepository(pool)
	ctx := context.Background()

	_, err := repo.Get(ctx, tenantA)
	assert.ErrorIs(t, err, model.ErrTenantConfigEmpty)

	out, err := repo.Upsert(ctx, tenantA, "ZAR", "manual")
	require.NoError(t, err)
	assert.Equal(t, "ZAR", out.DisplayCurrency)

	got, err := repo.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, "manual", got.FXSource)

	// Upsert again updates in place.
	out2, err := repo.Upsert(ctx, tenantA, "USD", "zimrate")
	require.NoError(t, err)
	assert.Equal(t, "USD", out2.DisplayCurrency)
}

// TestTenantConfigIsolationLeak is the mandatory leak path test: tenant B
// cannot read tenant A's config.
func TestTenantConfigIsolationLeak(t *testing.T) {
	pool := newPool(t)
	repo := repository.NewConfigRepository(pool)
	ctx := context.Background()

	_, err := repo.Upsert(ctx, tenantA, "ZWG", "zimrate")
	require.NoError(t, err)

	// tenant B sees no config of its own and cannot read tenant A's row.
	_, err = repo.Get(ctx, tenantB)
	assert.ErrorIs(t, err, model.ErrTenantConfigEmpty)

	// tenant A still reads its own.
	got, err := repo.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, tenantA, got.TenantID)
}

func TestAuditEmitAndQueryTenantScoped(t *testing.T) {
	pool := newPool(t)
	repo := repository.NewAuditRepository(pool)
	ctx := context.Background()
	actor := uuid.New()

	require.NoError(t, repo.Emit(ctx, model.AuditEvent{
		TenantID: tenantA, ActorID: &actor, Action: "currency.config.update", Detail: "display=ZAR",
	}))

	rowsA, err := repo.Query(ctx, tenantA, "currency.config.update", nil)
	require.NoError(t, err)
	assert.Len(t, rowsA, 1)

	// tenant B cannot see tenant A's audit rows.
	rowsB, err := repo.Query(ctx, tenantB, "currency.config.update", nil)
	require.NoError(t, err)
	assert.Empty(t, rowsB)
}
