package news

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

const reactionEvaluationVersion = 1

var reactionHorizons = []time.Duration{
	5 * time.Minute,
	15 * time.Minute,
	time.Hour,
	4 * time.Hour,
	24 * time.Hour,
}

// ReactionStore is deliberately narrow so timing and no-future-leakage rules
// can be proven with deterministic unit tests independently of PostgreSQL.
type ReactionStore interface {
	ListDueReactions(context.Context, time.Time, int) ([]domain.NewsReactionWork, error)
	FirstClosed5mCandle(context.Context, int64, time.Time, time.Time) (domain.Candle, bool, error)
	ReactionExtremes(context.Context, int64, time.Time, time.Time) (*float64, *float64, *time.Time, error)
	UpsertReaction(context.Context, domain.NewsMarketReaction) error
}

// ReactionStats describes one incremental evaluation pass.
type ReactionStats struct {
	Due              int
	Updated          int
	Completed        int
	InsufficientData int
}

// ReactionTracker incrementally fills event returns as horizons become known.
type ReactionTracker struct {
	mu    sync.RWMutex
	cfg   config.NewsConfig
	store ReactionStore
	log   *slog.Logger
	now   func() time.Time
}

// SetConfig applies live reaction scheduling controls.
func (t *ReactionTracker) SetConfig(cfg config.NewsConfig) {
	t.mu.Lock()
	t.cfg = cfg
	t.mu.Unlock()
}

func (t *ReactionTracker) config() config.NewsConfig {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cfg
}

// NewReactionTracker builds the tracker that measures market reaction to news
// using closed candles only.
func NewReactionTracker(cfg config.NewsConfig, store ReactionStore, logger *slog.Logger) *ReactionTracker {
	return &ReactionTracker{
		cfg: cfg, store: store,
		log: logging.For(logger, logging.CategoryNews),
		now: func() time.Time { return time.Now().UTC() },
	}
}

// ProcessDue evaluates a bounded queue. One broken pair is reported but does
// not prevent unrelated assets and clusters from progressing.
func (t *ReactionTracker) ProcessDue(ctx context.Context, limit int) (ReactionStats, error) {
	now := t.now().UTC()
	work, err := t.store.ListDueReactions(ctx, now, limit)
	if err != nil {
		return ReactionStats{}, err
	}
	stats := ReactionStats{Due: len(work)}
	var failures []error
	for _, item := range work {
		reaction, err := t.evaluate(ctx, item, now)
		if err != nil {
			failures = append(failures, fmt.Errorf("cluster %s asset %d: %w", item.ClusterID, item.AssetID, err))
			continue
		}
		if err := t.store.UpsertReaction(ctx, reaction); err != nil {
			failures = append(failures, fmt.Errorf("cluster %s asset %d: %w", item.ClusterID, item.AssetID, err))
			continue
		}
		stats.Updated++
		switch reaction.Status {
		case domain.NewsReactionComplete:
			stats.Completed++
		case domain.NewsReactionInsufficientData:
			stats.InsufficientData++
		}
	}
	return stats, errors.Join(failures...)
}

