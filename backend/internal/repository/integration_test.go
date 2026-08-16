//go:build integration

// Repository integration tests run against a real PostgreSQL instance, because
// the SQL here is written for PostgreSQL and a mock would only test the mock.
//
//	createdb advisor_test
//	TEST_DATABASE_URL=postgres://advisor:advisor@localhost:5432/advisor_test?sslmode=disable \
//	  go test -tags=integration ./internal/repository/...
package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/database"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	newsintelligence "github.com/crypto-market-advisor/advisor/internal/news"
)

func testDB(t *testing.T) (*pgxpool.Pool, *Repositories) {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	logger := logging.New("error", "text")

	if err := database.Migrate(url, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	db, err := database.Connect(ctx, config.DatabaseConfig{
		URL: url, MaxConns: 4, MinConns: 1, ConnectTimeout: 10 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)

	// Each test starts from a clean slate; CASCADE covers the child tables.
	if _, err := db.Pool.Exec(ctx, `TRUNCATE assets, recommendations, positions, backtest_runs,
		news_items, news_clusters, news_asset_aliases,
		app_settings, system_status RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM news_sources WHERE NOT system`); err != nil {
		t.Fatalf("reset custom news sources: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE news_sources SET status='offline', etag='', last_modified='',
		last_attempt_at=NULL, last_success_at=NULL, last_error='', consecutive_errors=0`); err != nil {
		t.Fatalf("reset news sources: %v", err)
	}
	return db.Pool, New(db.Pool)
}

func seedAsset(t *testing.T, repos *Repositories) domain.Asset {
	t.Helper()
	rank := 1
	asset, err := repos.Assets.Create(context.Background(), domain.Asset{
		CoinGeckoID: "bitcoin", Symbol: "BTC", DisplayName: "Bitcoin",
		BybitSymbol: "BTCUSDT", Enabled: true, MarketCapRank: &rank,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return asset
}

func TestAssetLifecycle(t *testing.T) {
	_, repos := testDB(t)
	ctx := context.Background()

	asset := seedAsset(t, repos)
	if asset.ID == 0 || asset.Symbol != "BTC" {
		t.Fatalf("unexpected asset: %+v", asset)
	}

	found, err := repos.Assets.GetBySymbol(ctx, "btc")
	if err != nil || found.ID != asset.ID {
		t.Fatalf("case-insensitive lookup failed: %v", err)
	}

	// A user flag must survive an automatic rank refresh.
	enabled := false
	pinned := true
	if _, err := repos.Assets.UpdateFlags(ctx, asset.ID, AssetFlags{Enabled: &enabled, Pinned: &pinned}); err != nil {
		t.Fatalf("update flags: %v", err)
	}
	if err := repos.Assets.UpdateRank(ctx, asset.ID, 3, "Bitcoin"); err != nil {
		t.Fatalf("update rank: %v", err)
	}
	after, err := repos.Assets.GetByID(ctx, asset.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Enabled || !after.Pinned {
		t.Fatal("an automatic rank refresh must not clobber user flags")
	}
	if after.MarketCapRank == nil || *after.MarketCapRank != 3 {
		t.Fatalf("rank was not updated: %+v", after.MarketCapRank)
	}

	if err := repos.Assets.Delete(ctx, asset.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repos.Assets.GetByID(ctx, asset.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNewsSourceLifecycle(t *testing.T) {
	pool, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)

	source, err := repos.News.UpsertSource(ctx, domain.NewsSource{
		Name: "Integration Feed", URL: "https://example.com/feed.xml",
		CanonicalURL: "https://example.com/feed.xml", Provider: domain.NewsProviderAtom,
		Priority: 55, Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert news source: %v", err)
	}
	if source.ID == uuid.Nil || source.Status != domain.NewsSourceOffline {
		t.Fatalf("unexpected source: %+v", source)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repos.News.RecordFetchFailure(ctx, source.ID, now, "temporary upstream error"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	failed, err := repos.News.GetSource(ctx, source.ID)
	if err != nil || failed.Status != domain.NewsSourceDegraded || failed.ConsecutiveErrors != 1 {
		t.Fatalf("failed source state: %+v, %v", failed, err)
	}

	if err := repos.News.RecordFetchSuccess(ctx, source.ID, now.Add(time.Minute), `"v2"`, "Fri, 14 Aug 2026 10:00:00 GMT"); err != nil {
		t.Fatalf("record success: %v", err)
	}
	healthy, err := repos.News.GetSource(ctx, source.ID)
	if err != nil || healthy.Status != domain.NewsSourceOnline || healthy.ConsecutiveErrors != 0 || healthy.ETag != `"v2"` {
		t.Fatalf("healthy source state: %+v, %v", healthy, err)
	}

	listed, err := repos.News.ListSources(ctx)
	if err != nil || len(listed) < 5 { // four system feeds plus this one
		t.Fatalf("list sources: count=%d err=%v", len(listed), err)
	}

	item := domain.NewsItem{
		ID: uuid.New(), SourceID: source.ID, ExternalID: "article-1",
		URL: "https://example.com/article/1", CanonicalURL: "https://example.com/article/1",
		Title: "BTC update", NormalizedTitle: "btc update", TitleHash: "hash-1",
		Language: "en", PublishedAt: now, FirstSeenAt: now, LastSeenAt: now,
		Metadata: map[string]any{"provider": "integration"},
	}
	inserted, existing, err := repos.News.UpsertItems(ctx, []domain.NewsItem{item})
	if err != nil || inserted != 1 || existing != 0 {
		t.Fatalf("first item upsert: inserted=%d existing=%d err=%v", inserted, existing, err)
	}
	item.ID = uuid.New()
	item.LastSeenAt = now.Add(time.Minute)
	inserted, existing, err = repos.News.UpsertItems(ctx, []domain.NewsItem{item})
	if err != nil || inserted != 0 || existing != 1 {
		t.Fatalf("idempotent item upsert: inserted=%d existing=%d err=%v", inserted, existing, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM news_items WHERE source_id = $1`, source.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored item count=%d err=%v", count, err)
	}

	coinDeskID := uuid.MustParse("10000000-0000-4000-8000-000000000003")
	for index, candidate := range []domain.NewsItem{
		{
			ID: uuid.New(), SourceID: source.ID, ExternalID: "etf-1",
			URL: "https://example.com/etf-1", CanonicalURL: "https://example.com/etf-1",
			Title:           "SEC approves spot Bitcoin ETF applications",
			NormalizedTitle: "sec approves spot bitcoin etf applications", TitleHash: "etf-hash-1",
			Language: "en", PublishedAt: now.Add(time.Minute),
			FirstSeenAt: now.Add(time.Minute), LastSeenAt: now.Add(time.Minute),
		},
		{
			ID: uuid.New(), SourceID: coinDeskID, ExternalID: "etf-2",
			URL: "https://coindesk.com/etf-2", CanonicalURL: "https://coindesk.com/etf-2",
			Title:           "Spot Bitcoin ETF applications approved by SEC",
			NormalizedTitle: "spot bitcoin etf applications approved by sec", TitleHash: "etf-hash-2",
			Language: "en", PublishedAt: now.Add(2 * time.Minute),
			FirstSeenAt: now.Add(2 * time.Minute), LastSeenAt: now.Add(2 * time.Minute),
		},
	} {
		if inserted, _, err := repos.News.UpsertItems(ctx, []domain.NewsItem{candidate}); err != nil || inserted != 1 {
			t.Fatalf("insert cluster candidate %d: inserted=%d err=%v", index, inserted, err)
		}
	}

	processor := newsintelligence.NewProcessor(config.NewsConfig{
		Enabled: true, ClusterTimeWindow: 6 * time.Hour, TitleSimilarityThreshold: 0.72,
	}, repos.News, repos.Assets, logging.New("error", "text"))
	stats, err := processor.ProcessPending(ctx)
	if err != nil {
		t.Fatalf("process pending news: %v", err)
	}
	if stats.ItemsProcessed != 3 || stats.ClustersCreated != 2 || stats.ItemsMerged != 1 {
		t.Fatalf("unexpected enrichment stats: %+v", stats)
	}
	var clusters, linkedAssets, etfCategories int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM news_clusters`).Scan(&clusters); err != nil || clusters != 2 {
		t.Fatalf("cluster count=%d err=%v", clusters, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM news_item_assets WHERE asset_id = $1`, asset.ID).Scan(&linkedAssets); err != nil || linkedAssets != 3 {
		t.Fatalf("linked asset count=%d err=%v", linkedAssets, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM news_cluster_categories WHERE category = 'etf'`).Scan(&etfCategories); err != nil || etfCategories != 1 {
		t.Fatalf("ETF category count=%d err=%v", etfCategories, err)
	}
}

func TestNewsReactionTrackingAndKnownAtBoundary(t *testing.T) {
	pool, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)

	firstSeen := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	clusterID := uuid.New()
	sourceID := uuid.MustParse("10000000-0000-4000-8000-000000000003")
	if _, err := pool.Exec(ctx, `
		INSERT INTO news_clusters (
			id, canonical_title, canonical_source_id, first_published_at,
			first_seen_at, last_seen_at, importance, freshness, critical,
			source_count, algorithm_version
		) VALUES ($1,'Known event',$2,$3,$3,$3,0.8,0.9,false,1,1)`,
		clusterID, sourceID, firstSeen); err != nil {
		t.Fatalf("seed news cluster: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO news_cluster_assets (cluster_id, asset_id, confidence)
		VALUES ($1,$2,1)`, clusterID, asset.ID); err != nil {
		t.Fatalf("attach cluster asset: %v", err)
	}

	baselineClose := firstSeen.Add(5 * time.Minute)
	if err := repos.Candles.UpsertMany(ctx, asset.ID, domain.TF5m, []domain.Candle{
		{OpenTime: baselineClose.Add(-5 * time.Minute), CloseTime: baselineClose, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10, Closed: true, Source: domain.CandleSourceNative},
		{OpenTime: baselineClose, CloseTime: baselineClose.Add(5 * time.Minute), Open: 100, High: 106, Low: 98, Close: 105, Volume: 12, Closed: true, Source: domain.CandleSourceNative},
		{OpenTime: time.Now().UTC().Add(5 * time.Minute), CloseTime: time.Now().UTC().Add(10 * time.Minute), Open: 105, High: 130, Low: 104, Close: 125, Volume: 15, Closed: true, Source: domain.CandleSourceNative},
	}); err != nil {
		t.Fatalf("seed reaction candles: %v", err)
	}

	tracker := newsintelligence.NewReactionTracker(config.NewsConfig{
		ReactionInterval: 5 * time.Minute, ReactionBaselineGrace: 2 * time.Hour,
	}, repos.News, logging.New("error", "text"))
	stats, err := tracker.ProcessDue(ctx, 10)
	if err != nil {
		t.Fatalf("process reactions: %v", err)
	}
	if stats.Updated != 1 {
		t.Fatalf("unexpected reaction stats: %+v", stats)
	}
	var status string
	var return5, return15 *float64
	var return5At, return15At *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, return_5m_pct, return_5m_at, return_15m_pct, return_15m_at
		FROM news_market_reactions WHERE cluster_id=$1 AND asset_id=$2`,
		clusterID, asset.ID).Scan(&status, &return5, &return5At, &return15, &return15At); err != nil {
		t.Fatalf("load stored reaction: %v", err)
	}
	if status != string(domain.NewsReactionTracking) || return5 == nil || return5At == nil || return15 != nil || return15At != nil {
		t.Fatalf("future horizon leaked: status=%s return5=%v return15=%v", status, return5, return15)
	}

	before := firstSeen.Add(-time.Nanosecond)
	clusters, total, err := repos.News.ListClusters(ctx, NewsListFilter{KnownAt: &before})
	if err != nil || total != 0 || len(clusters) != 0 {
		t.Fatalf("cluster visible before first_seen_at: total=%d len=%d err=%v", total, len(clusters), err)
	}
	clusters, total, err = repos.News.ListClusters(ctx, NewsListFilter{KnownAt: &firstSeen})
	if err != nil || total != 1 || len(clusters) != 1 {
		t.Fatalf("cluster not visible at first_seen_at: total=%d len=%d err=%v", total, len(clusters), err)
	}

	history, err := repos.News.ReactionHistory(ctx, asset.ID, "", time.Now().UTC(), 1)
	if err != nil {
		t.Fatalf("reaction history: %v", err)
	}
	if history.Status != "insufficient_history" || history.SampleSize != 0 {
		t.Fatalf("missing sample must be explicit: %+v", history)
	}

	assetItems, globalItems, err := repos.News.ListNewsSnapshotItems(
		ctx, asset.ID, firstSeen.Add(-time.Hour), time.Now().UTC(), 8, 5,
	)
	if err != nil || len(assetItems) != 1 || len(globalItems) != 0 {
		t.Fatalf("snapshot items: asset=%d global=%d err=%v", len(assetItems), len(globalItems), err)
	}
	if assetItems[0].Reaction == nil || assetItems[0].Reaction.Return5mPct == nil || assetItems[0].Reaction.Return15mPct != nil {
		t.Fatalf("snapshot exposed wrong horizons: %+v", assetItems[0].Reaction)
	}

	futureObservedAt := time.Now().UTC().Add(time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE news_market_reactions
		SET return_1h_pct=999, return_1h_at=$3
		WHERE cluster_id=$1 AND asset_id=$2`, clusterID, asset.ID, futureObservedAt); err != nil {
		t.Fatalf("seed future historical outcome: %v", err)
	}
	history, err = repos.News.ReactionHistory(ctx, asset.ID, "", time.Now().UTC(), 1)
	if err != nil || history.SampleSize != 0 || history.Status != "insufficient_history" {
		t.Fatalf("future observed outcome leaked into history: %+v err=%v", history, err)
	}
	history, err = repos.News.ReactionHistory(ctx, asset.ID, "", futureObservedAt.Add(time.Second), 1)
	if err != nil || history.SampleSize != 1 || history.Return1hAvg == nil || *history.Return1hAvg != 999 {
		t.Fatalf("mature observed outcome missing from history: %+v err=%v", history, err)
	}
}

func TestCandleUpsertAndOrdering(t *testing.T) {
	_, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)

	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	candles := make([]domain.Candle, 0, 10)
	for i := 0; i < 10; i++ {
		open := base.Add(time.Duration(i) * time.Hour)
		candles = append(candles, domain.Candle{
			OpenTime: open, CloseTime: open.Add(time.Hour),
			Open: 100 + float64(i), High: 101 + float64(i), Low: 99 + float64(i), Close: 100.5 + float64(i),
			Volume: 10, Closed: i < 9, Source: domain.CandleSourceNative,
		})
	}
	if err := repos.Candles.UpsertMany(ctx, asset.ID, domain.TF1h, candles); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Re-upserting the same bars must not duplicate them.
	if err := repos.Candles.UpsertMany(ctx, asset.ID, domain.TF1h, candles); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	closed, err := repos.Candles.Latest(ctx, asset.ID, domain.TF1h, 100, true)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(closed) != 9 {
		t.Fatalf("expected 9 closed candles, got %d", len(closed))
	}
	for i := 1; i < len(closed); i++ {
		if !closed[i].OpenTime.After(closed[i-1].OpenTime) {
			t.Fatal("candles must be returned in ascending time order")
		}
	}

	all, err := repos.Candles.Latest(ctx, asset.ID, domain.TF1h, 100, false)
	if err != nil || len(all) != 10 {
		t.Fatalf("expected 10 candles including the forming one, got %d (%v)", len(all), err)
	}

	ranged, err := repos.Candles.Range(ctx, asset.ID, domain.TF1h, base, base.Add(3*time.Hour))
	if err != nil || len(ranged) != 4 {
		t.Fatalf("expected 4 candles in range, got %d (%v)", len(ranged), err)
	}
}

func TestPositionEventSourcing(t *testing.T) {
	_, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)

	qty := decimal.RequireFromString("1")
	position := domain.Position{
		ID: uuid.New(), AssetID: asset.ID, Symbol: asset.Symbol,
		Direction: domain.DirectionLong, Status: domain.PositionOpen,
		EntryPrice: decimal.RequireFromString("100000"), Leverage: decimal.RequireFromString("10"),
		InitialQuantity: &qty, RemainingQuantity: &qty, SizeKnown: true,
		OpenedAt: time.Now().UTC(), FeeType: domain.FeeTaker,
	}
	openFill := domain.Fill{
		ID: uuid.New(), PositionID: position.ID, Kind: domain.FillOpen,
		Price: position.EntryPrice, Fee: decimal.RequireFromString("55"),
		FeeType: domain.FeeTaker, Quantity: &qty, ExecutedAt: position.OpenedAt,
	}
	if err := repos.Positions.Create(ctx, position, openFill); err != nil {
		t.Fatalf("create position: %v", err)
	}

	half := decimal.RequireFromString("0.5")
	closeFill := domain.Fill{
		ID: uuid.New(), PositionID: position.ID, Kind: domain.FillClose,
		Price: decimal.RequireFromString("110000"), Fee: decimal.RequireFromString("27.5"),
		FeeType: domain.FeeTaker, Quantity: &half, RealizedPnL: decimal.RequireFromString("5000"),
		ExecutedAt: time.Now().UTC(),
	}
	updated := position
	updated.Status = domain.PositionPartiallyClosed
	updated.RemainingQuantity = &half

	if err := repos.Positions.ApplyClose(ctx, updated, closeFill, domain.EventPartialClose, map[string]any{"close_pct": "50"}); err != nil {
		t.Fatalf("apply close: %v", err)
	}

	fills, err := repos.Positions.Fills(ctx, position.ID)
	if err != nil || len(fills) != 2 {
		t.Fatalf("expected 2 fills, got %d (%v)", len(fills), err)
	}
	if !fills[1].RealizedPnL.Equal(decimal.RequireFromString("5000")) {
		t.Fatalf("realized P&L lost precision: %s", fills[1].RealizedPnL)
	}

	events, err := repos.Positions.Events(ctx, position.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("expected OPENED and PARTIAL_CLOSE events, got %d (%v)", len(events), err)
	}

	stored, err := repos.Positions.Get(ctx, position.ID)
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if stored.Status != domain.PositionPartiallyClosed || !stored.RemainingQuantity.Equal(half) {
		t.Fatalf("cached state was not updated: %+v", stored)
	}
	if !stored.EntryPrice.Equal(position.EntryPrice) {
		t.Fatalf("entry price lost precision: %s", stored.EntryPrice)
	}
}

