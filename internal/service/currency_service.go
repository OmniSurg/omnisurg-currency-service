package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/rs/zerolog/log"
	"github.com/shopspring/decimal"
)

// CurrencyService owns rate resolution, conversion, refresh, and manual
// override logic.
type CurrencyService struct {
	snaps      SnapshotStore
	currencies CurrencyStore
	cache      RateCacher
	fetcher    RateFetcher
	audit      AuditEmitter
}

// NewCurrencyService builds a CurrencyService.
func NewCurrencyService(snaps SnapshotStore, currencies CurrencyStore, cache RateCacher, fetcher RateFetcher, audit AuditEmitter) *CurrencyService {
	return &CurrencyService{snaps: snaps, currencies: currencies, cache: cache, fetcher: fetcher, audit: audit}
}

// GetLatestRate returns the most recent base->quote rate. It serves from the
// cache when warm, otherwise reads the database (or derives the reciprocal /
// cross rate) and warms the cache. Unknown currencies fail closed.
func (s *CurrencyService) GetLatestRate(ctx context.Context, base, quote string) (model.FXSnapshot, error) {
	if err := s.ensureCurrency(ctx, base); err != nil {
		return model.FXSnapshot{}, err
	}
	if err := s.ensureCurrency(ctx, quote); err != nil {
		return model.FXSnapshot{}, err
	}
	if base == quote {
		return model.FXSnapshot{Base: base, Quote: quote, Rate: decimal.NewFromInt(1), Source: "identity", CapturedAt: time.Now().UTC()}, nil
	}
	if hit, ok := s.cache.Get(ctx, base, quote); ok {
		return hit, nil
	}
	snap, err := s.resolveRate(ctx, base, quote)
	if err != nil {
		return model.FXSnapshot{}, err
	}
	s.cache.Set(ctx, snap)
	return snap, nil
}

// resolveRate finds the base->quote rate from stored snapshots.
//
// Resolution order is deliberate for money correctness. USD is the platform
// reporting currency and every amount anchors to it, so for a pair where one
// side IS USD the reciprocal of the stored inverse pair is the genuine
// reciprocal of the same economic pair and is safe. For a CROSS pair (neither
// side is USD) the canonical value is the composition through the USD pivot:
// inverting a single directly stored cross snapshot (for example a manual
// override of the opposite direction) can diverge from the USD-anchored value
// that billing computes, so the reciprocal-of-inverse shortcut must not apply
// to cross pairs. A directly stored cross pair is still honoured (it is an
// explicit intentional rate); only deriving one cross direction by inverting
// the other is disallowed.
func (s *CurrencyService) resolveRate(ctx context.Context, base, quote string) (model.FXSnapshot, error) {
	// Direct pair (including an explicitly stored cross pair).
	if snap, err := s.snaps.GetLatest(ctx, base, quote); err == nil {
		return snap, nil
	} else if !errors.Is(err, model.ErrRateNotFound) {
		return model.FXSnapshot{}, err
	}
	// Reciprocal of the inverse pair, only when USD is one side. For a cross pair
	// the USD pivot below is canonical, so the reciprocal shortcut is skipped to
	// avoid an inverted manual override diverging from the USD-anchored rate.
	if base == model.BaseCurrency || quote == model.BaseCurrency {
		if inv, err := s.snaps.GetLatest(ctx, quote, base); err == nil {
			rate := model.Reciprocal(inv.Rate)
			if rate.IsPositive() {
				return model.FXSnapshot{Base: base, Quote: quote, Rate: rate, Source: inv.Source, CapturedAt: inv.CapturedAt}, nil
			}
		} else if !errors.Is(err, model.ErrRateNotFound) {
			return model.FXSnapshot{}, err
		}
	}
	// Cross rate through USD: base->USD and USD->quote.
	if base != model.BaseCurrency && quote != model.BaseCurrency {
		usdPerBase, err := s.usdPerUnit(ctx, base)
		if err != nil {
			return model.FXSnapshot{}, err
		}
		quotePerUSD, err := s.unitsPerUSD(ctx, quote)
		if err != nil {
			return model.FXSnapshot{}, err
		}
		cross, cerr := model.SafeCrossRate(usdPerBase.rate, quotePerUSD.rate)
		if cerr != nil {
			return model.FXSnapshot{}, model.ErrRateNotFound
		}
		captured := usdPerBase.capturedAt
		if quotePerUSD.capturedAt.Before(captured) {
			captured = quotePerUSD.capturedAt
		}
		return model.FXSnapshot{Base: base, Quote: quote, Rate: cross, Source: "cross", CapturedAt: captured}, nil
	}
	return model.FXSnapshot{}, model.ErrRateNotFound
}