func (t *ReactionTracker) evaluate(ctx context.Context, work domain.NewsReactionWork, now time.Time) (domain.NewsMarketReaction, error) {
	cfg := t.config()
	reaction := domain.NewsMarketReaction{
		ClusterID: work.ClusterID, AssetID: work.AssetID,
		Status: domain.NewsReactionTracking, EvaluationVersion: reactionEvaluationVersion,
	}
	if work.Reaction != nil {
		reaction = *work.Reaction
		reaction.EvaluationVersion = reactionEvaluationVersion
		reaction.LastError = ""
	}

	if reaction.BaselineTime == nil || reaction.BaselinePrice == nil {
		candle, found, err := t.store.FirstClosed5mCandle(ctx, work.AssetID, work.FirstSeenAt, now)
		if err != nil {
			return reaction, err
		}
		if !found {
			deadline := work.FirstSeenAt.Add(cfg.ReactionBaselineGrace)
			if !now.Before(deadline) {
				reaction.Status = domain.NewsReactionInsufficientData
				reaction.CompletedAt = timePtr(now)
				reaction.NextEvaluationAt = now
				reaction.LastError = "no closed 5m candle available within baseline grace period"
				return reaction, nil
			}
			reaction.Status = domain.NewsReactionTracking
			reaction.NextEvaluationAt = minTime(deadline, now.Add(t.pollInterval()))
			return reaction, nil
		}
		if candle.Close <= 0 {
			return reaction, fmt.Errorf("baseline candle has non-positive close %.8f", candle.Close)
		}
		baselineTime := candle.CloseTime.UTC()
		baselinePrice := candle.Close
		reaction.BaselineTime = &baselineTime
		reaction.BaselinePrice = &baselinePrice
	}

	baselineTime := reaction.BaselineTime.UTC()
	baselinePrice := *reaction.BaselinePrice
	values := []**float64{
		&reaction.Return5mPct,
		&reaction.Return15mPct,
		&reaction.Return1hPct,
		&reaction.Return4hPct,
		&reaction.Return24hPct,
	}
	observedAt := []**time.Time{
		&reaction.Return5mAt,
		&reaction.Return15mAt,
		&reaction.Return1hAt,
		&reaction.Return4hAt,
		&reaction.Return24hAt,
	}
	for index, horizon := range reactionHorizons {
		if *values[index] != nil {
			continue
		}
		target := baselineTime.Add(horizon)
		if now.Before(target) {
			continue
		}
		candle, found, err := t.store.FirstClosed5mCandle(ctx, work.AssetID, target, now)
		if err != nil {
			return reaction, err
		}
		if found {
			value := percentChange(baselinePrice, candle.Close)
			*values[index] = &value
			closeTime := candle.CloseTime.UTC()
			*observedAt[index] = &closeTime
		}
	}

	observedUntil := minTime(now, baselineTime.Add(24*time.Hour))
	maxHigh, minLow, observedThrough, err := t.store.ReactionExtremes(ctx, work.AssetID, baselineTime, observedUntil)
	if err != nil {
		return reaction, err
	}
	if observedThrough != nil {
		reaction.ObservedThrough = observedThrough
	}
	if maxHigh != nil {
		value := percentChange(baselinePrice, *maxHigh)
		if value < 0 {
			value = 0
		}
		reaction.MaxUpPct = &value
	}
	if minLow != nil {
		value := percentChange(baselinePrice, *minLow)
		if value > 0 {
			value = 0
		}
		reaction.MaxDownPct = &value
	}

	if reaction.Return24hPct != nil {
		reaction.Status = domain.NewsReactionComplete
		reaction.CompletedAt = timePtr(now)
		reaction.NextEvaluationAt = now
		return reaction, nil
	}
	if !now.Before(baselineTime.Add(24*time.Hour + t.cfg.ReactionBaselineGrace)) {
		reaction.Status = domain.NewsReactionInsufficientData
		reaction.CompletedAt = timePtr(now)
		reaction.NextEvaluationAt = now
		reaction.LastError = "24h horizon candle unavailable after grace period"
		return reaction, nil
	}

	reaction.Status = domain.NewsReactionTracking
	reaction.CompletedAt = nil
	reaction.NextEvaluationAt = t.nextDue(reaction, now)
	return reaction, nil
}

func (t *ReactionTracker) nextDue(reaction domain.NewsMarketReaction, now time.Time) time.Time {
	values := []*float64{
		reaction.Return5mPct, reaction.Return15mPct, reaction.Return1hPct,
		reaction.Return4hPct, reaction.Return24hPct,
	}
	for index, value := range values {
		if value != nil {
			continue
		}
		target := reaction.BaselineTime.Add(reactionHorizons[index])
		if target.After(now) {
			return target
		}
		return now.Add(t.pollInterval())
	}
	return now.Add(t.pollInterval())
}

func (t *ReactionTracker) pollInterval() time.Duration {
	cfg := t.config()
	if cfg.ReactionInterval < time.Minute {
		return 5 * time.Minute
	}
	return cfg.ReactionInterval
}

func percentChange(baseline, value float64) float64 {
	return (value/baseline - 1) * 100
}

func timePtr(value time.Time) *time.Time { return &value }

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
