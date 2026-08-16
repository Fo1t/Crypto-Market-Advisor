package domain

import (
	"time"

	"github.com/google/uuid"
)

// NewsProvider identifies the machine-readable transport used by a source.
type NewsProvider string

// Supported news provider kinds.
const (
	NewsProviderRSS   NewsProvider = "rss"
	NewsProviderAtom  NewsProvider = "atom"
	NewsProviderBybit NewsProvider = "bybit"
	NewsProviderGDELT NewsProvider = "gdelt"
)

// Valid reports whether the provider kind is one this build can fetch.
func (p NewsProvider) Valid() bool {
	switch p {
	case NewsProviderRSS, NewsProviderAtom, NewsProviderBybit, NewsProviderGDELT:
		return true
	default:
		return false
	}
}

// NewsSourceStatus is deliberately stable and language-neutral. UI layers
// translate these values instead of displaying the enum directly.
type NewsSourceStatus string

// Per-source availability as observed by the collector.
const (
	NewsSourceOnline   NewsSourceStatus = "online"
	NewsSourceDegraded NewsSourceStatus = "degraded"
	NewsSourceOffline  NewsSourceStatus = "offline"
	NewsSourceDisabled NewsSourceStatus = "disabled"
)

// Valid reports whether the category is one of the stable rule-based labels.
func (c NewsCategory) Valid() bool {
	switch c {
	case NewsCategoryMarket, NewsCategoryRegulation, NewsCategoryLegal,
		NewsCategorySecurity, NewsCategoryExploit, NewsCategoryHack,
		NewsCategoryExchange, NewsCategoryListing, NewsCategoryDelisting,
		NewsCategoryTradingSuspension, NewsCategoryProtocol,
		NewsCategoryNetworkUpgrade, NewsCategoryNetworkOutage, NewsCategoryETF,
		NewsCategoryInstitutional, NewsCategoryMacro, NewsCategoryMining,
		NewsCategoryStablecoin, NewsCategoryDeFi, NewsCategoryTokenomics,
		NewsCategoryPartnership, NewsCategoryOther:
		return true
	default:
		return false
	}
}

// NewsContextStatus distinguishes a healthy empty result from an outage.
type NewsContextStatus string

// News context states passed to the LLM and shown in the UI.
const (
	NewsContextOK                NewsContextStatus = "ok"
	NewsContextAvailableButEmpty NewsContextStatus = "available_but_empty"
	NewsContextDegraded          NewsContextStatus = "degraded"
	NewsContextUnavailable       NewsContextStatus = "unavailable"
	NewsContextDisabled          NewsContextStatus = "disabled"
)

// NewsCategory is assigned locally by deterministic rules.
type NewsCategory string

// The stable set of rule-based news categories.
const (
	NewsCategoryMarket            NewsCategory = "market"
	NewsCategoryRegulation        NewsCategory = "regulation"
	NewsCategoryLegal             NewsCategory = "legal"
	NewsCategorySecurity          NewsCategory = "security"
	NewsCategoryExploit           NewsCategory = "exploit"
	NewsCategoryHack              NewsCategory = "hack"
	NewsCategoryExchange          NewsCategory = "exchange"
	NewsCategoryListing           NewsCategory = "listing"
	NewsCategoryDelisting         NewsCategory = "delisting"
	NewsCategoryTradingSuspension NewsCategory = "trading_suspension"
	NewsCategoryProtocol          NewsCategory = "protocol"
	NewsCategoryNetworkUpgrade    NewsCategory = "network_upgrade"
	NewsCategoryNetworkOutage     NewsCategory = "network_outage"
	NewsCategoryETF               NewsCategory = "etf"
	NewsCategoryInstitutional     NewsCategory = "institutional"
	NewsCategoryMacro             NewsCategory = "macro"
	NewsCategoryMining            NewsCategory = "mining"
	NewsCategoryStablecoin        NewsCategory = "stablecoin"
	NewsCategoryDeFi              NewsCategory = "defi"
	NewsCategoryTokenomics        NewsCategory = "tokenomics"
	NewsCategoryPartnership       NewsCategory = "partnership"
	NewsCategoryOther             NewsCategory = "other"
)

// NewsCategoryMatch records deterministic classification confidence.
type NewsCategoryMatch struct {
	Category   NewsCategory `json:"category"`
	Confidence float64      `json:"confidence"`
}

// NewsAssetMatch records why a monitored asset was attached to an item.
type NewsAssetMatch struct {
	AssetID    int64   `json:"asset_id"`
	Symbol     string  `json:"symbol"`
	Confidence float64 `json:"confidence"`
	MatchedBy  string  `json:"matched_by"`
}

