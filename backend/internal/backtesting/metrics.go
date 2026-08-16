package backtesting

import (
	"math"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// simState tracks equity and counters while a backtest runs.
type simState struct {
	cash            decimal.Decimal
	initial         decimal.Decimal
	lastEquity      decimal.Decimal
	peakEquity      decimal.Decimal
	maxDrawdownPct  float64
	inferences      int
	cacheHits       int
	degradedSteps   int
	analysisPoints  int
	unfilledEntries int
	reasons         map[string]int
	replayFrom      time.Time
	replayTo        time.Time
	dataIssues      map[string]struct{}
	curve           []domain.EquityPoint
}

func newSimState(params domain.BacktestParams) *simState {
	initial := params.InitialCapital
	if initial.LessThanOrEqual(decimal.Zero) {
		initial = decimal.NewFromInt(10000)
	}
	return &simState{
		cash: initial, initial: initial, lastEquity: initial, peakEquity: initial,
		dataIssues: map[string]struct{}{}, reasons: map[string]int{},
	}
}

// recordEquityPoint samples the equity curve once per bar. The engine marks to
// market several times inside a bar; only the close-of-bar value is kept, so the
// curve stays a fair one-point-per-bar series.
func (s *simState) recordEquityPoint(at time.Time) {
	s.curve = append(s.curve, domain.EquityPoint{Time: at.UTC(), Equity: s.lastEquity.InexactFloat64()})
}

// equityCurve downsamples the recorded curve to at most max points, always
// keeping the first and last sample so the chart starts and ends on the real
// values.
func (s *simState) equityCurve(max int) []domain.EquityPoint {
	if max <= 2 || len(s.curve) <= max {
		return s.curve
	}
	out := make([]domain.EquityPoint, 0, max)
	step := float64(len(s.curve)-1) / float64(max-1)
	for i := 0; i < max-1; i++ {
		out = append(out, s.curve[int(float64(i)*step)])
	}
	return append(out, s.curve[len(s.curve)-1])
}

// recordReason counts the verdict of one analysis point, which is what makes a
// run with no trades explainable.
func (s *simState) recordReason(reason domain.StrategyDecisionReason) {
	if reason == "" {
		return
	}
	s.reasons[string(reason)]++
}

// recordBar widens the span the replay actually covered.
func (s *simState) recordBar(at time.Time) {
	if s.replayFrom.IsZero() || at.Before(s.replayFrom) {
		s.replayFrom = at
	}
	if at.After(s.replayTo) {
		s.replayTo = at
	}
}

// recordQuality remembers why a replayed step had less information than the
// live system would, because that is what shrinks the risk-adjusted size.
func (s *simState) recordQuality(quality domain.DataQuality) {
	if quality.Status == "" || quality.Status == domain.DataQualityOK {
		return
	}
	s.degradedSteps++
	for _, field := range quality.MissingFields {
		s.dataIssues[field] = struct{}{}
	}
}

func (s *simState) equity() decimal.Decimal {
	if s.lastEquity.IsPositive() {
		return s.lastEquity
	}
	return s.cash
}

func (s *simState) availableMargin(positions []*openTrade) decimal.Decimal {
	available := s.cash
	for _, position := range positions {
		if position.originalQty.IsZero() {
			continue
		}
		reserved := position.initialMargin.Mul(position.remainingQty).Div(position.originalQty)
		available = available.Sub(reserved)
	}
	if available.IsNegative() {
		return decimal.Zero
	}
	return available
}

// markToMarket records the full equity curve, including unrealized P&L and an
// estimated taker fee for closing the remaining quantity.
func (s *simState) markToMarket(mark float64, positions []*openTrade, takerFeePct decimal.Decimal) {
	equity := s.cash
	price := decimal.NewFromFloat(mark)
	for _, position := range positions {
		unrealized := price.Sub(position.trade.EntryPrice).Mul(position.remainingQty).
			Mul(decimal.NewFromInt(int64(position.trade.Direction.Sign())))
		exitFee := price.Mul(position.remainingQty).Mul(takerFeePct).Div(decimal.NewFromInt(100))
		equity = equity.Add(unrealized).Sub(exitFee)
	}
	s.recordEquity(equity)
}

func (s *simState) recordEquity(equity decimal.Decimal) {
	s.lastEquity = equity
	if equity.GreaterThan(s.peakEquity) {
		s.peakEquity = equity
		return
	}
	if s.peakEquity.IsPositive() {
		value, _ := s.peakEquity.Sub(equity).Div(s.peakEquity).Mul(decimal.NewFromInt(100)).Float64()
		if value > s.maxDrawdownPct {
			s.maxDrawdownPct = value
		}
	}
}

// metrics summarises the run.
func (s *simState) metrics(trades []domain.BacktestTrade) domain.BacktestMetrics {
	m := domain.BacktestMetrics{
		FinalCapital:    s.cash.Round(4),
		Trades:          len(trades),
		InferencesUsed:  s.inferences,
		CacheHits:       s.cacheHits,
		DegradedSteps:   s.degradedSteps,
		AnalysisPoints:  s.analysisPoints,
		UnfilledEntries: s.unfilledEntries,
		DataIssues:      s.sortedDataIssues(),
	}
	if len(s.reasons) > 0 {
		m.DecisionReasons = s.reasons
	}
	if !s.replayFrom.IsZero() {
		from, to := s.replayFrom, s.replayTo
		m.ReplayFrom, m.ReplayTo = &from, &to
	}
	if s.initial.IsPositive() {
		ret, _ := s.cash.Sub(s.initial).Div(s.initial).Mul(decimal.NewFromInt(100)).Float64()
		m.TotalReturnPct = round2(ret)
		m.MaxDrawdownPct = round2(s.maxDrawdownPct)
	}
	if len(trades) == 0 {
		return m
	}

	var grossProfit, grossLoss, fees, funding decimal.Decimal
	var returns []float64
	var mfeSum, maeSum, holdSum float64
	var longWins, shortWins int

	for _, t := range trades {
		fees = fees.Add(t.Fees)
		funding = funding.Add(t.Funding)
		switch {
		case t.NetPnL.IsPositive():
			m.Wins++
			grossProfit = grossProfit.Add(t.NetPnL)
			if t.Direction == domain.DirectionLong {
				longWins++
			} else {
				shortWins++
			}
		case t.NetPnL.IsNegative():
			m.Losses++
			grossLoss = grossLoss.Add(t.NetPnL.Abs())
		}
		if t.Direction == domain.DirectionLong {
			m.LongTrades++
		} else {
			m.ShortTrades++
		}
		returns = append(returns, t.PnLPct)
		if t.MFEPct != nil {
			mfeSum += *t.MFEPct
		}
		if t.MAEPct != nil {
			maeSum += *t.MAEPct
		}
		if t.ClosedAt != nil {
			holdSum += t.ClosedAt.Sub(t.OpenedAt).Minutes()
		}
	}

	m.TotalFees = fees.Round(4)
	m.TotalFunding = funding.Round(4)
	m.WinRate = round2(float64(m.Wins) / float64(len(trades)))
	m.AverageMFEPct = round2(mfeSum / float64(len(trades)))
	m.AverageMAEPct = round2(maeSum / float64(len(trades)))
	m.AvgHoldingMinute = round2(holdSum / float64(len(trades)))

	if m.LongTrades > 0 {
		m.LongWinRate = round2(float64(longWins) / float64(m.LongTrades))
	}
	if m.ShortTrades > 0 {
		m.ShortWinRate = round2(float64(shortWins) / float64(m.ShortTrades))
	}
	if grossLoss.IsPositive() {
		pf, _ := grossProfit.Div(grossLoss).Float64()
		pf = round2(pf)
		m.ProfitFactor = &pf
	}

	var sum float64
	for _, r := range returns {
		sum += r
	}
	m.AverageTradePct = round2(sum / float64(len(returns)))
	m.Expectancy = m.AverageTradePct

	if sharpe, ok := sharpeRatio(returns); ok {
		m.Sharpe = &sharpe
	}
	return m
}

func (s *simState) sortedDataIssues() []string {
	if len(s.dataIssues) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.dataIssues))
	for field := range s.dataIssues {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// sharpeRatio is the mean return over its standard deviation. It is reported
// per-trade and unannualised: annualising a handful of trades would imply more
// precision than the data supports.
func sharpeRatio(returns []float64) (float64, bool) {
	if len(returns) < 5 {
		return 0, false
	}
	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	var variance float64
	for _, r := range returns {
		d := r - mean
		variance += d * d
	}
	sd := math.Sqrt(variance / float64(len(returns)))
	if sd == 0 {
		return 0, false
	}
	return round2(mean / sd), true
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}
