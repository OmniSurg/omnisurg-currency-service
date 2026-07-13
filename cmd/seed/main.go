// Command seed records the current Zimbabwe FX snapshots and the Acme tenant
// fx config so the local stack and CST have realistic data. Idempotent: a seed
// snapshot is inserted only once per pair (SeedSnapshot guards on source=seed),
// and the tenant config upsert is naturally idempotent.
//
// Rates are "quote units per one base unit" and reflect the RBZ official
// interbank averages from sheet 05_Multi_Currency (as at 12 Jun 2026):
//
//	USD/ZWG = 26.7692  (1 USD = 26.7692 ZWG)
//	ZAR/USD = 0.06128  (1 ZAR = 0.06128 USD, sheet worked example factor)
//
// One leg is seeded per pair. The service derives the inverse via Reciprocal
// (USD/ZAR = 1/0.06128 = 16.3185) and a cross rate through USD when neither
// side is USD, so every ZAR<->USD path resolves from the single 0.06128 factor.
// Seeding both legs with independently rounded values would make a direct
// ZAR->USD convert and a USD cross disagree, so only one leg is stored.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OmniSurg/omnisurg-currency-service/internal/config"
	"github.com/OmniSurg/omnisurg-currency-service/internal/db"
	pg "github.com/OmniSurg/omnisurg-go-common/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

// tenantAcme matches identity-service and tenant-service so the cross service
// flow resolves to the same tenant.
var tenantAcme = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func pgID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("seed complete")
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pg.OpenPool(ctx, pg.Options{DSN: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer pool.Close()

	q := db.New(pool)
	// fx_snapshots is platform-global (no RLS): seed on the bare pool.
	// One authoritative leg per pair (sheet 05). The service derives the inverse
	// leg via Reciprocal and any cross rate through USD, so ZAR<->USD round trips
	// and the USD cross stay internally consistent off the single 0.06128 factor.
	pairs := []struct {
		base, quote, rate string
	}{
		{"USD", "ZWG", "26.7692"},
		{"ZAR", "USD", "0.06128"},
	}
	for _, p := range pairs {
		if serr := q.SeedSnapshot(ctx, db.SeedSnapshotParams{
			BaseCurrency:  p.base,
			QuoteCurrency: p.quote,
			Rate:          dec(p.rate),
		}); serr != nil {
			return fmt.Errorf("seed snapshot %s/%s: %w", p.base, p.quote, serr)
		}
	}
	fmt.Println("fx snapshots seeded")

	// tenant_fx_config is tenant scoped (RLS): run under WithTenant.
	err = pg.WithTenant(ctx, pool, tenantAcme.String(), func(ctx context.Context, conn pg.Conn) error {
		if _, uerr := db.New(conn).UpsertTenantConfig(ctx, db.UpsertTenantConfigParams{
			TenantID:        pgID(tenantAcme),
			DisplayCurrency: "ZWG",
			FxSource:        "zimrate",
		}); uerr != nil {
			return fmt.Errorf("seed acme fx config: %w", uerr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("acme fx config seeded")
	return nil
}
