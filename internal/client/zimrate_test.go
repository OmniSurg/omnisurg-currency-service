package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/client"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchRateParsesStubShape(t *testing.T) {
	// Mirror the zimrate-stub response shape exactly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pair":"USD/ZWG","rate":"26.7692","as_of":"2026-06-12T00:00:00Z","source":"stub","basis":"fixture"}`))
	}))
	defer srv.Close()

	c := client.NewZimRateClient(srv.URL, "")
	q, err := c.FetchRate(context.Background(), "USD", "ZWG")
	require.NoError(t, err)
	assert.Equal(t, "USD", q.Base)
	assert.Equal(t, "ZWG", q.Quote)
	require.NoError(t, err)
	want, _ := decimal.NewFromString("26.7692")
	assert.True(t, q.Rate.Equal(want))
	assert.Equal(t, "stub", q.Source)
	assert.Equal(t, 2026, q.AsOf.Year())
}

func TestFetchRateParsesBareNumberRate(t *testing.T) {
	// The live ZimRate feed and the stub may emit rate as a bare JSON number;
	// json.Number decodes the token text either way so no float64 is involved.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pair":"USD/ZWG","rate":26.7692,"as_of":"2026-06-12T00:00:00Z","source":"stub"}`))
	}))
	defer srv.Close()

	c := client.NewZimRateClient(srv.URL, "")
	q, err := c.FetchRate(context.Background(), "USD", "ZWG")
	require.NoError(t, err)
	want, _ := decimal.NewFromString("26.7692")
	assert.True(t, q.Rate.Equal(want))
}

func TestFetchRatePreservesExactDecimal(t *testing.T) {
	// A rate with more digits than a float64 can hold exactly must survive the
	// ingestion path unchanged, proving the string decode avoids float drift.
	const exact = "0.123456789012345678"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pair":"USD/ZWG","rate":"` + exact + `","as_of":"2026-06-12T00:00:00Z","source":"api"}`))
	}))
	defer srv.Close()

	c := client.NewZimRateClient(srv.URL, "")
	q, err := c.FetchRate(context.Background(), "USD", "ZWG")
	require.NoError(t, err)
	assert.Equal(t, exact, q.Rate.String())
}

func TestFetchRateRejectsNonNumericRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pair":"USD/ZWG","rate":"not-a-number","as_of":"2026-06-12T00:00:00Z","source":"api"}`))
	}))
	defer srv.Close()
	c := client.NewZimRateClient(srv.URL, "")
	_, err := c.FetchRate(context.Background(), "USD", "ZWG")
	assert.Error(t, err)
}

func TestFetchRateSendsBearerKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"pair":"USD/ZWG","rate":1.5,"as_of":"2026-06-12T00:00:00Z","source":"api"}`))
	}))
	defer srv.Close()

	c := client.NewZimRateClient(srv.URL, "secret-key")
	_, err := c.FetchRate(context.Background(), "USD", "ZWG")
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-key", gotAuth)
}

func TestFetchRateUpstreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := client.NewZimRateClient(srv.URL, "")
	_, err := c.FetchRate(context.Background(), "USD", "ZWG")
	assert.Error(t, err)
}

func TestFetchRateRejectsNonPositive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pair":"USD/ZWG","rate":0,"as_of":"2026-06-12T00:00:00Z","source":"api"}`))
	}))
	defer srv.Close()
	c := client.NewZimRateClient(srv.URL, "")
	_, err := c.FetchRate(context.Background(), "USD", "ZWG")
	assert.Error(t, err)
}

func TestFetchRateConnectionRefused(t *testing.T) {
	// A server that is closed immediately yields a dial error.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := client.NewZimRateClient(url, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.FetchRate(ctx, "USD", "ZWG")
	assert.Error(t, err)
}
