package billing

import "log/slog"

// ComputeCostBRL is a pure helper: converts (units, USD/unit, USD/BRL) → BRL.
// Returns 0 if the price is unknown (logs WARN so reconcile can surface the
// drift later). If fx.Get returns (_, false) the caller-supplied defaultFx
// is used; if defaultFx is also non-positive the result is 0.
//
// Defensive: negative units clamp to 0. Nil loaders return 0 so a
// mis-wired caller cannot panic the request.
//
// Accepts the concrete loader pointers — both PricesLoader.Get and
// FXLoader.Get are lock-free on the hot path.
func ComputeCostBRL(
	units float64,
	model, provider, unit string,
	prices *PricesLoader,
	fx *FXLoader,
	defaultUSDBRL float64,
	log *slog.Logger,
) float64 {
	if units <= 0 {
		return 0
	}
	if prices == nil {
		return 0
	}
	p, ok := prices.Get(model, provider, unit)
	if !ok {
		if log != nil {
			log.Warn("price missing — cost will be 0",
				"model", model, "provider", provider, "unit", unit)
		}
		return 0
	}
	rate := defaultUSDBRL
	if fx != nil {
		if fxRow, fxOK := fx.Get("USD/BRL"); fxOK {
			rate = fxRow.Rate
		}
	}
	if rate <= 0 {
		return 0
	}
	return units * p.UnitCostUSD * rate
}
