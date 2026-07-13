package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/client"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

// --- mocks ---

type fakeSnapshots struct {
	// pairs maps "BASE/QUOTE" -> snapshot.
	pairs    map[string]model.FXSnapshot
	inserted []model.NewSnapshot
}

func (f *fakeSnapshots) GetLatest(_ context.Context, base, quote string) (model.FXSnapshot, error) {
	s, ok := f.pairs[base+"/"+quote]
	if !ok {
		return model.FXSnapshot{}, model.ErrRateNotFound
	}
	return s, nil
}

func (f *fakeSnapshots) Insert(_ context.Context, in model.NewSnapshot) (model.FXSnapshot, error) {
	f.inserted = append(f.inserted, in)
	return model.FXSnapshot{ID: uuid.New(), Base: in.Base, Quote: in.Quote, Rate: in.Rate, Source: in.Source, CapturedAt: time.Now()}, nil
}

type fakeCurrencies struct{ decimals map[string]int32 }

func (f *fakeCurrencies) Get(_ context.Context, code string) (model.Currency, error) {
	d, ok := f.decimals[code]
	if !ok {
		return model.Currency{}, model.ErrCurrencyUnknown
	}
	return model.Currency{Code: code, Decimals: d, Active: true, IsBase: code == model.BaseCurrency}, nil
}

type noopCache struct {
	getHit       *model.FXSnapshot
	setCalls     int
	invalidCalls int
}

func (c *noopCache) Get(_ context.Context, _, _ string) (model.FXSnapshot, bool) {
	if c.getHit != nil {
		return *c.getHit, true
	}
	return model.FXSnapshot{}, false
}
func (c *noopCache) Set(_ context.Context, _ model.FXSnapshot) { c.setCalls++ }
func (c *noopCache) Invalidate(_ context.Context, _, _ string) { c.invalidCalls++ }

type fakeFetcher struct {
	quote client.RateQuote
	err   error
}

func (f *fakeFetcher) FetchRate(_ context.Context, _, _ string) (client.RateQuote, error) {
	return f.quote, f.err
}

type fakeAudit struct{ events []model.AuditEvent }

func (a *fakeAudit) Emit(_ context.Context, ev model.AuditEvent) error {
	a.events = append(a.events, ev)
	return nil
}

func threeCurrencies() *fakeCurrencies {
	return &fakeCurrencies{decimals: map[string]int32{"USD": 2, "ZWG": 2, "ZAR": 2}}
}

func newSvc(snaps *fakeSnapshots, cur *fakeCurrencies, cache service.RateCacher, fetch service.RateFetcher, audit *fakeAudit) *service.CurrencyService {
	if cache == nil {
		cache = &noopCache{}
	}
	if audit == nil {
		audit = &fakeAudit{}
	}
	return service.NewCurrencyService(snaps, cur, cache, fetch, audit)
}

// --- latest rate ---

func TestGetLatestRateDirect(t *testing.T) {
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{
		"USD/ZWG": {Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CapturedAt: time.Now()},
	}}
	cache := &noopCache{}
	svc := newSvc(snaps, threeCurrencies(), cache, nil, nil)
	got, err := svc.GetLatestRate(context.Background(), "USD", "ZWG")
	require.NoError(t, err)
	assert.True(t, got.Rate.Equal(dec("26.7692")))
	assert.Equal(t, 1, cache.setCalls, "a db hit warms the cache")
}

func TestGetLatestRateReciprocal(t *testing.T) {
	// Only USD/ZWG stored; asking ZWG/USD returns the reciprocal.
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{
		"USD/ZWG": {Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CapturedAt: time.Now()},
	}}
	svc := newSvc(snaps, threeCurrencies(), nil, nil, nil)
	got, err := svc.GetLatestRate(context.Background(), "ZWG", "USD")
	require.NoError(t, err)
	// 1/26.7692 ~ 0.037356...
	assert.True(t, got.Rate.Round(6).Equal(dec("0.037356")), "got %s", got.Rate)
}

