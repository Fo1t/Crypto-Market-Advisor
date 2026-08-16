package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Candle is one OHLCV bar. Prices are float64 because every consumer is an
// analytical calculation; money accounting never uses these values.
type Candle struct {
	OpenTime  time.Time    `json:"open_time"`
	CloseTime time.Time    `json:"close_time"`
	Open      float64      `json:"open"`
	High      float64      `json:"high"`
	Low       float64      `json:"low"`
	Close     float64      `json:"close"`
	Volume    float64      `json:"volume"`
	Turnover  float64      `json:"turnover"`
	Closed    bool         `json:"closed"`
	Source    CandleSource `json:"source"`
	Provider  string       `json:"provider"`
}

// Range returns high-low.
func (c Candle) Range() float64 { return c.High - c.Low }

// Body returns the absolute distance between open and close.
func (c Candle) Body() float64 {
	if c.Close >= c.Open {
		return c.Close - c.Open
	}
	return c.Open - c.Close
}

// UpperShadow returns the wick above the body.
func (c Candle) UpperShadow() float64 {
	if c.Close >= c.Open {
		return c.High - c.Close
	}
	return c.High - c.Open
}

// LowerShadow returns the wick below the body.
func (c Candle) LowerShadow() float64 {
	if c.Close >= c.Open {
		return c.Open - c.Low
	}
	return c.Close - c.Low
}

// Bullish reports whether the candle closed above its open.
func (c Candle) Bullish() bool { return c.Close > c.Open }

// Bearish reports whether the candle closed below its open.
func (c Candle) Bearish() bool { return c.Close < c.Open }

// Midpoint returns the average of high and low.
func (c Candle) Midpoint() float64 { return (c.High + c.Low) / 2 }

// TypicalPrice returns (H+L+C)/3, used by CCI, MFI and VWAP.
func (c Candle) TypicalPrice() float64 { return (c.High + c.Low + c.Close) / 3 }

// Asset is a tracked cryptocurrency.
type Asset struct {
	ID                   int64     `json:"id"`
	CoinGeckoID          string    `json:"coingecko_id"`
	Symbol               string    `json:"symbol"`
	DisplayName          string    `json:"display_name"`
	BybitSymbol          string    `json:"bybit_symbol"`
	Enabled              bool      `json:"enabled"`
	ManuallyAdded        bool      `json:"manually_added"`
	Pinned               bool      `json:"pinned"`
	ExcludedFromAutoList bool      `json:"excluded_from_auto_list"`
	MarketCapRank        *int      `json:"market_cap_rank"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// MarketInfo is the spot/market overview for an asset.
type MarketInfo struct {
	CoinGeckoID       string    `json:"coingecko_id"`
	Symbol            string    `json:"symbol"`
	Name              string    `json:"name"`
	Price             float64   `json:"price"`
	MarketCap         float64   `json:"market_cap"`
	MarketCapRank     int       `json:"market_cap_rank"`
	Volume24h         float64   `json:"volume_24h"`
	PriceChange24hPct float64   `json:"price_change_24h_pct"`
	PriceChange1hPct  *float64  `json:"price_change_1h_pct,omitempty"`
	PriceChange7dPct  *float64  `json:"price_change_7d_pct,omitempty"`
	High24h           *float64  `json:"high_24h,omitempty"`
	Low24h            *float64  `json:"low_24h,omitempty"`
	FetchedAt         time.Time `json:"fetched_at"`
}

// DataQuality describes how complete the inputs to an analysis were.
type DataQuality struct {
	Status        DataQualityStatus `json:"status"`
	MissingFields []string          `json:"missing_fields"`
	Notes         []string          `json:"notes,omitempty"`
}

// AddMissing records a missing field and degrades the status.
func (d *DataQuality) AddMissing(field string) {
	for _, f := range d.MissingFields {
		if f == field {
			return
		}
	}
	d.MissingFields = append(d.MissingFields, field)
	if d.Status == DataQualityOK {
		d.Status = DataQualityDegraded
	}
}

// AddNote records a human readable remark without changing the status.
func (d *DataQuality) AddNote(note string) {
	for _, n := range d.Notes {
		if n == note {
			return
		}
	}
	d.Notes = append(d.Notes, note)
}

// Money is the alias used for every value that participates in accounting.
type Money = decimal.Decimal

// CandleCoverage is what history exists for one asset and timeframe.
type CandleCoverage struct {
	Candles int        `json:"candles"`
	From    *time.Time `json:"from,omitempty"`
	To      *time.Time `json:"to,omitempty"`
}

// FundingRate is one settled funding payment of a perpetual contract, as the
// exchange published it. Rate is a fraction of notional per settlement - the
// unit Bybit uses - so 0.0001 means one basis point every eight hours. A long
// pays a positive rate and receives a negative one.
type FundingRate struct {
	SettledAt time.Time `json:"settled_at"`
	Rate      float64   `json:"rate"`
}

// Pct returns the same rate in percent, which is the unit the backtest
// parameters and the UI speak.
func (f FundingRate) Pct() float64 { return f.Rate * 100 }
