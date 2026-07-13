// Command smoke is the currency-service Contract Smoke Test runner. It asserts
// x-fr and x-nfr coverage, then exercises the live local service. JWTs are
// signed locally with OMNISURG_JWT_SECRET (the same secret the service verifies
// with), so the runner does not depend on identity-service.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"time"

	ojwt "github.com/OmniSurg/omnisurg-go-common/jwt"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type credentials struct {
	BaseURL       string `json:"base_url"`
	AcmeTenantID  string `json:"acme_tenant_id"`
	OtherTenantID string `json:"other_tenant_id"`
}

type spec struct {
	Paths map[string]map[string]struct {
		OperationID string         `yaml:"operationId"`
		XFR         []string       `yaml:"x-fr"`
		XNFR        map[string]any `yaml:"x-nfr"`
	} `yaml:"paths"`
}

type runner struct {
	creds  credentials
	secret string
	client *http.Client
	fails  int
	total  int
}

func main() {
	credPath := flag.String("credentials", "credentials.json", "path to credentials json")
	specPath := flag.String("spec", "../docs/api-contract/openapi.yaml", "path to openapi yaml")
	flag.Parse()

	secret := os.Getenv("OMNISURG_JWT_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "OMNISURG_JWT_SECRET not set in environment")
		os.Exit(2)
	}
	r := &runner{secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
	if err := r.loadCreds(*credPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := r.checkSpecCoverage(*specPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	r.testHealth()
	r.testLatestRate()
	r.testConvert()
	r.testRefresh()
	r.testManualOverride()
	r.testConfig()
	r.testLatency()

	fmt.Printf("\nSummary: %d of %d scenarios passed. ", r.total-r.fails, r.total)
	if r.fails > 0 {
		fmt.Printf("CST FAIL.\n")
		os.Exit(1)
	}
	fmt.Printf("CST PASS.\n")
}

func (r *runner) loadCreds(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}
	return json.Unmarshal(b, &r.creds)
}

func (r *runner) checkSpecCoverage(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	var s spec
	if err := yaml.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}
	required := []string{"latency_ms_p99", "rate_limit", "auth_required", "tenant_scoped"}
	var missing []string
	for p, methods := range s.Paths {
		for m, op := range methods {
			if len(op.XFR) == 0 || len(op.XNFR) == 0 {
				missing = append(missing, fmt.Sprintf("%s %s missing x-fr or x-nfr", m, p))
				continue
			}
			for _, k := range required {
				if _, ok := op.XNFR[k]; !ok {
					missing = append(missing, fmt.Sprintf("%s %s missing x-nfr %s", m, p, k))
				}
			}
		}
	}
	sort.Strings(missing)
	for _, mm := range missing {
		r.record("spec-coverage", false, mm)
	}
	if len(missing) > 0 {
		return fmt.Errorf("spec coverage failed for %d operations", len(missing))
	}
	r.record("spec-coverage", true, "every operation declares x-fr and x-nfr")
	return nil
}

func (r *runner) mintProvider() string {
	tok, _ := ojwt.Sign(ojwt.Claims{Subject: uuid.NewString(), ProviderRole: "provider_super_admin"}, r.secret, time.Hour)
	return tok
}

func (r *runner) mintTenant(tenantID, role string) string {
	tok, _ := ojwt.Sign(ojwt.Claims{Subject: uuid.NewString(), TenantID: tenantID, Role: role}, r.secret, time.Hour)
	return tok
}

func (r *runner) request(method, path, token string, headers map[string]string, body any) (int, []byte) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, r.creds.BaseURL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (r *runner) record(name string, pass bool, detail string) {
	r.total++
	status := "PASS"
	if !pass {
		status = "FAIL"
		r.fails++
	}
	fmt.Printf("[%s] %s :: %s\n", status, name, detail)
}

func has(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }

func (r *runner) auditCount(tenantToken, action string) int {
	code, b := r.request(http.MethodGet, "/_debug/audit?action="+action, tenantToken, nil, nil)
	if code != 200 {
		return 0
	}
	var parsed struct {
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &parsed)
	return parsed.Data.Count
}

func convertedMinor(b []byte) int64 {
	var parsed struct {
		Data struct {
			ConvertedMinor int64 `json:"converted_minor"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &parsed)
	return parsed.Data.ConvertedMinor
}

// --- batteries ---

func (r *runner) testHealth() {
	code, b := r.request(http.MethodGet, "/health", "", nil, nil)
	r.record("GET /health", code == 200 && has(b, "\"status\":\"ok\""), fmt.Sprintf("status=%d", code))
}

func (r *runner) testLatestRate() {
	reception := r.mintTenant(r.creds.AcmeTenantID, "reception")
	code, b := r.request(http.MethodGet, "/rates/latest?base=USD&quote=ZWG", reception, nil, nil)
	r.record("GET /rates/latest x-fr authed 200", code == 200 && has(b, "26.7692"), fmt.Sprintf("status=%d", code))

	// reciprocal: ZWG/USD resolves from the inverse leg.
	code1b, b1b := r.request(http.MethodGet, "/rates/latest?base=ZWG&quote=USD", reception, nil, nil)
	r.record("GET /rates/latest x-fr reciprocal 200", code1b == 200 && has(b1b, "0.0373"), fmt.Sprintf("status=%d", code1b))

	code2, b2 := r.request(http.MethodGet, "/rates/latest?base=USD&quote=ZWG", "", nil, nil)
	r.record("GET /rates/latest x-fr missing auth 401", code2 == 401 && has(b2, "AUTH_UNAUTHORIZED"), fmt.Sprintf("status=%d", code2))

	code3, b3 := r.request(http.MethodGet, "/rates/latest?base=USD&quote=XXX", reception, nil, nil)
	r.record("GET /rates/latest x-fr unknown currency 422", code3 == 422 && has(b3, "CURRENCY_UNKNOWN"), fmt.Sprintf("status=%d", code3))

	code4, b4 := r.request(http.MethodGet, "/rates/latest?base=USD&quote=GBP", reception, nil, nil)
	// GBP is not an active currency in P1, so this is 422 (unknown), not 404.
	r.record("GET /rates/latest x-fr inactive currency 422", code4 == 422 && has(b4, "CURRENCY_UNKNOWN"), fmt.Sprintf("status=%d", code4))
}

func (r *runner) testConvert() {
	reception := r.mintTenant(r.creds.AcmeTenantID, "reception")

	// 100.00 USD -> ZWG at 26.7692 = 2676.92 ZWG = 267692 minor.
	code, b := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": 10000, "from": "USD", "to": "ZWG"})
	r.record("POST /convert x-fr USD->ZWG correctness", code == 200 && convertedMinor(b) == 267692, fmt.Sprintf("status=%d converted=%d want=267692", code, convertedMinor(b)))

	// 2000.00 ZAR -> USD at the seeded ZAR/USD=0.06128 = 122.56 USD = 12256 minor
	// (sheet worked example).
	code2, b2 := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": 200000, "from": "ZAR", "to": "USD"})
	r.record("POST /convert x-fr ZAR->USD worked example", code2 == 200 && convertedMinor(b2) == 12256, fmt.Sprintf("status=%d converted=%d want=12256", code2, convertedMinor(b2)))

	// cross: 1000.00 ZWG -> ZAR via USD. Only ZAR/USD=0.06128 is seeded, so
	// USD/ZAR derives as 1/0.06128 = 16.318538. Cross = ZWG/USD * USD/ZAR =
	// 0.037356 * 16.318538 = 0.609601. 1000 * 0.609601 = 609.60 ZAR = 60960 minor.
	// This is the single-source value: a direct ZAR->USD and this cross now agree.
	code3, b3 := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": 100000, "from": "ZWG", "to": "ZAR"})
	r.record("POST /convert x-fr cross ZWG->ZAR", code3 == 200 && convertedMinor(b3) == 60960, fmt.Sprintf("status=%d converted=%d want=60960", code3, convertedMinor(b3)))

	// identity
	code4, b4 := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": 4299, "from": "USD", "to": "USD"})
	r.record("POST /convert x-fr identity", code4 == 200 && convertedMinor(b4) == 4299, fmt.Sprintf("status=%d converted=%d", code4, convertedMinor(b4)))

	code5, b5 := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": -1, "from": "USD", "to": "ZWG"})
	r.record("POST /convert x-fr negative 422", code5 == 422 && has(b5, "VALIDATION_FAILED"), fmt.Sprintf("status=%d", code5))

	code6, b6 := r.request(http.MethodPost, "/convert", "", nil, map[string]any{"amount_minor": 100, "from": "USD", "to": "ZWG"})
	r.record("POST /convert x-fr missing auth 401", code6 == 401 && has(b6, "AUTH_UNAUTHORIZED"), fmt.Sprintf("status=%d", code6))

	// No rate snapshot supports USD->BWP: BWP is a known, active reference
	// currency with no seeded rate, so the contract's documented 404
	// CURRENCY_RATE_NOT_FOUND applies (distinct from 422 CURRENCY_UNKNOWN).
	code7, b7 := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": 10000, "from": "USD", "to": "BWP"})
	r.record("POST /convert x-fr no rate snapshot 404", code7 == 404 && has(b7, "CURRENCY_RATE_NOT_FOUND"), fmt.Sprintf("status=%d", code7))
}

func (r *runner) testRefresh() {
	provider := r.mintProvider()
	code, b := r.request(http.MethodPost, "/refresh", provider, nil, map[string]any{"base": "USD", "quote": "ZWG"})
	r.record("POST /refresh x-fr provider 201 from stub", code == 201 && has(b, "\"source\":\"zimrate\""), fmt.Sprintf("status=%d", code))

	code2, b2 := r.request(http.MethodPost, "/refresh", r.mintTenant(r.creds.AcmeTenantID, "practice_admin"), nil, nil)
	r.record("POST /refresh x-fr tenant role 403", code2 == 403 && has(b2, "AUTH_FORBIDDEN"), fmt.Sprintf("status=%d", code2))

	code3, b3 := r.request(http.MethodPost, "/refresh", "", nil, nil)
	r.record("POST /refresh x-fr missing auth 401", code3 == 401 && has(b3, "AUTH_UNAUTHORIZED"), fmt.Sprintf("status=%d", code3))

	// audit under the platform tenant (all-zero) for the provider refresh.
	platformToken := r.mintTenant("00000000-0000-0000-0000-000000000000", "practice_admin")
	r.record("POST /refresh x-nfr audit currency.rate.refresh", r.auditCount(platformToken, "currency.rate.refresh") >= 1, "audit row present")
}

func (r *runner) testManualOverride() {
	admin := r.mintTenant(r.creds.AcmeTenantID, "practice_admin")
	// Override the direct ZAR/ZWG pair (the reverse direction), which no convert
	// assertion reads: the cross ZWG->ZAR resolves through USD off ZWG/USD and
	// ZAR/USD, never the direct ZAR/ZWG leg. This keeps the append-only snapshot
	// from perturbing the deterministic convert checks on reruns against a
	// persistent volume.
	code, b := r.request(http.MethodPost, "/rates/manual", admin, nil, map[string]any{"base": "ZAR", "quote": "ZWG", "rate": "16.387321"})
	r.record("POST /rates/manual x-fr admin 201", code == 201 && has(b, "\"source\":\"manual\"") && has(b, "16.387321"), fmt.Sprintf("status=%d", code))

	code2, b2 := r.request(http.MethodPost, "/rates/manual", r.mintTenant(r.creds.AcmeTenantID, "reception"), nil, map[string]any{"base": "ZAR", "quote": "ZWG", "rate": "0.6"})
	r.record("POST /rates/manual x-fr reception 403", code2 == 403 && has(b2, "AUTH_FORBIDDEN"), fmt.Sprintf("status=%d", code2))

	code3, b3 := r.request(http.MethodPost, "/rates/manual", admin, nil, map[string]any{"base": "USD", "quote": "ZWG", "rate": "abc"})
	r.record("POST /rates/manual x-fr bad rate 422", code3 == 422 && has(b3, "VALIDATION_FAILED"), fmt.Sprintf("status=%d", code3))

	r.record("POST /rates/manual x-nfr audit currency.rate.manual_override", r.auditCount(admin, "currency.rate.manual_override") >= 1, "audit row present")
}

func (r *runner) testConfig() {
	reception := r.mintTenant(r.creds.AcmeTenantID, "reception")
	code, b := r.request(http.MethodGet, "/config", reception, nil, nil)
	r.record("GET /config x-fr authed 200", code == 200 && has(b, "display_currency") && has(b, "\"base_currency\":\"USD\""), fmt.Sprintf("status=%d", code))

	code2, b2 := r.request(http.MethodGet, "/config", "", nil, nil)
	r.record("GET /config x-fr missing auth 401", code2 == 401 && has(b2, "AUTH_UNAUTHORIZED"), fmt.Sprintf("status=%d", code2))

	admin := r.mintTenant(r.creds.AcmeTenantID, "practice_admin")
	code3, b3 := r.request(http.MethodPut, "/config", admin, nil, map[string]any{"display_currency": "ZAR", "fx_source": "manual"})
	r.record("PUT /config x-fr admin 200", code3 == 200 && has(b3, "\"display_currency\":\"ZAR\""), fmt.Sprintf("status=%d", code3))

	code4, b4 := r.request(http.MethodPut, "/config", reception, nil, map[string]any{"display_currency": "ZWG"})
	r.record("PUT /config x-fr reception 403", code4 == 403 && has(b4, "AUTH_FORBIDDEN"), fmt.Sprintf("status=%d", code4))

	code5, b5 := r.request(http.MethodPut, "/config", admin, nil, map[string]any{"display_currency": "JPY"})
	r.record("PUT /config x-fr unknown currency 422", code5 == 422 && has(b5, "CURRENCY_UNKNOWN"), fmt.Sprintf("status=%d", code5))

	r.record("PUT /config x-nfr audit currency.config.update", r.auditCount(admin, "currency.config.update") >= 1, "audit row present")

	// tenant isolation: the other tenant gets its own default config, never Acme's ZAR override.
	other := r.mintTenant(r.creds.OtherTenantID, "practice_admin")
	code6, b6 := r.request(http.MethodGet, "/config", other, nil, nil)
	r.record("GET /config x-nfr tenant isolation", code6 == 200 && has(b6, "\"display_currency\":\"ZWG\""), fmt.Sprintf("status=%d other isolated default", code6))

	// reset Acme config so reruns stay deterministic.
	_, _ = r.request(http.MethodPut, "/config", admin, nil, map[string]any{"display_currency": "ZWG", "fx_source": "zimrate"})
}

func (r *runner) testLatency() {
	reception := r.mintTenant(r.creds.AcmeTenantID, "reception")

	// GET /rates/latest p99 budget.
	p99Rates, ok := r.probeP99("GET /rates/latest x-nfr latency", func() int {
		code, _ := r.request(http.MethodGet, "/rates/latest?base=USD&quote=ZWG", reception, nil, nil)
		return code
	})
	if ok {
		r.record("GET /rates/latest x-nfr latency_ms_p99 budget 50ms (warm cache)", p99Rates <= 50*time.Millisecond, fmt.Sprintf("p99=%dms", p99Rates.Milliseconds()))
	}

	// POST /convert declares the same 50ms p99 budget; probe it too.
	p99Convert, ok := r.probeP99("POST /convert x-nfr latency", func() int {
		code, _ := r.request(http.MethodPost, "/convert", reception, nil, map[string]any{"amount_minor": 10000, "from": "USD", "to": "ZWG"})
		return code
	})
	if ok {
		r.record("POST /convert x-nfr latency_ms_p99 budget 50ms (warm cache)", p99Convert <= 50*time.Millisecond, fmt.Sprintf("p99=%dms", p99Convert.Milliseconds()))
	}
}

// probeP99 calls do n times, expecting 200 each, and returns the p99 sample. It
// records a failure and returns ok=false if any call does not return 200.
func (r *runner) probeP99(name string, do func() int) (time.Duration, bool) {
	const n = 20
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		if code := do(); code != 200 {
			r.record(name, false, fmt.Sprintf("call failed status=%d", code))
			return 0, false
		}
		samples = append(samples, time.Since(start))
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(math.Ceil(0.99*float64(len(samples)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx], true
}
