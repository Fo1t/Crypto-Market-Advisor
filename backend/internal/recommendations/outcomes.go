package recommendations

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// OutcomeHorizon is how long after a recommendation its outcome is finalised.
const OutcomeHorizon = 24 * time.Hour

// EvaluateOutcomes updates the market outcome of recommendations that are not
// final yet. Predictions themselves are never modified: outcomes live in their
// own table so the historical record stays honest.
func (s *Service) EvaluateOutcomes(ctx context.Context, limit int) (int, error) {
	pending, err := s.repos.Recommendations.PendingOutcomes(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("load pending outcomes: %w", err)
	}

	updated := 0
	for _, rec := range pending {
		outcome, err := s.evaluateOne(ctx, rec)
		if err != nil {
			s.log.Warn("outcome evaluation failed",
				slog.String("symbol", rec.Symbol),
				slog.String("error", err.Error()))
			continue
		}
		if outcome == nil {
			continue
		}
		if err := s.repos.Recommendations.UpsertOutcome(ctx, *outcome); err != nil {
			s.log.Warn("store outcome failed", slog.String("error", err.Error()))
			continue
		}
		updated++
	}
	return updated, nil
}

func (s *Service) evaluateOne(ctx context.Context, rec domain.Recommendation) (*domain.Outcome, error) {
	now := time.Now().UTC()
	age := now.Sub(rec.CreatedAt)

	outcome := domain.Outcome{
		RecommendationID: rec.ID,
		EvaluatedAt:      now,
		Status:           domain.OutcomePending,
	}

	// The fine timeframe resolves same-candle TP/SL conflicts when available.
	fine, err := s.repos.Candles.Range(ctx, rec.AssetID, domain.TF1m, rec.CreatedAt, now)
	if err != nil {
		return nil, err
	}
	coarse, err := s.repos.Candles.Range(ctx, rec.AssetID, domain.TF5m, rec.CreatedAt, now)
	if err != nil {
		return nil, err
	}

	series := coarse
	if len(fine) > len(coarse) {
		series = fine
	}
	if len(series) == 0 {
		if age > OutcomeHorizon {
			// No data ever arrived for this window; close it out honestly.
			outcome.Finalized = true
			outcome.Status = domain.OutcomeNeither
			outcome.AmbiguityReason = "no candles stored for the evaluation window"
			return &outcome, nil
		}
		return nil, nil
	}

	reference := rec.ReferencePrice.InexactFloat64()
	if reference <= 0 {
		return nil, nil
	}

	outcome.PriceAfter5m = priceAfter(series, rec.CreatedAt, 5*time.Minute)
	outcome.PriceAfter15m = priceAfter(series, rec.CreatedAt, 15*time.Minute)
	outcome.PriceAfter1h = priceAfter(series, rec.CreatedAt, time.Hour)
	outcome.PriceAfter4h = priceAfter(series, rec.CreatedAt, 4*time.Hour)
	outcome.PriceAfter24h = priceAfter(series, rec.CreatedAt, 24*time.Hour)

	direction, isEntry := rec.Action.Direction()
	if !isEntry {
		mfe, mae := excursions(series, reference, domain.DirectionLong)
		outcome.MFEPct, outcome.MAEPct = &mfe, &mae
		outcome.Status = domain.OutcomeNoTrade
		outcome.Finalized = age > OutcomeHorizon
		return &outcome, nil
	}

	mfe, mae := excursions(series, reference, direction)
	outcome.MFEPct, outcome.MAEPct = &mfe, &mae

	hit := detectHits(series, fine, rec, direction)
	outcome.FirstTPHitIndex = hit.tpIndex
	outcome.FirstSLHitIndex = hit.slIndex
	outcome.Ambiguous = hit.ambiguous
	outcome.AmbiguityReason = hit.reason

	switch {
	case hit.ambiguous:
		outcome.Status = domain.OutcomeAmbiguous
		outcome.Finalized = true
	case hit.tpIndex != nil && hit.slIndex == nil:
		outcome.Status = domain.OutcomeTPHit
		outcome.Result = domain.ResultWin
		outcome.Finalized = true
	case hit.slIndex != nil:
		outcome.Status = domain.OutcomeSLHit
		outcome.Result = domain.ResultLoss
		outcome.Finalized = true
	case age > OutcomeHorizon:
		outcome.Status = domain.OutcomeNeither
		outcome.Finalized = true
		// Neither level was reached within the horizon: classify by where the
		// price ended up relative to entry, which is the honest reading.
		if outcome.PriceAfter24h != nil {
			change := (*outcome.PriceAfter24h - reference) / reference * float64(direction.Sign())
			switch {
			case change > 0:
				outcome.Result = domain.ResultWin
			case change < 0:
				outcome.Result = domain.ResultLoss
			default:
				outcome.Result = domain.ResultBreakeven
			}
		}
	}
	return &outcome, nil
}

