package domain

import (
	"fmt"
	"sort"
	"time"
)

// Timeframe is a candle aggregation interval.
type Timeframe string

// Supported timeframes.
const (
	TF1m  Timeframe = "1m"
	TF5m  Timeframe = "5m"
	TF15m Timeframe = "15m"
	TF1h  Timeframe = "1h"
	TF4h  Timeframe = "4h"
	TF1d  Timeframe = "1d"
)

var timeframeDurations = map[Timeframe]time.Duration{
	TF1m:  time.Minute,
	TF5m:  5 * time.Minute,
	TF15m: 15 * time.Minute,
	TF1h:  time.Hour,
	TF4h:  4 * time.Hour,
	TF1d:  24 * time.Hour,
}

// AllTimeframes lists every supported timeframe from fastest to slowest.
var AllTimeframes = []Timeframe{TF1m, TF5m, TF15m, TF1h, TF4h, TF1d}

// Valid reports whether the timeframe is supported.
func (t Timeframe) Valid() bool {
	_, ok := timeframeDurations[t]
	return ok
}

// Duration returns the wall-clock length of one candle.
func (t Timeframe) Duration() time.Duration { return timeframeDurations[t] }

// String implements fmt.Stringer.
func (t Timeframe) String() string { return string(t) }

// Truncate returns the open time of the candle that contains ts.
// All timeframes here divide a day evenly, so UTC truncation is exact.
func (t Timeframe) Truncate(ts time.Time) time.Time {
	d := t.Duration()
	if d == 0 {
		return ts.UTC()
	}
	return ts.UTC().Truncate(d)
}

// ParseTimeframe converts an untrusted string into a Timeframe.
func ParseTimeframe(s string) (Timeframe, error) {
	tf := Timeframe(s)
	if !tf.Valid() {
		return "", fmt.Errorf("unknown timeframe %q", s)
	}
	return tf, nil
}

// ParseTimeframes converts a list, failing on the first unknown entry.
func ParseTimeframes(list []string) ([]Timeframe, error) {
	out := make([]Timeframe, 0, len(list))
	for _, s := range list {
		tf, err := ParseTimeframe(s)
		if err != nil {
			return nil, err
		}
		out = append(out, tf)
	}
	SortTimeframes(out)
	return out, nil
}

// SortTimeframes orders timeframes from fastest to slowest in place.
func SortTimeframes(tfs []Timeframe) {
	sort.Slice(tfs, func(i, j int) bool {
		return tfs[i].Duration() < tfs[j].Duration()
	})
}

// CandleSource records how a candle was obtained, which feeds data quality.
type CandleSource string

const (
	// CandleSourceNative means the provider returned real OHLC data.
	CandleSourceNative CandleSource = "native"
	// CandleSourceDerived means the candle was aggregated from finer candles.
	CandleSourceDerived CandleSource = "derived"
	// CandleSourcePolled means the candle was built from sampled price ticks.
	CandleSourcePolled CandleSource = "polled"
)
