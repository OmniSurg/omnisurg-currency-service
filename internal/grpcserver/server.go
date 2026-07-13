// Package grpcserver adapts the gRPC CurrencyService contract onto the existing
// internal/service layer. It holds NO business logic: every RPC maps the
// request to the service input, calls the SAME method the REST handler calls,
// and maps the domain result or error back. This is the gRPC analogue of
// internal/handler. Rate resolution, the cross rate and reciprocal derivation,
// decimal rounding, and the cache fall through all live in internal/service and
// are reached identically from both transports.
//
// GLOBAL REGISTRY GUARANTEE. Unlike a tenant-scoped service, CurrencyService is
// NOT tenant-scoped. fx_snapshots and currencies are platform-global, no-RLS
// tables shared by every tenant (the same posture as the tenant registry), so
// the read RPCs the BFF and billing dial here carry no tenant data and require
// no tenant id. The shared interceptor therefore runs with RequireTenant=false;
// a request id still propagates for correlation. GetLatestRate is the 50 ms p99
// FX hop the billing money loop depends on (design spec section 9). The admin
// mutations (Refresh, SetManualRate) stay REST-only and are deliberately NOT
// exposed on this gRPC surface, so there is no gRPC mutation to scope.
package grpcserver

import (
	"context"

	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	cerr "github.com/OmniSurg/omnisurg-go-common/errors"
	currencyv1 "github.com/OmniSurg/omnisurg-proto/gen/go/omnisurg/currency/v1"
)

// Server implements currencyv1.CurrencyServiceServer as a thin adapter.
type Server struct {
	currencyv1.UnimplementedCurrencyServiceServer
	rates *service.CurrencyService
}

// New builds the adapter over the existing currency service layer.
func New(rates *service.CurrencyService) *Server {
	return &Server{rates: rates}
}

// GetLatestRate returns the most recent base to quote FX snapshot. No tenant
// context is required: FX rates are platform-global. Unknown currencies and
// missing pairs fail closed through the service layer and map to gRPC codes via
// the shared errors.ToStatus.
func (s *Server) GetLatestRate(ctx context.Context, req *currencyv1.GetLatestRateRequest) (*currencyv1.FXSnapshot, error) {
	snap, err := s.rates.GetLatestRate(ctx, req.GetBase(), req.GetQuote())
	if err != nil {
		return nil, cerr.ToStatus(err)
	}
	return toProtoFXSnapshot(snap), nil
}

// Convert converts the request amount (common.v1.Money in integer minor units
// of its currency) into the quote currency using the latest snapshot. The from
// currency is the money currency code; the to currency is the request quote. All
// math is decimal and minor-unit integer; no float is ever introduced.
func (s *Server) Convert(ctx context.Context, req *currencyv1.ConvertRequest) (*currencyv1.Conversion, error) {
	amount := req.GetAmount()
	conv, err := s.rates.Convert(ctx, amount.GetAmountMinor(), amount.GetCurrencyCode(), req.GetQuote())
	if err != nil {
		return nil, cerr.ToStatus(err)
	}
	return toProtoConversion(conv), nil
}
