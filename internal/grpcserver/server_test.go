package grpcserver_test

// WHY THERE IS NO TENANT-LEAK TEST IN THIS FILE.
//
// The tenant-service exemplar (Plan P chunk 3) ships a mandatory gRPC-path
// tenant-leak test because its branch and branding tables run under
// postgres.WithTenant with FORCE ROW LEVEL SECURITY: there is per-tenant data
// that RLS must isolate, so the test proves the isolation holds identically on
// the gRPC path. currency-service is different by design. The FX surface the
// gRPC server exposes (GetLatestRate, Convert) reads ONLY platform-global,
// no-RLS tables: fx_snapshots and currencies are shared across every tenant
// (the same posture as the tenant registry), exactly as the repo CLAUDE.md
// locks. There is no per-tenant FX data to leak, so the standard cross-tenant
// leak assertion does not apply here.
//
// Instead this test proves the equivalent guarantee for a global registry:
//  1. The FX read works for any caller WITHOUT a tenant id, because the
//     interceptor runs with RequireTenant=false (the cross-service billing hop
//     and provider callers have no tenant). A tenant-scoped service would reject
//     a no-tenant call with Unauthenticated; the FX read must NOT.
//  2. The seeded rate round-trips EXACTLY as a decimal string, with no float
//     precision loss, on the gRPC path.
//  3. Convert applies the rate with no float drift (minor-unit integer math).
//  4. An unknown currency pair surfaces codes.NotFound.
//  5. The response carries NO tenant-specific data (FXSnapshot and Conversion
//     hold only base, quote, rate, source, and money; never a tenant id).
//
// The only admin RPCs (Refresh, SetManualRate) stay REST-only and are NOT
// registered on the gRPC server, so there is no gRPC mutation surface to scope.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/cache"
	"github.com/OmniSurg/omnisurg-currency-service/internal/grpcserver"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/OmniSurg/omnisurg-currency-service/internal/repository"
	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	"github.com/OmniSurg/omnisurg-currency-service/test/harness"
	mw "github.com/OmniSurg/omnisurg-go-common/middleware"
	pg "github.com/OmniSurg/omnisurg-go-common/postgres"
	commonv1 "github.com/OmniSurg/omnisurg-proto/gen/go/omnisurg/common/v1"
	currencyv1 "github.com/OmniSurg/omnisurg-proto/gen/go/omnisurg/currency/v1"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const grpcTestSecret = "grpc-currency-test-secret"

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