func TestGetLatestRateCacheHitSkipsDB(t *testing.T) {
	hit := model.FXSnapshot{Base: "USD", Quote: "ZWG", Rate: dec("99"), Source: "seed", CapturedAt: time.Now()}
	cache := &noopCache{getHit: &hit}
	// Empty snaps: if it read the db it would error.
	svc := newSvc(&fakeSnapshots{pairs: map[string]model.FXSnapshot{}}, threeCurrencies(), cache, nil, nil)
	got, err := svc.GetLatestRate(context.Background(), "USD", "ZWG")
	require.NoError(t, err)
	assert.True(t, got.Rate.Equal(dec("99")))
}

func TestGetLatestRateUnknownCurrency(t *testing.T) {
	svc := newSvc(&fakeSnapshots{}, threeCurrencies(), nil, nil, nil)
	_, err := svc.GetLatestRate(context.Background(), "USD", "XXX")
	assert.ErrorIs(t, err, model.ErrCurrencyUnknown)
}

func TestGetLatestRateNotFound(t *testing.T) {
	svc := newSvc(&fakeSnapshots{pairs: map[string]model.FXSnapshot{}}, threeCurrencies(), nil, nil, nil)
	_, err := svc.GetLatestRate(context.Background(), "USD", "ZAR")
	assert.ErrorIs(t, err, model.ErrRateNotFound)
}

// --- convert ---

func TestConvertSameCurrency(t *testing.T) {
	svc := newSvc(&fakeSnapshots{}, threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 4299, "USD", "USD")
	require.NoError(t, err)
	assert.EqualValues(t, 4299, out.ConvertedMinor)
	assert.True(t, out.Rate.Equal(dec("1")))
}

func TestConvertZARToUSD(t *testing.T) {
	// 2000.00 ZAR -> USD at reciprocal of USD/ZAR=18. 1/18 = 0.0555..; 200000 minor
	// * 0.0555.. = 11111.11 -> wait, use the sheet ZAR/USD direct factor instead.
	// Store ZAR/USD direct = 0.06128 (sheet). 2000 * 0.06128 = 122.56 -> 12256 minor.
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{
		"ZAR/USD": {Base: "ZAR", Quote: "USD", Rate: dec("0.06128"), Source: "seed", CapturedAt: time.Now()},
	}}
	svc := newSvc(snaps, threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 200000, "ZAR", "USD")
	require.NoError(t, err)
	assert.EqualValues(t, 12256, out.ConvertedMinor)
}

func TestConvertUSDToZWG(t *testing.T) {
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{
		"USD/ZWG": {Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CapturedAt: time.Now()},
	}}
	svc := newSvc(snaps, threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 10000, "USD", "ZWG") // 100.00 USD
	require.NoError(t, err)
	assert.EqualValues(t, 267692, out.ConvertedMinor) // 2676.92 ZWG
}

func TestConvertCrossThroughUSD(t *testing.T) {
	// Cross-rate math check with illustrative fixture rates (not the live seed,
	// which seeds only one leg per pair): ZWG -> ZAR with no direct pair, via USD.
	// 1 ZWG = 0.037356 USD (ZWG/USD); 1 USD = 18 ZAR (USD/ZAR fixture).
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{
		"ZWG/USD": {Base: "ZWG", Quote: "USD", Rate: dec("0.037356"), Source: "seed", CapturedAt: time.Now().Add(-2 * time.Hour)},
		"USD/ZAR": {Base: "USD", Quote: "ZAR", Rate: dec("18"), Source: "seed", CapturedAt: time.Now()},
	}}
	svc := newSvc(snaps, threeCurrencies(), nil, nil, nil)
	// 1000.00 ZWG -> ZAR. rate = 0.037356 * 18 = 0.672408. 1000 * 0.672408 = 672.408 -> 672.41 ZAR.
	out, err := svc.Convert(context.Background(), 100000, "ZWG", "ZAR")
	require.NoError(t, err)
	assert.EqualValues(t, 67241, out.ConvertedMinor)
	assert.True(t, out.Rate.Round(6).Equal(dec("0.672408")), "rate %s", out.Rate)
}

