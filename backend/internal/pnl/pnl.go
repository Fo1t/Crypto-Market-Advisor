// Package pnl implements position accounting for USDT linear perpetuals.
//
// Every value here is a decimal. Floating point is never used for money: an
// accumulated rounding error in realized P&L would corrupt the trade history
// that later feeds the statistics and the LLM context.
package pnl

import (
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

var (
	hundred = decimal.NewFromInt(100)
	zero    = decimal.Zero
)

// Inputs carries the full state of a position for computation.
type Inputs struct {
	Position       domain.Position
	Fills          []domain.Fill
	Fees           []domain.CashEvent
	Funding        []domain.CashEvent
	CurrentPrice   *decimal.Decimal
	FeesConfigured bool
}

// Compute derives the complete P&L state from the append-only fill history.
// The position row is only a cache; this function is the source of truth.
func Compute(in Inputs) domain.PnL {
	p := in.Position
	out := domain.PnL{
		FeesConfigured: in.FeesConfigured,
		Approximate:    !p.SizeKnown,
		RemainingPct:   hundred,
	}

	sign := decimal.NewFromInt(int64(p.Direction.Sign()))

	var grossRealized, fees, closedQty decimal.Decimal
	for _, f := range in.Fills {
		fees = fees.Add(f.Fee)
		if f.Kind != domain.FillClose {
			continue
		}
		grossRealized = grossRealized.Add(f.RealizedPnL)
		if f.Quantity != nil {
			closedQty = closedQty.Add(*f.Quantity)
		}
	}
	for _, e := range in.Fees {
		fees = fees.Add(e.Amount)
	}

	var funding decimal.Decimal
	for _, e := range in.Funding {
		funding = funding.Add(e.Amount)
	}

	out.GrossRealized = grossRealized
	out.Fees = fees
	out.Funding = funding
	// Funding is signed: a positive amount is a payment received.
	out.NetRealized = grossRealized.Sub(fees).Add(funding)

	remaining := zero
	if p.RemainingQuantity != nil {
		remaining = *p.RemainingQuantity
	}
	if p.InitialQuantity != nil && p.InitialQuantity.GreaterThan(zero) {
		out.RemainingPct = remaining.Div(*p.InitialQuantity).Mul(hundred).Round(4)
	} else if p.Status == domain.PositionClosed {
		out.RemainingPct = zero
	}

	if in.CurrentPrice != nil && p.EntryPrice.GreaterThan(zero) {
		priceChange := in.CurrentPrice.Sub(p.EntryPrice).Div(p.EntryPrice).Mul(hundred).Mul(sign)
		out.PriceChangePct = priceChange.Round(6)
		out.LeveragedROIPct = priceChange.Mul(p.Leverage).Round(6)

		if p.Status != domain.PositionClosed && remaining.GreaterThan(zero) {
			out.Unrealized = in.CurrentPrice.Sub(p.EntryPrice).Mul(remaining).Mul(sign).Round(10)
		}
	}
	out.Total = out.NetRealized.Add(out.Unrealized)

	if p.InitialMargin != nil && p.InitialMargin.GreaterThan(zero) {
		realizedPct := out.NetRealized.Div(*p.InitialMargin).Mul(hundred).Round(4)
		out.RealizedPct = &realizedPct

		unrealizedPct := out.Unrealized.Div(*p.InitialMargin).Mul(hundred).Round(4)
		out.UnrealizedPct = &unrealizedPct

		roi := out.Total.Div(*p.InitialMargin).Mul(hundred).Round(4)
		out.ROIOnMarginPct = &roi
	}

	// Without a recorded size the absolute numbers are meaningless, so only
	// the percentage view is reported and it is flagged as approximate.
	if !p.SizeKnown {
		out.Approximate = true
	}
	return out
}

// CloseRequest describes a (partial) close the user performed manually.
type CloseRequest struct {
	Position       domain.Position
	ExecutionPrice decimal.Decimal
	ClosePct       *decimal.Decimal
	Quantity       *decimal.Decimal
	FeeType        domain.FeeType
	ActualFee      *decimal.Decimal
	MakerFeePct    decimal.Decimal
	TakerFeePct    decimal.Decimal
	FeesConfigured bool
}

// CloseResult is the computed effect of a close.
type CloseResult struct {
	Quantity     decimal.Decimal
	ClosePct     decimal.Decimal
	RealizedPnL  decimal.Decimal
	Fee          decimal.Decimal
	FeeEstimated bool
	Remaining    decimal.Decimal
	FullyClosed  bool
}

// ComputeClose derives the quantity, realized P&L and fee of a close.
// Either a percentage of the remaining position or an explicit quantity may be
// given; the percentage always refers to what is still open.
func ComputeClose(req CloseRequest) (CloseResult, error) {
	p := req.Position
	remaining := zero
	if p.RemainingQuantity != nil {
		remaining = *p.RemainingQuantity
	}

	var res CloseResult
	switch {
	case req.Quantity != nil && req.Quantity.GreaterThan(zero):
		res.Quantity = *req.Quantity
		if remaining.GreaterThan(zero) && res.Quantity.GreaterThan(remaining) {
			res.Quantity = remaining
		}
	case req.ClosePct != nil && req.ClosePct.GreaterThan(zero):
		pct := *req.ClosePct
		if pct.GreaterThan(hundred) {
			pct = hundred
		}
		res.ClosePct = pct
		res.Quantity = remaining.Mul(pct).Div(hundred)
	default:
		return res, ErrNoCloseSize
	}

	if remaining.GreaterThan(zero) {
		res.ClosePct = res.Quantity.Div(remaining).Mul(hundred).Round(6)
		res.Remaining = remaining.Sub(res.Quantity)
		if res.Remaining.LessThan(zero) {
			res.Remaining = zero
		}
	}
	res.FullyClosed = !p.SizeKnown && res.ClosePct.GreaterThanOrEqual(hundred) ||
		(remaining.GreaterThan(zero) && res.Remaining.IsZero())

	sign := decimal.NewFromInt(int64(p.Direction.Sign()))
	res.RealizedPnL = req.ExecutionPrice.Sub(p.EntryPrice).Mul(res.Quantity).Mul(sign).Round(10)

	fee, estimated := computeFee(req, res.Quantity)
	res.Fee = fee
	res.FeeEstimated = estimated
	return res, nil
}

// computeFee returns the fee of a fill and whether it was estimated from the
// configured rate rather than entered by the user.
func computeFee(req CloseRequest, quantity decimal.Decimal) (decimal.Decimal, bool) {
	if req.ActualFee != nil {
		return *req.ActualFee, false
	}
	if !req.FeesConfigured {
		// Fee rates were never configured; inventing one would silently
		// falsify the accounting, so the fee stays zero and is flagged.
		return zero, true
	}
	rate := req.TakerFeePct
	if req.FeeType == domain.FeeMaker {
		rate = req.MakerFeePct
	}
	notional := req.ExecutionPrice.Mul(quantity)
	return notional.Mul(rate).Div(hundred).Round(10), true
}

// OpenFee computes the fee of the opening fill.
func OpenFee(price, quantity, makerPct, takerPct decimal.Decimal, feeType domain.FeeType, actual *decimal.Decimal, configured bool) (decimal.Decimal, bool) {
	if actual != nil {
		return *actual, false
	}
	if !configured || quantity.IsZero() {
		return zero, true
	}
	rate := takerPct
	if feeType == domain.FeeMaker {
		rate = makerPct
	}
	return price.Mul(quantity).Mul(rate).Div(hundred).Round(10), true
}

// DeriveSize fills in the quantity/notional/margin triple from whichever of the
// three the user supplied. Returns sizeKnown=false when none was given.
func DeriveSize(entryPrice, leverage decimal.Decimal, quantity, notional, margin *decimal.Decimal) (q, n, m decimal.Decimal, sizeKnown bool) {
	if entryPrice.LessThanOrEqual(zero) || leverage.LessThanOrEqual(zero) {
		return zero, zero, zero, false
	}
	switch {
	case quantity != nil && quantity.GreaterThan(zero):
		q = *quantity
		n = q.Mul(entryPrice)
		m = n.Div(leverage)
	case notional != nil && notional.GreaterThan(zero):
		n = *notional
		q = n.Div(entryPrice)
		m = n.Div(leverage)
	case margin != nil && margin.GreaterThan(zero):
		m = *margin
		n = m.Mul(leverage)
		q = n.Div(entryPrice)
	default:
		return zero, zero, zero, false
	}
	return q.Round(12), n.Round(10), m.Round(10), true
}

// Classify labels a finished trade.
func Classify(net decimal.Decimal) domain.TradeResult {
	switch {
	case net.GreaterThan(zero):
		return domain.ResultWin
	case net.LessThan(zero):
		return domain.ResultLoss
	default:
		return domain.ResultBreakeven
	}
}