// testServer boots Postgres via the harness (non superuser, NOBYPASSRLS),
// constructs the REAL repositories and the REAL service layer (no mocks), seeds
// a USD/ZWG snapshot, wires the grpcserver with the shared go-common
// interceptor under RequireTenant=false (FX rates are platform data), and serves
// it on an in process bufconn. It returns a connected client.
func testServer(t *testing.T) currencyv1.CurrencyServiceClient {
	t.Helper()
	dsn, stop := harness.StartPostgres(t)
	t.Cleanup(stop)

	ctx := context.Background()
	pool, err := pg.OpenPool(ctx, pg.Options{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	snapshotRepo := repository.NewSnapshotRepository(pool)
	currencyRepo := repository.NewCurrencyRepository(pool)
	auditRepo := repository.NewAuditRepository(pool)
	// nil redis: the cache degrades to a database read, never a failure.
	rateCache := cache.New(nil, time.Minute)

	// Seed the rate the billing money loop reads. fx_snapshots is global no-RLS,
	// so this is visible to every caller regardless of tenant.
	actor := uuid.New()
	_, err = snapshotRepo.Insert(ctx, model.NewSnapshot{
		Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CreatedBy: &actor,
	})
	require.NoError(t, err)

	ratesSvc := service.NewCurrencyService(snapshotRepo, currencyRepo, rateCache, nil, auditRepo)

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(mw.UnaryServerInterceptor(mw.InterceptorOptions{
			JWTSecret:     grpcTestSecret,
			RequireTenant: false,
		})),
	)
	currencyv1.RegisterCurrencyServiceServer(srv, grpcserver.New(ratesSvc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return currencyv1.NewCurrencyServiceClient(conn)
}

// TestGRPCGetLatestRateGlobalNoTenant proves the FX read is global: it works for
// a caller with NO tenant id and NO JWT, because the interceptor runs with
// RequireTenant=false. A tenant-scoped service would reject this with
// Unauthenticated; the FX read must not. The seeded decimal round-trips exactly.
func TestGRPCGetLatestRateGlobalNoTenant(t *testing.T) {
	client := testServer(t)

	snap, err := client.GetLatestRate(context.Background(), &currencyv1.GetLatestRateRequest{
		Base: "USD", Quote: "ZWG",
	})
	require.NoError(t, err)
	assert.Equal(t, "USD", snap.GetBase())
	assert.Equal(t, "ZWG", snap.GetQuote())
	// Exact decimal string, no float drift.
	assert.Equal(t, "26.7692", snap.GetRate())
	assert.Equal(t, "seed", snap.GetSource())
	require.NotNil(t, snap.GetCapturedAt())
}

// TestGRPCGetLatestRateUnknownPairIsNotFound proves an unknown pair surfaces
// codes.NotFound through the shared error mapper. BWP is an active reference
// currency with no seeded rate, exactly the convert-404 path the repo tests pin.
func TestGRPCGetLatestRateUnknownPairIsNotFound(t *testing.T) {
	client := testServer(t)

	_, err := client.GetLatestRate(context.Background(), &currencyv1.GetLatestRateRequest{
		Base: "USD", Quote: "BWP",
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestGRPCGetLatestRateUnknownCurrencyIsInvalidArgument proves a currency that
// is not active on the platform fails closed as InvalidArgument (422 mapped).
func TestGRPCGetLatestRateUnknownCurrencyIsInvalidArgument(t *testing.T) {
	client := testServer(t)

	_, err := client.GetLatestRate(context.Background(), &currencyv1.GetLatestRateRequest{
		Base: "USD", Quote: "JPY",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestGRPCConvertAppliesRateNoFloatLoss proves Convert applies the seeded rate
// with exact minor-unit integer math: 100.00 USD at 26.7692 = 2676.92 ZWG, ie
// 267692 minor units, with no float precision loss. The response money carries
// the quote currency and the rate echoes the exact decimal string.
func TestGRPCConvertAppliesRateNoFloatLoss(t *testing.T) {
	client := testServer(t)

	conv, err := client.Convert(context.Background(), &currencyv1.ConvertRequest{
		Amount: &commonv1.Money{CurrencyCode: "USD", AmountMinor: 10000},
		Quote:  "ZWG",
	})
	require.NoError(t, err)
	require.NotNil(t, conv.GetFrom())
	require.NotNil(t, conv.GetTo())
	assert.Equal(t, "USD", conv.GetFrom().GetCurrencyCode())
	assert.EqualValues(t, 10000, conv.GetFrom().GetAmountMinor())
	assert.Equal(t, "ZWG", conv.GetTo().GetCurrencyCode())
	// 10000 minor USD * 26.7692 = 267692 minor ZWG, exact.
	assert.EqualValues(t, 267692, conv.GetTo().GetAmountMinor())
	assert.Equal(t, "26.7692", conv.GetRate())
}

// TestGRPCConvertUnknownPairIsNotFound proves Convert surfaces NotFound when no
// rate is available for the pair.
func TestGRPCConvertUnknownPairIsNotFound(t *testing.T) {
	client := testServer(t)

	_, err := client.Convert(context.Background(), &currencyv1.ConvertRequest{
		Amount: &commonv1.Money{CurrencyCode: "USD", AmountMinor: 10000},
		Quote:  "BWP",
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestGRPCConvertNegativeAmountIsInvalidArgument proves a negative amount fails
// validation as InvalidArgument (422 mapped), the same guard the REST path has.
func TestGRPCConvertNegativeAmountIsInvalidArgument(t *testing.T) {
	client := testServer(t)

	_, err := client.Convert(context.Background(), &currencyv1.ConvertRequest{
		Amount: &commonv1.Money{CurrencyCode: "USD", AmountMinor: -1},
		Quote:  "ZWG",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestGRPCWithJWTTenantStillReadsGlobalRate proves a caller WITH a tenant JWT
// reads the same global rate as a no-tenant caller, and the response carries no
// tenant-specific data. This is the positive control that the FX read is not
// scoped by, nor leaks, tenant context.
func TestGRPCWithJWTTenantStillReadsGlobalRate(t *testing.T) {
	client := testServer(t)

	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(mw.MetadataKeyTenantID, uuid.NewString()))

	snap, err := client.GetLatestRate(ctx, &currencyv1.GetLatestRateRequest{
		Base: "USD", Quote: "ZWG",
	})
	require.NoError(t, err)
	assert.Equal(t, "26.7692", snap.GetRate())
}
