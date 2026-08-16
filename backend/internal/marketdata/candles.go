// Package marketdata ingests external market data and turns it into the OHLCV
// series the analysis layer consumes.
package marketdata

import "github.com/crypto-market-advisor/advisor/internal/domain"

// ClosedOnly filters out the still-forming candle, which is what every analysis
// path must use to avoid acting on an incomplete bar.
func ClosedOnly(candles []domain.Candle) []domain.Candle {
	out := make([]domain.Candle, 0, len(candles))
	for _, c := range candles {
		if c.Closed {
			out = append(out, c)
		}
	}
	return out
}
