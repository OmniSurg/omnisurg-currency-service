// Package client holds outbound HTTP clients. ZimRateClient pulls the official
// RBZ interbank rate from the ZimRate API (sheet 05_Multi_Currency primary
// feed). In the local stack it targets the zimrate-stub. The response shape
// mirrors the stub and the documented ZimRate JSON: pair, rate, as_of, source.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// RateQuote is the parsed upstream rate. Rate is "quote units per one base
// unit" matching the platform snapshot convention.
type RateQuote struct {
	Base   string
	Quote  string
	Rate   decimal.Decimal
	AsOf   time.Time
	Source string
}

// ZimRateClient fetches rates from the ZimRate API or its local stub.
type ZimRateClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewZimRateClient builds a client. baseURL is the host (no path); the rates
// path is appended internally. apiKey may be empty against the stub.
func NewZimRateClient(baseURL, apiKey string) *ZimRateClient {
	return &ZimRateClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 8 * time.Second},
	}
}

// upstreamResponse is the ZimRate / stub JSON body. Rate is decoded as a string
// (json.Number) and parsed with decimal.NewFromString so no float64 ever enters
// the rate ingestion path; binary float would lose precision on the exact
// decimals ZimRate quotes.
type upstreamResponse struct {
	Pair   string      `json:"pair"`
	Rate   json.Number `json:"rate"`
	AsOf   string      `json:"as_of"`
	Source string      `json:"source"`
}

// FetchRate pulls the rate for base/quote. The stub ignores the pair and always
// returns USD/ZWG; against the real API the pair query parameter selects it.
// The returned RateQuote carries the requested base and quote so the caller
// stores a correctly labelled snapshot.
func (c *ZimRateClient) FetchRate(ctx context.Context, base, quote string) (RateQuote, error) {
	url := fmt.Sprintf("%s/api/v1/rates?pair=%s/%s", c.baseURL, base, quote)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return RateQuote{}, fmt.Errorf("zimrate: build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return RateQuote{}, fmt.Errorf("zimrate: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RateQuote{}, fmt.Errorf("zimrate: upstream returned status %d", resp.StatusCode)
	}
	var body upstreamResponse
	if derr := json.NewDecoder(resp.Body).Decode(&body); derr != nil {
		return RateQuote{}, fmt.Errorf("zimrate: decode body: %w", derr)
	}
	rate, rerr := decimal.NewFromString(body.Rate.String())
	if rerr != nil {
		return RateQuote{}, fmt.Errorf("zimrate: parse rate %q: %w", body.Rate.String(), rerr)
	}
	if !rate.IsPositive() {
		return RateQuote{}, fmt.Errorf("zimrate: upstream returned non positive rate %s", body.Rate.String())
	}
	asOf, perr := time.Parse(time.RFC3339, body.AsOf)
	if perr != nil {
		asOf = time.Now().UTC()
	}
	// Parsed straight from the JSON token text, so the exact decimal ZimRate
	// quotes is preserved with no float64 round trip.
	return RateQuote{
		Base:   base,
		Quote:  quote,
		Rate:   rate,
		AsOf:   asOf,
		Source: body.Source,
	}, nil
}