func TestRecommendationIsImmutableAndOutcomeIsSeparate(t *testing.T) {
	_, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)

	rec := domain.Recommendation{
		ID: uuid.New(), AssetID: asset.ID, Symbol: asset.Symbol, CreatedAt: time.Now().UTC(),
		Action: domain.RecommendationOpenLong, Confidence: 72, RiskLevel: domain.RiskMedium,
		Summary: "test", ReferencePrice: decimal.RequireFromString("100000.123456789"),
		AllocationPct: decimal.RequireFromString("5"),
		Leverage:      domain.LeveragePlan{LLMSuggested: 20, RiskMaximum: 8, Recommended: 8},
		TakeProfit:    []domain.PriceTarget{{Price: 102000, ClosePct: 100}},
		StopLoss:      []domain.PriceTarget{{Price: 98000, ClosePct: 100}},
		SignalsFor:    []string{"uptrend"}, SignalsAgainst: []string{}, Invalidation: []string{},
		MarketRegime: domain.RegimeWeakUptrend, DataQuality: domain.DataQualityOK,
		Translations: map[string]domain.RecommendationNarrative{
			"ru": {Summary: "Тест"}, "en": {Summary: "Test"}, "zh-CN": {Summary: "测试"},
		},
	}
	if err := repos.Recommendations.Insert(ctx, rec, nil); err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}

	stored, err := repos.Recommendations.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !stored.ReferencePrice.Equal(rec.ReferencePrice) {
		t.Fatalf("reference price lost precision: %s", stored.ReferencePrice)
	}
	if stored.Leverage.LLMSuggested != 20 || stored.Leverage.Recommended != 8 {
		t.Fatalf("both leverage values must survive: %+v", stored.Leverage)
	}
	if stored.Translations["zh-CN"].Summary != "测试" {
		t.Fatalf("recommendation translations did not survive: %+v", stored.Translations)
	}

	minConfidence, maxConfidence := 70, 80
	filtered, total, err := repos.Recommendations.List(ctx, ListFilter{
		RiskLevel: string(domain.RiskMedium), DataQuality: string(domain.DataQualityOK),
		MinConfidence: &minConfidence, MaxConfidence: &maxConfidence,
	})
	if err != nil || total != 1 || len(filtered) != 1 {
		t.Fatalf("filtered list: total=%d items=%d err=%v", total, len(filtered), err)
	}

	if err := repos.Recommendations.SetDismissed(ctx, rec.ID, true); err != nil {
		t.Fatalf("dismiss recommendation: %v", err)
	}
	active, total, err := repos.Recommendations.List(ctx, ListFilter{})
	if err != nil || total != 0 || len(active) != 0 {
		t.Fatalf("dismissed recommendation leaked into active list: total=%d items=%d err=%v", total, len(active), err)
	}
	dismissed, total, err := repos.Recommendations.List(ctx, ListFilter{Visibility: "dismissed"})
	if err != nil || total != 1 || len(dismissed) != 1 || dismissed[0].DismissedAt == nil {
		t.Fatalf("dismissed list: total=%d items=%d err=%v", total, len(dismissed), err)
	}
	if err := repos.Recommendations.SetDismissed(ctx, rec.ID, false); err != nil {
		t.Fatalf("restore recommendation: %v", err)
	}
	if dismissedCount, err := repos.Recommendations.DismissAll(ctx); err != nil || dismissedCount != 1 {
		t.Fatalf("dismiss all recommendations: count=%d err=%v", dismissedCount, err)
	}
	if dismissedCount, err := repos.Recommendations.DismissAll(ctx); err != nil || dismissedCount != 0 {
		t.Fatalf("repeat dismiss all must be idempotent: count=%d err=%v", dismissedCount, err)
	}
	if err := repos.Recommendations.SetDismissed(ctx, rec.ID, false); err != nil {
		t.Fatalf("restore recommendation after bulk dismiss: %v", err)
	}

	// The outcome lives in its own table and never rewrites the prediction.
	won := domain.ResultWin
	if err := repos.Recommendations.UpsertOutcome(ctx, domain.Outcome{
		RecommendationID: rec.ID, EvaluatedAt: time.Now().UTC(), Finalized: true,
		Status: domain.OutcomeTPHit, Result: won,
	}); err != nil {
		t.Fatalf("upsert outcome: %v", err)
	}

	again, err := repos.Recommendations.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get after outcome: %v", err)
	}
	if again.Confidence != rec.Confidence || again.Action != rec.Action {
		t.Fatal("recording an outcome must not change the prediction")
	}

	outcomes, err := repos.Recommendations.Outcomes(ctx, []uuid.UUID{rec.ID})
	if err != nil || outcomes[rec.ID].Status != domain.OutcomeTPHit {
		t.Fatalf("outcome not stored: %v / %+v", err, outcomes)
	}

	pending, err := repos.Recommendations.PendingOutcomes(ctx, 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	for _, p := range pending {
		if p.ID == rec.ID {
			t.Fatal("a finalized outcome must not stay pending")
		}
	}
}

