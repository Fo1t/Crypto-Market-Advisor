package strategies

import "github.com/crypto-market-advisor/advisor/internal/domain"

// Preset is a whole decision policy that was measured as a unit, not a
// suggestion assembled by taste. Each one below carries the figures it produced
// on the stored history so the settings screen can show what it is offering
// instead of a bare name.
//
// Every profile was replayed over the same two grids: seven four-hour windows
// spanning five years (three of them falling markets) and four separate years of
// daily bars, on fourteen assets, with the actual funding of each perpetual
// charged every eight hours.
type Preset struct {
	ID string `json:"id"`
	// Set is the policy itself.
	Set domain.StrategySet `json:"set"`
	// ProfitFactor4h and ProfitFactor1d are the pooled figures of the measurement.
	ProfitFactor4h float64 `json:"profit_factor_4h"`
	ProfitFactor1d float64 `json:"profit_factor_1d"`
	// WorstWindow4h and WorstWindow1d are the weakest single window of each grid,
	// which is what a pooled figure hides.
	WorstWindow4h float64 `json:"worst_window_4h"`
	WorstWindow1d float64 `json:"worst_window_1d"`
	// Trades4h and Trades1d say how much evidence the figures rest on.
	Trades4h int `json:"trades_4h"`
	Trades1d int `json:"trades_1d"`
	// Default marks the profile a fresh installation starts from.
	Default bool `json:"default"`
}

// Preset identifiers. Stable machine keys: the UI translates them.
const (
	PresetRegimeMomentum = "regime_momentum_pair"
	PresetEnsemble       = "broad_ensemble"
	PresetCoreTrio       = "core_trio"
	PresetMomentumSolo   = "momentum_solo"
)

// presetWeights builds a policy from a set of weights, leaving every other
// strategy switched off and every filter at the catalog default.
func presetWeights(minSignal float64, weights map[string]float64) domain.StrategySet {
	items := make([]domain.StrategyConfig, 0, len(catalog))
	for _, def := range catalog {
		item := domain.StrategyConfig{ID: def.ID, Enabled: def.Enabled, Weight: def.Weight, HardVeto: def.HardVeto}
		if def.Kind == domain.StrategyDirectional {
			weight, listed := weights[def.ID]
			item.Enabled, item.Weight = listed, weight
		}
		items = append(items, item)
	}
	return domain.StrategySet{MinSignal: minSignal, Items: items, Sides: DefaultSides}
}

// Presets returns every measured profile, the default one first.
func Presets() []Preset {
	return []Preset{
		{
			// Two rules that say the same thing in different languages: trade with
			// a trend the market as a whole confirms. The first fires when price has
			// already run, which the forward-return study found to be the strongest
			// effect in the data; the second keeps it honest about direction.
			ID: PresetRegimeMomentum,
			Set: presetWeights(1.4, map[string]float64{
				IDRegimeMomentum: 1.5,
				IDSuperTrend:     1.0,
			}),
			ProfitFactor4h: 1.27, ProfitFactor1d: 1.56,
			WorstWindow4h: 0.54, WorstWindow1d: 0.62,
			Trades4h: 888, Trades1d: 327,
			Default: true,
		},
		{
			// The profile the project used before: fourteen opinions, each with a
			// small weight. It trades far more and earns less per trade, but its
			// weakest daily window is the mildest of the four profiles.
			ID: PresetEnsemble,
			Set: presetWeights(1.8, map[string]float64{
				IDMACD: 1.0, IDMarketStructure: 0.9, IDMomentum: 0.8, IDEMATrend: 0.8,
				IDRSIReversion: 0.8, IDADXDirectional: 0.7, IDCompositeScore: 0.6,
				IDVWAP: 0.6, IDChartPatterns: 0.6, IDCandlePatterns: 0.6, IDBreakout: 0.5,
				IDVolumeConfirmation: 0.3, IDDivergences: 0.3, IDRSI2Reversion: 1.0,
			}),
			ProfitFactor4h: 1.04, ProfitFactor1d: 1.27,
			WorstWindow4h: 0.52, WorstWindow1d: 1.00,
			Trades4h: 1404, Trades1d: 482,
		},
		{
			// The three strategies that ranked highest on their own. A middle
			// ground: more trades than the momentum pair, better figures than the
			// broad ensemble.
			ID: PresetCoreTrio,
			Set: presetWeights(1.6, map[string]float64{
				IDMACD: 1.0, IDVWAP: 1.0, IDSuperTrend: 1.0,
			}),
			ProfitFactor4h: 1.07, ProfitFactor1d: 1.33,
			WorstWindow4h: 0.50, WorstWindow1d: 0.91,
			Trades4h: 1151, Trades1d: 404,
		},
		{
			// The measured effect on its own, with nothing to dilute it. The best
			// figure per trade and the harshest weakest window: it trades only while
			// the market as a whole is rising, so a falling year gives it almost
			// nothing to work with.
			ID: PresetMomentumSolo,
			Set: presetWeights(0.3, map[string]float64{
				IDRegimeMomentum: 1.0,
			}),
			ProfitFactor4h: 1.18, ProfitFactor1d: 1.43,
			WorstWindow4h: 0.26, WorstWindow1d: 0.40,
			Trades4h: 732, Trades1d: 280,
		},
	}
}

// DefaultPreset returns the profile a fresh installation starts from.
func DefaultPreset() Preset {
	for _, preset := range Presets() {
		if preset.Default {
			return preset
		}
	}
	return Presets()[0]
}
