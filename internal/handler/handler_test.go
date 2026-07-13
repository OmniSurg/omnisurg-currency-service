package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/client"
	"github.com/OmniSurg/omnisurg-currency-service/internal/handler"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	ojwt "github.com/OmniSurg/omnisurg-go-common/jwt"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secret = "http-test-secret"

var tenantA = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

// --- in memory stores ---

type memSnaps struct{ pairs map[string]model.FXSnapshot }

func (m *memSnaps) GetLatest(_ context.Context, base, quote string) (model.FXSnapshot, error) {
	s, ok := m.pairs[base+"/"+quote]
	if !ok {
		return model.FXSnapshot{}, model.ErrRateNotFound
	}
	return s, nil
}
func (m *memSnaps) Insert(_ context.Context, in model.NewSnapshot) (model.FXSnapshot, error) {
	s := model.FXSnapshot{ID: uuid.New(), Base: in.Base, Quote: in.Quote, Rate: in.Rate, Source: in.Source, CapturedAt: time.Now()}
	m.pairs[in.Base+"/"+in.Quote] = s
	return s, nil
}

type memCur struct{}

func (memCur) Get(_ context.Context, code string) (model.Currency, error) {
	switch code {
	// BWP is a known, active reference currency with no seeded rate, so it drives
	// the documented CURRENCY_RATE_NOT_FOUND (404) path on convert and rate lookup.
	case "USD", "ZWG", "ZAR", "BWP":
		return model.Currency{Code: code, Decimals: 2, Active: true, IsBase: code == "USD"}, nil
	}
	return model.Currency{}, model.ErrCurrencyUnknown
}

type memConfig struct {
	rows map[uuid.UUID]model.TenantFXConfig
}

func (m *memConfig) Get(_ context.Context, t uuid.UUID) (model.TenantFXConfig, error) {
	c, ok := m.rows[t]
	if !ok {
		return model.TenantFXConfig{}, model.ErrTenantConfigEmpty
	}
	return c, nil
}
func (m *memConfig) Upsert(_ context.Context, t uuid.UUID, disp, src string) (model.TenantFXConfig, error) {
	c := model.TenantFXConfig{TenantID: t, DisplayCurrency: disp, FXSource: src, UpdatedAt: time.Now()}
	m.rows[t] = c
	return c, nil
}

type memCache struct{}

func (memCache) Get(context.Context, string, string) (model.FXSnapshot, bool) {
	return model.FXSnapshot{}, false
}
func (memCache) Set(context.Context, model.FXSnapshot)      {}
func (memCache) Invalidate(context.Context, string, string) {}

type memFetcher struct {
	q   client.RateQuote
	err error
}

func (m memFetcher) FetchRate(context.Context, string, string) (client.RateQuote, error) {
	return m.q, m.err
}

type memAudit struct{ events []model.AuditEvent }

func (m *memAudit) Emit(_ context.Context, ev model.AuditEvent) error {
	m.events = append(m.events, ev)
	return nil
}
func (m *memAudit) Query(_ context.Context, _ uuid.UUID, action string, _ *uuid.UUID) ([]model.AuditRow, error) {
	var out []model.AuditRow
	for _, e := range m.events {
		if e.Action == action {
			out = append(out, model.AuditRow{Action: e.Action})
		}
	}
	return out, nil
}

type fixture struct {
	router  http.Handler
	snaps   *memSnaps
	audit   *memAudit
	fetcher memFetcher
}

func newFixture(t *testing.T, fetcher memFetcher) fixture {
	t.Helper()
	snaps := &memSnaps{pairs: map[string]model.FXSnapshot{}}
	cfg := &memConfig{rows: map[uuid.UUID]model.TenantFXConfig{}}
	audit := &memAudit{}
	ratesSvc := service.NewCurrencyService(snaps, memCur{}, memCache{}, fetcher, audit)
	configSvc := service.NewConfigService(cfg, memCur{}, audit)
	r := handler.NewRouter(handler.RouterConfig{
		Rates: ratesSvc, Config: configSvc, Audit: audit,
		JWTSecret: secret, Env: "local", Ping: func(context.Context) error { return nil },
	})
	return fixture{router: r, snaps: snaps, audit: audit, fetcher: fetcher}
}

func tenantToken(t *testing.T, tenantID, role string) string {
	t.Helper()
	tok, err := ojwt.Sign(ojwt.Claims{Subject: uuid.NewString(), TenantID: tenantID, Role: role}, secret, time.Hour)
	require.NoError(t, err)
	return tok
}

func providerToken(t *testing.T) string {
	t.Helper()
	tok, err := ojwt.Sign(ojwt.Claims{Subject: uuid.NewString(), ProviderRole: model.RoleProviderSuperAdmin}, secret, time.Hour)
	require.NoError(t, err)
	return tok
}

func do(t *testing.T, f fixture, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, "/api/v1/currency"+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return w, parsed
}