// liveSeedSnapshots returns the exact single-leg snapshots the production seed
// stores (cmd/seed): only USD/ZWG and ZAR/USD. Every other direction is derived
// by the service via Reciprocal or the USD cross, so these tests exercise the
// same derivation path the live stack uses, not the both-legs fake-store
// shortcut that hid the cross-rate bug.
func liveSeedSnapshots() *fakeSnapshots {
	return &fakeSnapshots{pairs: map[string]model.FXSnapshot{
		"USD/ZWG": {Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CapturedAt: time.Now()},
		"ZAR/USD": {Base: "ZAR", Quote: "USD", Rate: dec("0.06128"), Source: "seed", CapturedAt: time.Now()},
	}}
}

// The four conversion shapes, all driven off the live single-leg seed so the
// reciprocal and USD-cross derivation paths are the ones under test.

func TestConvertSameCurrencyLiveSeed(t *testing.T) {
	svc := newSvc(liveSeedSnapshots(), threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 4299, "USD", "USD")
	require.NoError(t, err)
	assert.EqualValues(t, 4299, out.ConvertedMinor)
	assert.True(t, out.Rate.Equal(dec("1")))
}

func TestConvertToUSDLiveSeed(t *testing.T) {
	// ZAR -> USD reads the direct ZAR/USD=0.06128 seed leg.
	// 2000.00 ZAR * 0.06128 = 122.56 USD = 12256 minor (sheet 05 worked example).
	svc := newSvc(liveSeedSnapshots(), threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 200000, "ZAR", "USD")
	require.NoError(t, err)
	assert.EqualValues(t, 12256, out.ConvertedMinor)
}

func TestConvertFromUSDLiveSeed(t *testing.T) {
	// USD -> ZWG reads the direct USD/ZWG=26.7692 seed leg.
	// 100.00 USD * 26.7692 = 2676.92 ZWG = 267692 minor.
	svc := newSvc(liveSeedSnapshots(), threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 10000, "USD", "ZWG")
	require.NoError(t, err)
	assert.EqualValues(t, 267692, out.ConvertedMinor)
}

func TestConvertFromUSDViaReciprocalLiveSeed(t *testing.T) {
	// USD -> ZAR has no direct leg; it derives from Reciprocal(ZAR/USD=0.06128)
	// = 16.318538 (USD/ZAR). 100.00 USD * 16.318538 = 1631.85 ZAR = 163185 minor.
	svc := newSvc(liveSeedSnapshots(), threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 10000, "USD", "ZAR")
	require.NoError(t, err)
	assert.EqualValues(t, 163185, out.ConvertedMinor)
}

func TestConvertCrossLiveSeed(t *testing.T) {
	// ZWG -> ZAR (neither side USD) composes through the USD pivot off the single
	// seed legs: (1/26.7692) USD/ZWG * (1/0.06128) ZAR/USD = 0.037356 * 16.318538
	// = 0.609601. 1000.00 ZWG * 0.609601 = 609.60 ZAR = 60960 minor.
	svc := newSvc(liveSeedSnapshots(), threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 100000, "ZWG", "ZAR")
	require.NoError(t, err)
	assert.EqualValues(t, 60960, out.ConvertedMinor)
	assert.True(t, out.Rate.Round(6).Equal(dec("0.609601")), "cross rate %s", out.Rate)
}

func TestConvertCrossIgnoresConflictingDirectInverseSnapshot(t *testing.T) {
	// Regression: an append-only ZAR/ZWG snapshot (e.g. a manual override of the
	// opposite direction) must NOT poison the ZWG -> ZAR cross. Inverting that
	// single direct cross rate (Reciprocal(16.387321) = 0.061022) would yield
	// 6102 minor, diverging from the USD-anchored value billing depends on. The
	// USD cross stays canonical: 60960 minor.
	snaps := liveSeedSnapshots()
	snaps.pairs["ZAR/ZWG"] = model.FXSnapshot{Base: "ZAR", Quote: "ZWG", Rate: dec("16.387321"), Source: "manual", CapturedAt: time.Now()}
	svc := newSvc(snaps, threeCurrencies(), nil, nil, nil)
	out, err := svc.Convert(context.Background(), 100000, "ZWG", "ZAR")
	require.NoError(t, err)
	assert.EqualValues(t, 60960, out.ConvertedMinor)
	assert.True(t, out.Rate.Round(6).Equal(dec("0.609601")), "cross rate %s", out.Rate)
}