// NewsSource is a configured public feed and its conditional-GET state.
type NewsSource struct {
	ID                uuid.UUID
	Name              string
	URL               string
	CanonicalURL      string
	Provider          NewsProvider
	Priority          int
	Enabled           bool
	System            bool
	Status            NewsSourceStatus
	ETag              string
	LastModified      string
	LastAttemptAt     *time.Time
	LastSuccessAt     *time.Time
	LastError         string
	ConsecutiveErrors int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// RawNewsItem is the provider-neutral ingestion contract before persistence.
type RawNewsItem struct {
	ExternalID  string
	URL         string
	Title       string
	Summary     string
	Language    string
	PublishedAt time.Time
	Metadata    map[string]any
}

// NewsItem is a normalized material received by the application.
type NewsItem struct {
	ID              uuid.UUID
	SourceID        uuid.UUID
	ClusterID       *uuid.UUID
	ExternalID      string
	URL             string
	CanonicalURL    string
	Title           string
	NormalizedTitle string
	TitleHash       string
	Summary         string
	Language        string
	PublishedAt     time.Time
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	Metadata        map[string]any
}

// NewsWorkItem includes source facts needed by deterministic enrichment.
type NewsWorkItem struct {
	Item           NewsItem
	SourcePriority int
	SourceSystem   bool
}

// NewsCluster groups reports of the same event without embeddings or LLMs.
type NewsCluster struct {
	ID                uuid.UUID
	CanonicalTitle    string
	CanonicalSourceID *uuid.UUID
	FirstPublishedAt  time.Time
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
	Importance        float64
	Freshness         float64
	Critical          bool
	SourceCount       int
	Categories        []NewsCategory
}

// NewsClusterCandidate carries lightweight relations used for similarity.
type NewsClusterCandidate struct {
	Cluster    NewsCluster
	AssetIDs   []int64
	Categories []NewsCategory
}

// NewsAssetRef links a cluster to a tracked asset with the matcher confidence.
type NewsAssetRef struct {
	ID         int64   `json:"id"`
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
}

// NewsSourceRef identifies the feed a publication came from.
type NewsSourceRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Priority int       `json:"priority"`
	System   bool      `json:"system"`
}

// NewsPublication is one deduplicated material inside a cluster.
type NewsPublication struct {
	ID          uuid.UUID     `json:"id"`
	Source      NewsSourceRef `json:"source"`
	URL         string        `json:"url"`
	Title       string        `json:"title"`
	Summary     string        `json:"summary"`
	Language    string        `json:"language"`
	PublishedAt time.Time     `json:"published_at"`
	FirstSeenAt time.Time     `json:"first_seen_at"`
}

// NewsClusterView is the read model shared by the API and future LLM snapshot.
type NewsClusterView struct {
	ID               uuid.UUID           `json:"id"`
	CanonicalTitle   string              `json:"canonical_title"`
	CanonicalURL     string              `json:"canonical_url"`
	CanonicalSummary string              `json:"canonical_summary"`
	Language         string              `json:"language"`
	FirstPublishedAt time.Time           `json:"first_published_at"`
	FirstSeenAt      time.Time           `json:"first_seen_at"`
	LastSeenAt       time.Time           `json:"last_seen_at"`
	Importance       float64             `json:"importance"`
	Freshness        float64             `json:"freshness"`
	Critical         bool                `json:"critical"`
	SourceCount      int                 `json:"source_count"`
	PublicationCount int                 `json:"publication_count"`
	Assets           []NewsAssetRef      `json:"assets"`
	Categories       []NewsCategoryMatch `json:"categories"`
	Sources          []NewsSourceRef     `json:"sources"`
	Publications     []NewsPublication   `json:"publications,omitempty"`
	Reactions        []NewsReactionView  `json:"reactions"`
}

// NewsReactionView is the measured market reaction of one asset to a cluster.
type NewsReactionView struct {
	AssetID         int64              `json:"asset_id"`
	Symbol          string             `json:"symbol"`
	BaselineTime    *time.Time         `json:"baseline_time,omitempty"`
	BaselinePrice   *float64           `json:"baseline_price,omitempty"`
	Return5mPct     *float64           `json:"return_5m_pct,omitempty"`
	Return15mPct    *float64           `json:"return_15m_pct,omitempty"`
	Return1hPct     *float64           `json:"return_1h_pct,omitempty"`
	Return4hPct     *float64           `json:"return_4h_pct,omitempty"`
	Return24hPct    *float64           `json:"return_24h_pct,omitempty"`
	MaxUpPct        *float64           `json:"max_up_move_pct,omitempty"`
	MaxDownPct      *float64           `json:"max_down_move_pct,omitempty"`
	ObservedThrough *time.Time         `json:"observed_through,omitempty"`
	Status          NewsReactionStatus `json:"status"`
}

// NewsStats aggregates source health and ingestion counters for health checks.
type NewsStats struct {
	SourcesTotal    int                      `json:"sources_total"`
	SourcesEnabled  int                      `json:"sources_enabled"`
	SourcesByStatus map[NewsSourceStatus]int `json:"sources_by_status"`
	ItemsTotal      int                      `json:"items_total"`
	ClustersTotal   int                      `json:"clusters_total"`
	CriticalTotal   int                      `json:"critical_total"`
	LastSeenAt      *time.Time               `json:"last_seen_at,omitempty"`
}

