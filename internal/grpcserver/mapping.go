package grpcserver

import (
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	commonv1 "github.com/OmniSurg/omnisurg-proto/gen/go/omnisurg/common/v1"
	currencyv1 "github.com/OmniSurg/omnisurg-proto/gen/go/omnisurg/currency/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file holds the pure model to proto mappers. They mirror the REST
// presenter's model to DTO logic but target the proto types. There is no
// business logic here; only field projection. The rate is carried as a decimal
// string (never a float) so no precision is lost across the wire, matching the
// FXSnapshot contract comment in currency/v1.

func toProtoFXSnapshot(s model.FXSnapshot) *currencyv1.FXSnapshot {
	return &currencyv1.FXSnapshot{
		Base:       s.Base,
		Quote:      s.Quote,
		Rate:       s.Rate.String(),
		Source:     s.Source,
		CapturedAt: timestamppb.New(s.CapturedAt),
	}
}

// toProtoConversion maps the domain conversion to the proto message. Both legs
// are common.v1.Money in integer minor units plus the ISO currency code; the
// applied rate is echoed as an exact decimal string.
func toProtoConversion(c model.Conversion) *currencyv1.Conversion {
	return &currencyv1.Conversion{
		From: &commonv1.Money{CurrencyCode: c.FromCurrency, AmountMinor: c.AmountMinor},
		To:   &commonv1.Money{CurrencyCode: c.ToCurrency, AmountMinor: c.ConvertedMinor},
		Rate: c.Rate.String(),
	}
}