type ratePoint struct {
	rate       decimal.Decimal
	capturedAt time.Time
}

// usdPerUnit returns "USD per one unit" of currency c (the c/USD rate).
func (s *CurrencyService) usdPerUnit(ctx context.Context, c string) (ratePoint, error) {
	if snap, err := s.snaps.GetLatest(ctx, c, model.BaseCurrency); err == nil {
		return ratePoint{rate: snap.Rate, capturedAt: snap.CapturedAt}, nil
	} else if !errors.Is(err, model.ErrRateNotFound) {
		return ratePoint{}, err
	}
	snap, err := s.snaps.GetLatest(ctx, model.BaseCurrency, c)
	if err != nil {
		return ratePoint{}, err
	}
	return ratePoint{rate: model.Reciprocal(snap.Rate), capturedAt: snap.CapturedAt}, nil
}

// unitsPerUSD returns "units of c per one USD" (the USD/c rate).
func (s *CurrencyService) unitsPerUSD(ctx context.Context, c string) (ratePoint, error) {
	if snap, err := s.snaps.GetLatest(ctx, model.BaseCurrency, c); err == nil {
		return ratePoint{rate: snap.Rate, capturedAt: snap.CapturedAt}, nil
	} else if !errors.Is(err, model.ErrRateNotFound) {
		return ratePoint{}, err
	}
	snap, err := s.snaps.GetLatest(ctx, c, model.BaseCurrency)
	if err != nil {
		return ratePoint{}, err
	}
	return ratePoint{rate: model.Reciprocal(snap.Rate), capturedAt: snap.CapturedAt}, nil
}

// Convert converts amountMinor in from to the smallest unit of to, applying the
// resolved rate and spec rounding (half away from zero to the target decimals).
func (s *CurrencyService) Convert(ctx context.Context, amountMinor int64, from, to string) (model.Conversion, error) {
	if amountMinor < 0 {
		return model.Conversion{}, model.ErrValidation.WithDetails([]map[string]string{{"field": "amount_minor", "issue": "must not be negative"}})
	}
	fromCur, err := s.currencies.Get(ctx, from)
	if err != nil {
		return model.Conversion{}, err
	}
	toCur, err := s.currencies.Get(ctx, to)
	if err != nil {
		return model.Conversion{}, err
	}
	snap, err := s.GetLatestRate(ctx, from, to)
	if err != nil {
		return model.Conversion{}, err
	}
	converted := model.ConvertMinor(amountMinor, fromCur.Decimals, toCur.Decimals, snap.Rate)
	return model.Conversion{
		FromCurrency:   from,
		ToCurrency:     to,
		AmountMinor:    amountMinor,
		ConvertedMinor: converted,
		Rate:           snap.Rate,
		CapturedAt:     snap.CapturedAt,
	}, nil
}