// NewsMarketReaction is filled incrementally as each horizon closes.
type NewsMarketReaction struct {
	ClusterID         uuid.UUID
	AssetID           int64
	BaselineTime      *time.Time
	BaselinePrice     *float64
	Return5mPct       *float64
	Return5mAt        *time.Time
	Return15mPct      *float64
	Return15mAt       *time.Time
	Return1hPct       *float64
	Return1hAt        *time.Time
	Return4hPct       *float64
	Return4hAt        *time.Time
	Return24hPct      *float64
	Return24hAt       *time.Time
	MaxUpPct          *float64
	MaxDownPct        *float64
	ObservedThrough   *time.Time
	Status            NewsReactionStatus
	NextEvaluationAt  time.Time
	CompletedAt       *time.Time
	LastError         string
	EvaluationVersion int
}

// NewsReactionStatus is stable storage state, not pre-translated UI copy.
type NewsReactionStatus string

// Reaction rows stay tracking until every horizon closes or data runs out.
const (
	NewsReactionTracking         NewsReactionStatus = "tracking"
	NewsReactionComplete         NewsReactionStatus = "complete"
	NewsReactionInsufficientData NewsReactionStatus = "insufficient_data"
)

// NewsReactionTarget identifies one independently closing return horizon.
type NewsReactionTarget struct {
	Duration time.Duration
	Value    *float64
}

// NewsReactionWork is a cluster/asset pair due for evaluation.
type NewsReactionWork struct {
	ClusterID   uuid.UUID
	AssetID     int64
	FirstSeenAt time.Time
	Reaction    *NewsMarketReaction
}

// NewsReactionHistory is safe to include in a historical analysis snapshot:
// every aggregate is restricted to information observable at KnownAt.
type NewsReactionHistory struct {
	Status        string   `json:"status"`
	SampleSize    int      `json:"sample_size_1h"`
	SampleSize24h int      `json:"sample_size_24h"`
	Return1hAvg   *float64 `json:"return_1h_avg_pct,omitempty"`
	Return24hAvg  *float64 `json:"return_24h_avg_pct,omitempty"`
	WinRate1h     *float64 `json:"win_rate_1h_pct,omitempty"`
}

// NewsReactionSoFar contains only observations that existed at snapshot time.
type NewsReactionSoFar struct {
	ElapsedMinutes  int        `json:"elapsed_minutes"`
	Return5mPct     *float64   `json:"return_5m_pct,omitempty"`
	Return15mPct    *float64   `json:"return_15m_pct,omitempty"`
	Return1hPct     *float64   `json:"return_1h_pct,omitempty"`
	Return4hPct     *float64   `json:"return_4h_pct,omitempty"`
	Return24hPct    *float64   `json:"return_24h_pct,omitempty"`
	LatestReturnPct *float64   `json:"latest_return_pct,omitempty"`
	MaxUpPct        *float64   `json:"max_up_move_pct,omitempty"`
	MaxDownPct      *float64   `json:"max_down_move_pct,omitempty"`
	ObservedThrough *time.Time `json:"observed_through,omitempty"`
}

// NewsSnapshotItem is the compact, sanitized event representation used by the
// main model. It never contains article HTML or raw provider payloads.
type NewsSnapshotItem struct {
	ClusterID       uuid.UUID          `json:"cluster_id"`
	AgeMinutes      int                `json:"age_minutes"`
	CanonicalSource string             `json:"canonical_source"`
	SourceType      string             `json:"source_type"`
	Title           string             `json:"title"`
	Summary         string             `json:"summary,omitempty"`
	Assets          []string           `json:"assets"`
	Categories      []NewsCategory     `json:"categories"`
	Importance      float64            `json:"importance"`
	Freshness       float64            `json:"freshness"`
	Critical        bool               `json:"critical"`
	SourceCount     int                `json:"source_count"`
	Reaction        *NewsReactionSoFar `json:"market_reaction_so_far,omitempty"`
}

// NewsSnapshot is persisted with the technical snapshot and passed through
// the compact serializer without triggering any per-article inference.
type NewsSnapshot struct {
	Status        NewsContextStatus   `json:"status"`
	LookbackHours float64             `json:"lookback_hours"`
	AssetSpecific []NewsSnapshotItem  `json:"asset_specific"`
	Global        []NewsSnapshotItem  `json:"global"`
	Historical    NewsReactionHistory `json:"historical_news_context"`
	StatusDetail  string              `json:"status_detail,omitempty"`
}

// NewsAssessment is optional model interpretation. It is kept separate from
// factual news content and actual market reactions.
type NewsAssessment struct {
	OverallSentiment  string            `json:"overall_sentiment"`
	Impact            string            `json:"impact"`
	TimeHorizon       string            `json:"time_horizon"`
	Confidence        int               `json:"confidence"`
	ImportantClusters []uuid.UUID       `json:"important_clusters"`
	Reasons           map[string]string `json:"reasons"`
}

// NewsFetchResult carries both items and HTTP cache metadata.
type NewsFetchResult struct {
	Items        []RawNewsItem
	ETag         string
	LastModified string
	NotModified  bool
	FetchedAt    time.Time
}