func TestBacktestSoftDeleteKeepsAuditData(t *testing.T) {
	pool, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)
	assetID := asset.ID
	now := time.Now().UTC().Truncate(time.Microsecond)
	run := domain.BacktestRun{
		ID: uuid.New(), Mode: domain.BacktestTechnical, Symbol: asset.Symbol, AssetID: &assetID,
		Timeframe: domain.TF1h, DateFrom: now.Add(-24 * time.Hour), DateTo: now,
		Status: domain.BacktestCompleted,
		Params: domain.BacktestParams{
			Mode: domain.BacktestTechnical, Symbol: asset.Symbol, Timeframe: domain.TF1h,
			DateFrom: now.Add(-24 * time.Hour), DateTo: now,
			InitialCapital: decimal.NewFromInt(10_000), AllocationPct: decimal.NewFromInt(5),
			Leverage: decimal.NewFromInt(10), SlippagePct: decimal.RequireFromString("0.02"),
		},
	}
	if err := repos.Backtests.Create(ctx, run); err != nil {
		t.Fatalf("create backtest: %v", err)
	}
	closedAt := now
	exitPrice := decimal.NewFromInt(101)
	if err := repos.Backtests.InsertTrades(ctx, []domain.BacktestTrade{{
		ID: uuid.New(), RunID: run.ID, Symbol: asset.Symbol, Direction: domain.DirectionLong,
		OpenedAt: now.Add(-time.Hour), ClosedAt: &closedAt, EntryPrice: decimal.NewFromInt(100),
		ExitPrice: &exitPrice, Quantity: decimal.NewFromInt(1), Leverage: decimal.NewFromInt(10),
		AllocationPct: decimal.NewFromInt(5), GrossPnL: decimal.NewFromInt(1),
		Fees: decimal.RequireFromString("0.1"), NetPnL: decimal.RequireFromString("0.9"),
		PnLPct: 0.9, ExitReason: "take_profit",
		Executions: []domain.BacktestExecution{{
			Kind: "take_profit", ExecutedAt: closedAt, Price: exitPrice,
			Quantity: decimal.NewFromInt(1), ClosePct: 100,
			GrossPnL: decimal.NewFromInt(1), Fee: decimal.RequireFromString("0.1"), FeeType: domain.FeeMaker,
		}},
	}}); err != nil {
		t.Fatalf("insert backtest trade: %v", err)
	}
	storedTrades, err := repos.Backtests.Trades(ctx, run.ID)
	if err != nil || len(storedTrades) != 1 || len(storedTrades[0].Executions) != 1 {
		t.Fatalf("execution audit did not round-trip: trades=%+v err=%v", storedTrades, err)
	}
	if storedTrades[0].Executions[0].FeeType != domain.FeeMaker || storedTrades[0].Executions[0].ClosePct != 100 {
		t.Fatalf("unexpected stored execution: %+v", storedTrades[0].Executions[0])
	}

	if err := repos.Backtests.SoftDelete(ctx, run.ID); err != nil {
		t.Fatalf("soft-delete backtest: %v", err)
	}
	if _, err := repos.Backtests.Get(ctx, run.ID); err != ErrNotFound {
		t.Fatalf("deleted backtest must be hidden from get, got %v", err)
	}
	listed, total, err := repos.Backtests.List(ctx, 25, 0)
	if err != nil || total != 0 || len(listed) != 0 {
		t.Fatalf("deleted backtest leaked into list: total=%d items=%d err=%v", total, len(listed), err)
	}

	var deletedAt *time.Time
	var tradeCount int
	if err := pool.QueryRow(ctx, `SELECT deleted_at FROM backtest_runs WHERE id = $1`, run.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("load audit row: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("soft-delete marker was not stored")
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM backtest_trades WHERE run_id = $1`, run.ID).Scan(&tradeCount); err != nil {
		t.Fatalf("count retained trades: %v", err)
	}
	if tradeCount != 1 {
		t.Fatalf("soft-delete removed audit trades: %d", tradeCount)
	}
}

// TestInterruptedBacktestsAreRetiredOnStartup covers the restart path: a run
// that was still active when the process stopped can never finish, so startup
// has to retire it instead of leaving a permanently "running" row that the UI
// refuses to delete.
func TestInterruptedBacktestsAreRetiredOnStartup(t *testing.T) {
	pool, repos := testDB(t)
	ctx := context.Background()
	asset := seedAsset(t, repos)
	assetID := asset.ID
	now := time.Now().UTC().Truncate(time.Microsecond)

	newRun := func(status domain.BacktestStatus) domain.BacktestRun {
		return domain.BacktestRun{
			ID: uuid.New(), Mode: domain.BacktestLLM, Symbol: asset.Symbol, AssetID: &assetID,
			Timeframe: domain.TF1h, DateFrom: now.Add(-24 * time.Hour), DateTo: now, Status: status,
			Params: domain.BacktestParams{
				Mode: domain.BacktestLLM, Symbol: asset.Symbol, Timeframe: domain.TF1h,
				DateFrom: now.Add(-24 * time.Hour), DateTo: now,
				InitialCapital: decimal.NewFromInt(10_000), AllocationPct: decimal.NewFromInt(5),
			},
		}
	}
	running, pending, done := newRun(domain.BacktestRunning), newRun(domain.BacktestPending), newRun(domain.BacktestCompleted)
	for _, run := range []domain.BacktestRun{running, pending, done} {
		if err := repos.Backtests.Create(ctx, run); err != nil {
			t.Fatalf("create backtest: %v", err)
		}
	}

	retired, err := repos.Backtests.CleanupInterrupted(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if retired != 2 {
		t.Fatalf("both active runs must be retired, got %d", retired)
	}

	listed, total, err := repos.Backtests.List(ctx, 25, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(listed) != 1 || listed[0].ID != done.ID {
		t.Fatalf("only the finished run may stay visible: total=%d items=%+v", total, listed)
	}

	// The rows themselves survive, carrying the reason they were retired.
	var status, message string
	if err := pool.QueryRow(ctx,
		`SELECT status, error_message FROM backtest_runs WHERE id = $1`, running.ID).Scan(&status, &message); err != nil {
		t.Fatalf("load retired row: %v", err)
	}
	if status != string(domain.BacktestCanceled) || message == "" {
		t.Fatalf("retired run must be canceled with a reason: status=%q message=%q", status, message)
	}

	// Running it again on a clean state must retire nothing.
	if retired, err = repos.Backtests.CleanupInterrupted(ctx); err != nil || retired != 0 {
		t.Fatalf("cleanup must be idempotent: retired=%d err=%v", retired, err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	_, repos := testDB(t)
	ctx := context.Background()

	type doc struct {
		Language string `json:"language"`
		Leverage int    `json:"leverage"`
	}
	if err := repos.Settings.Put(ctx, "test", doc{Language: "ru", Leverage: 8}); err != nil {
		t.Fatalf("put: %v", err)
	}

	var loaded doc
	found, err := repos.Settings.Get(ctx, "test", &loaded)
	if err != nil || !found || loaded.Language != "ru" || loaded.Leverage != 8 {
		t.Fatalf("round trip failed: %v %v %+v", err, found, loaded)
	}

	var missing doc
	found, err = repos.Settings.Get(ctx, "absent", &missing)
	if err != nil || found {
		t.Fatalf("absent key must report not found: %v %v", err, found)
	}
}

func TestMigrationsAreReversible(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	logger := logging.New("error", "text")

	if err := database.Migrate(url, logger); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := database.MigrateDown(url); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := database.Migrate(url, logger); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