// Refresh pulls the latest rate from the upstream source and appends a snapshot.
// Provider scoped (the router gates the role). Upstream failure maps to a clean
// 502 so the caller can retry; the existing snapshots remain readable.
func (s *CurrencyService) Refresh(ctx context.Context, caller Caller, base, quote string) (model.FXSnapshot, error) {
	if base == "" {
		base = model.BaseCurrency
	}
	if quote == "" {
		quote = "ZWG"
	}
	if err := s.ensureCurrency(ctx, base); err != nil {
		return model.FXSnapshot{}, err
	}
	if err := s.ensureCurrency(ctx, quote); err != nil {
		return model.FXSnapshot{}, err
	}
	if s.fetcher == nil {
		return model.FXSnapshot{}, model.ErrRateSourceDown
	}
	quoteRate, err := s.fetcher.FetchRate(ctx, base, quote)
	if err != nil {
		return model.FXSnapshot{}, model.ErrRateSourceDown.WithCause(err)
	}
	actor := caller.UserID
	snap, err := s.snaps.Insert(ctx, model.NewSnapshot{
		Base: base, Quote: quote, Rate: quoteRate.Rate, Source: "zimrate", CreatedBy: &actor,
	})
	if err != nil {
		return model.FXSnapshot{}, err
	}
	s.cache.Invalidate(ctx, base, quote)
	s.cache.Invalidate(ctx, quote, base)
	s.emitAudit(ctx, caller, "currency.rate.refresh", fmt.Sprintf("%s/%s=%s source=zimrate", base, quote, snap.Rate.String()))
	return snap, nil
}

// SetManualRate records an admin override snapshot. The sheet requires the
// override be logged with user and timestamp; the audit row carries both.
func (s *CurrencyService) SetManualRate(ctx context.Context, caller Caller, base, quote string, rate decimal.Decimal) (model.FXSnapshot, error) {
	if err := s.ensureCurrency(ctx, base); err != nil {
		return model.FXSnapshot{}, err
	}
	if err := s.ensureCurrency(ctx, quote); err != nil {
		return model.FXSnapshot{}, err
	}
	if !rate.IsPositive() {
		return model.FXSnapshot{}, model.ErrValidation.WithDetails([]map[string]string{{"field": "rate", "issue": "must be a positive decimal"}})
	}
	if base == quote {
		return model.FXSnapshot{}, model.ErrValidation.WithDetails([]map[string]string{{"field": "quote", "issue": "must differ from base"}})
	}
	actor := caller.UserID
	snap, err := s.snaps.Insert(ctx, model.NewSnapshot{
		Base: base, Quote: quote, Rate: rate, Source: "manual", CreatedBy: &actor,
	})
	if err != nil {
		return model.FXSnapshot{}, err
	}
	s.cache.Invalidate(ctx, base, quote)
	s.cache.Invalidate(ctx, quote, base)
	s.emitAudit(ctx, caller, "currency.rate.manual_override", fmt.Sprintf("%s/%s=%s by=%s", base, quote, rate.String(), actor.String()))
	return snap, nil
}

func (s *CurrencyService) ensureCurrency(ctx context.Context, code string) error {
	_, err := s.currencies.Get(ctx, code)
	return err
}

// emitAudit writes the audit row, logging (not failing) on error so a degraded
// audit path does not break the financial operation. Audit write failure is
// alertable via Sentry per the observability standard.
//
// Provider scoped actions (refresh) carry no tenant; they audit under the
// platform tenant (the all-zero UUID) which RLS scopes consistently for both
// insert and the debug query. The Plan F audit-service swap records platform
// actions in its own stream.
func (s *CurrencyService) emitAudit(ctx context.Context, caller Caller, action, detail string) {
	if s.audit == nil {
		return
	}
	actor := caller.UserID
	if err := s.audit.Emit(ctx, model.AuditEvent{
		TenantID:   caller.TenantID, // uuid.Nil for provider (platform) actions
		ActorID:    &actor,
		Action:     action,
		TargetType: "fx_rate",
		Detail:     detail,
		RequestID:  caller.RequestID,
	}); err != nil {
		log.Error().Err(err).Str("action", action).Msg("audit emit failed")
	}
}