func TestHealth(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, _ := do(t, f, http.MethodGet, "/health", "", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetLatestRateRequiresAuth(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, body := do(t, f, http.MethodGet, "/rates/latest?base=USD&quote=ZWG", "", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "AUTH_UNAUTHORIZED", errCode(body))
}

func TestGetLatestRate(t *testing.T) {
	f := newFixture(t, memFetcher{})
	f.snaps.pairs["USD/ZWG"] = model.FXSnapshot{Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CapturedAt: time.Now()}
	w, body := do(t, f, http.MethodGet, "/rates/latest?base=USD&quote=ZWG", tenantToken(t, tenantA.String(), model.RoleReception), nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "26.7692", dataField(body, "rate"))
}

func TestGetLatestRateUnknownCurrency(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, body := do(t, f, http.MethodGet, "/rates/latest?base=USD&quote=XXX", tenantToken(t, tenantA.String(), model.RoleReception), nil)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "CURRENCY_UNKNOWN", errCode(body))
}

func TestConvert(t *testing.T) {
	f := newFixture(t, memFetcher{})
	f.snaps.pairs["USD/ZWG"] = model.FXSnapshot{Base: "USD", Quote: "ZWG", Rate: dec("26.7692"), Source: "seed", CapturedAt: time.Now()}
	w, body := do(t, f, http.MethodPost, "/convert", tenantToken(t, tenantA.String(), model.RoleReception),
		map[string]any{"amount_minor": 10000, "from": "USD", "to": "ZWG"})
	assert.Equal(t, http.StatusOK, w.Code)
	data := body["data"].(map[string]any)
	assert.EqualValues(t, 267692, data["converted_minor"])
}

func TestConvertNegativeRejected(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, body := do(t, f, http.MethodPost, "/convert", tenantToken(t, tenantA.String(), model.RoleReception),
		map[string]any{"amount_minor": -5, "from": "USD", "to": "ZWG"})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "VALIDATION_FAILED", errCode(body))
}

func TestRefreshProviderOnly(t *testing.T) {
	f := newFixture(t, memFetcher{q: client.RateQuote{Base: "USD", Quote: "ZWG", Rate: dec("27.5"), AsOf: time.Now(), Source: "stub"}})
	// tenant role forbidden
	w, body := do(t, f, http.MethodPost, "/refresh", tenantToken(t, tenantA.String(), model.RolePracticeAdmin), nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "AUTH_FORBIDDEN", errCode(body))
	// provider allowed
	w2, body2 := do(t, f, http.MethodPost, "/refresh", providerToken(t), map[string]any{"base": "USD", "quote": "ZWG"})
	assert.Equal(t, http.StatusCreated, w2.Code)
	assert.Equal(t, "27.5", dataField(body2, "rate"))
}

func TestRefreshUpstreamDown(t *testing.T) {
	f := newFixture(t, memFetcher{err: assertErr{}})
	w, body := do(t, f, http.MethodPost, "/refresh", providerToken(t), nil)
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, "CURRENCY_SOURCE_UNAVAILABLE", errCode(body))
}

func TestManualOverrideAdminOnly(t *testing.T) {
	f := newFixture(t, memFetcher{})
	// reception forbidden
	w, _ := do(t, f, http.MethodPost, "/rates/manual", tenantToken(t, tenantA.String(), model.RoleReception),
		map[string]any{"base": "USD", "quote": "ZWG", "rate": "30"})
	assert.Equal(t, http.StatusForbidden, w.Code)
	// admin allowed + audited
	w2, body2 := do(t, f, http.MethodPost, "/rates/manual", tenantToken(t, tenantA.String(), model.RolePracticeAdmin),
		map[string]any{"base": "USD", "quote": "ZWG", "rate": "30.5"})
	assert.Equal(t, http.StatusCreated, w2.Code)
	assert.Equal(t, "manual", dataField(body2, "source"))
	assert.Equal(t, "30.5", dataField(body2, "rate"))
}

func TestManualOverrideBadRate(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, body := do(t, f, http.MethodPost, "/rates/manual", tenantToken(t, tenantA.String(), model.RolePracticeAdmin),
		map[string]any{"base": "USD", "quote": "ZWG", "rate": "abc"})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "VALIDATION_FAILED", errCode(body))
}

func TestConfigGetDefaults(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, body := do(t, f, http.MethodGet, "/config", tenantToken(t, tenantA.String(), model.RoleReception), nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ZWG", dataField(body, "display_currency"))
	assert.Equal(t, "USD", dataField(body, "base_currency"))
}

func TestConfigUpdateAdminOnly(t *testing.T) {
	f := newFixture(t, memFetcher{})
	// reception forbidden
	w, _ := do(t, f, http.MethodPut, "/config", tenantToken(t, tenantA.String(), model.RoleReception),
		map[string]any{"display_currency": "ZAR"})
	assert.Equal(t, http.StatusForbidden, w.Code)
	// admin allowed
	w2, body2 := do(t, f, http.MethodPut, "/config", tenantToken(t, tenantA.String(), model.RolePracticeAdmin),
		map[string]any{"display_currency": "ZAR", "fx_source": "manual"})
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "ZAR", dataField(body2, "display_currency"))
}

func TestConfigUpdateUnknownCurrency(t *testing.T) {
	f := newFixture(t, memFetcher{})
	w, body := do(t, f, http.MethodPut, "/config", tenantToken(t, tenantA.String(), model.RolePracticeAdmin),
		map[string]any{"display_currency": "JPY"})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "CURRENCY_UNKNOWN", errCode(body))
}

// --- helpers ---

type assertErr struct{}

func (assertErr) Error() string { return "upstream down" }

func errCode(body map[string]any) string {
	e, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := e["code"].(string)
	return code
}

func dataField(body map[string]any, field string) string {
	d, ok := body["data"].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := d[field].(string)
	return v
}