func TestConvertNegativeAmountRejected(t *testing.T) {
	svc := newSvc(&fakeSnapshots{}, threeCurrencies(), nil, nil, nil)
	_, err := svc.Convert(context.Background(), -1, "USD", "ZWG")
	assert.ErrorIs(t, err, model.ErrValidation)
}

func TestConvertUnknownCurrency(t *testing.T) {
	svc := newSvc(&fakeSnapshots{}, threeCurrencies(), nil, nil, nil)
	_, err := svc.Convert(context.Background(), 100, "USD", "JPY")
	assert.ErrorIs(t, err, model.ErrCurrencyUnknown)
}

// --- refresh ---

func TestRefreshStoresSnapshotAndAudits(t *testing.T) {
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{}}
	cache := &noopCache{}
	fetch := &fakeFetcher{quote: client.RateQuote{Base: "USD", Quote: "ZWG", Rate: dec("27.1"), AsOf: time.Now(), Source: "stub"}}
	audit := &fakeAudit{}
	svc := newSvc(snaps, threeCurrencies(), cache, fetch, audit)
	caller := service.Caller{UserID: uuid.New(), ProviderRole: model.RoleProviderSuperAdmin}
	out, err := svc.Refresh(context.Background(), caller, "USD", "ZWG")
	require.NoError(t, err)
	assert.True(t, out.Rate.Equal(dec("27.1")))
	assert.Equal(t, "zimrate", snaps.inserted[0].Source)
	// Both directions of the pair are invalidated so the reciprocal cache entry
	// cannot serve a stale rate after a refresh.
	assert.Equal(t, 2, cache.invalidCalls)
	require.Len(t, audit.events, 1)
	assert.Equal(t, "currency.rate.refresh", audit.events[0].Action)
}

func TestRefreshUpstreamDownMapsToSourceError(t *testing.T) {
	fetch := &fakeFetcher{err: errors.New("dial tcp: connection refused")}
	svc := newSvc(&fakeSnapshots{}, threeCurrencies(), nil, fetch, nil)
	caller := service.Caller{UserID: uuid.New(), ProviderRole: model.RoleProviderSuperAdmin}
	_, err := svc.Refresh(context.Background(), caller, "USD", "ZWG")
	assert.ErrorIs(t, err, model.ErrRateSourceDown)
}

// --- manual override ---

func TestManualOverrideStoresAndAudits(t *testing.T) {
	snaps := &fakeSnapshots{pairs: map[string]model.FXSnapshot{}}
	audit := &fakeAudit{}
	cache := &noopCache{}
	svc := newSvc(snaps, threeCurrencies(), cache, nil, audit)
	caller := service.Caller{UserID: uuid.New(), TenantID: uuid.New(), Role: model.RolePracticeAdmin}
	out, err := svc.SetManualRate(context.Background(), caller, "USD", "ZWG", dec("30.5"))
	require.NoError(t, err)
	assert.Equal(t, "manual", out.Source)
	assert.Equal(t, "manual", snaps.inserted[0].Source)
	require.Len(t, audit.events, 1)
	assert.Equal(t, "currency.rate.manual_override", audit.events[0].Action)
	assert.NotEmpty(t, audit.events[0].Detail, "override detail records the rate")
}

func TestManualOverrideRejectsNonPositive(t *testing.T) {
	svc := newSvc(&fakeSnapshots{}, threeCurrencies(), nil, nil, nil)
	caller := service.Caller{UserID: uuid.New(), TenantID: uuid.New(), Role: model.RolePracticeAdmin}
	_, err := svc.SetManualRate(context.Background(), caller, "USD", "ZWG", dec("0"))
	assert.ErrorIs(t, err, model.ErrValidation)
}