type hitResult struct {
	tpIndex   *int
	slIndex   *int
	ambiguous bool
	reason    string
}

// detectHits walks the candles in order and finds which level was reached first.
// When a single candle touches both a take profit and a stop loss, the order
// inside that candle is unknowable from OHLC alone; a finer series is consulted
// and, if that is not available either, the outcome is marked ambiguous rather
// than guessed.
func detectHits(series, fine []domain.Candle, rec domain.Recommendation, direction domain.Direction) hitResult {
	for _, candle := range series {
		tpIdx := firstTouched(candle, rec.TakeProfit, direction, true)
		slIdx := firstTouched(candle, rec.StopLoss, direction, false)

		switch {
		case tpIdx == nil && slIdx == nil:
			continue
		case tpIdx != nil && slIdx == nil:
			return hitResult{tpIndex: tpIdx}
		case tpIdx == nil && slIdx != nil:
			return hitResult{slIndex: slIdx}
		}

		// Both touched inside one candle: try to resolve on the finer series.
		if resolved, ok := resolveWithFiner(fine, candle, rec, direction); ok {
			return resolved
		}
		return hitResult{
			tpIndex:   tpIdx,
			slIndex:   slIdx,
			ambiguous: true,
			reason:    "take profit and stop loss were both touched inside one candle and no finer data resolves the order",
		}
	}
	return hitResult{}
}

// resolveWithFiner replays the conflicting candle on a finer timeframe.
func resolveWithFiner(fine []domain.Candle, conflict domain.Candle, rec domain.Recommendation, direction domain.Direction) (hitResult, bool) {
	if len(fine) == 0 {
		return hitResult{}, false
	}
	var covered bool
	for _, c := range fine {
		if c.OpenTime.Before(conflict.OpenTime) || !c.OpenTime.Before(conflict.CloseTime) {
			continue
		}
		covered = true

		tpIdx := firstTouched(c, rec.TakeProfit, direction, true)
		slIdx := firstTouched(c, rec.StopLoss, direction, false)
		switch {
		case tpIdx != nil && slIdx == nil:
			return hitResult{tpIndex: tpIdx}, true
		case slIdx != nil && tpIdx == nil:
			return hitResult{slIndex: slIdx}, true
		case tpIdx != nil && slIdx != nil:
			// Still ambiguous even at the finest resolution available.
			return hitResult{}, false
		}
	}
	if !covered {
		return hitResult{}, false
	}
	return hitResult{}, false
}

// firstTouched returns the index of the first level the candle reached.
func firstTouched(candle domain.Candle, levels []domain.PriceTarget, direction domain.Direction, isTakeProfit bool) *int {
	for i, level := range levels {
		var touched bool
		switch {
		case direction == domain.DirectionLong && isTakeProfit:
			touched = candle.High >= level.Price
		case direction == domain.DirectionLong && !isTakeProfit:
			touched = candle.Low <= level.Price
		case direction == domain.DirectionShort && isTakeProfit:
			touched = candle.Low <= level.Price
		default:
			touched = candle.High >= level.Price
		}
		if touched {
			idx := i
			return &idx
		}
	}
	return nil
}

// priceAfter returns the close of the candle covering created+offset.
func priceAfter(series []domain.Candle, createdAt time.Time, offset time.Duration) *float64 {
	target := createdAt.Add(offset)
	if len(series) == 0 || series[len(series)-1].CloseTime.Before(target) {
		return nil
	}
	for _, c := range series {
		if !c.CloseTime.Before(target) {
			price := c.Close
			return &price
		}
	}
	return nil
}

// excursions computes the maximum favourable and adverse moves in percent.
func excursions(series []domain.Candle, reference float64, direction domain.Direction) (mfe, mae float64) {
	if reference <= 0 {
		return 0, 0
	}
	sign := float64(direction.Sign())
	for _, c := range series {
		up := (c.High - reference) / reference * 100 * sign
		down := (c.Low - reference) / reference * 100 * sign
		mfe = math.Max(mfe, math.Max(up, down))
		mae = math.Min(mae, math.Min(up, down))
	}
	return math.Round(mfe*100) / 100, math.Round(mae*100) / 100
}
